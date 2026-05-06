package retention

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestPolicy_Validate(t *testing.T) {
	cases := []struct {
		name string
		p    RetentionPolicy
		want error
	}{
		{"valid", RetentionPolicy{DataKind: DataKindLogs, HotDays: 3, DeleteAfter: 3}, nil},
		{"empty kind", RetentionPolicy{HotDays: 3}, ErrInvalidDataKind},
		{"negative", RetentionPolicy{DataKind: DataKindLogs, HotDays: -1}, ErrNegativeRetention},
		{"cold past delete", RetentionPolicy{DataKind: DataKindLogs, ColdAfter: 90, DeleteAfter: 30}, ErrColdAfterDelete},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.Validate(); !errors.Is(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestPolicy_ShouldArchiveAndDelete(t *testing.T) {
	now := time.Now().UTC()
	pol := RetentionPolicy{DataKind: DataKindSignals, ColdAfter: 90, DeleteAfter: 365}
	old := now.Add(-100 * 24 * time.Hour)
	veryOld := now.Add(-400 * 24 * time.Hour)
	if !pol.ShouldArchive(old, now) {
		t.Fatal("100d-old should archive")
	}
	if pol.ShouldArchive(now.Add(-30*24*time.Hour), now) {
		t.Fatal("30d-old should not archive")
	}
	if !pol.ShouldDelete(veryOld, now) {
		t.Fatal("400d-old should delete")
	}
	if pol.ShouldDelete(old, now) {
		t.Fatal("100d-old should not delete (under 365)")
	}
}

func TestDefaultsFor_FreePlanHasNoColdTier(t *testing.T) {
	free := DefaultsFor("free")
	signals, ok := free.Get(DataKindSignals)
	if !ok {
		t.Fatal("free plan must have signals retention")
	}
	if signals.ColdAfter != 0 {
		t.Fatalf("free plan should not archive, got cold_after=%d", signals.ColdAfter)
	}
	if _, hasCold := free.Get(DataKindEmbeddingsCold); hasCold {
		t.Fatal("free plan should not have embeddings cold tier")
	}
}

func TestDefaultsFor_TeamArchivesSignals(t *testing.T) {
	team := DefaultsFor("team")
	pol, _ := team.Get(DataKindSignals)
	if pol.ColdAfter == 0 {
		t.Fatal("team plan must archive signals")
	}
}

func TestDefaultsFor_EnterpriseAuditMultiYear(t *testing.T) {
	ent := DefaultsFor("enterprise")
	audit, _ := ent.Get(DataKindAudit)
	if audit.DeleteAfter < 365*5 {
		t.Fatalf("enterprise audit must persist >=5y, got %d days", audit.DeleteAfter)
	}
}

func TestDefaultsFor_UnknownFallsBackToFree(t *testing.T) {
	weird := DefaultsFor("weird-plan")
	if _, ok := weird.Get(DataKindSignals); !ok {
		t.Fatal("unknown plan should still ship free defaults")
	}
}

// --- ColdTierMover + Worker tests ---

type fakeColdStore struct {
	mu sync.Mutex
	kv map[string][]byte
}

func newFakeColdStore() *fakeColdStore { return &fakeColdStore{kv: map[string][]byte{}} }

func (f *fakeColdStore) Put(_ context.Context, key string, body io.Reader) (string, error) {
	b, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(b))
	copy(cp, b)
	f.kv[key] = cp
	return "s3://bucket/" + key, nil
}

func (f *fakeColdStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.kv[key]
	if !ok {
		return nil, io.EOF
	}
	return io.NopCloser(bytesReader(b)), nil
}

func (f *fakeColdStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.kv, key)
	return nil
}

type fakeSource struct {
	rows     []HotRow
	archived map[string]string
	deleted  int
}

func (s *fakeSource) ListBefore(_ context.Context, cutoff time.Time, limit int) ([]HotRow, error) {
	out := make([]HotRow, 0, len(s.rows))
	for _, r := range s.rows {
		if r.CreatedAt.Before(cutoff) {
			out = append(out, r)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *fakeSource) MarkArchived(_ context.Context, pk, uri string) error {
	if s.archived == nil {
		s.archived = map[string]string{}
	}
	s.archived[pk] = uri
	return nil
}

func (s *fakeSource) DeleteWhereArchivedBefore(_ context.Context, cutoff time.Time) (int, error) {
	n := 0
	kept := s.rows[:0]
	for _, r := range s.rows {
		if _, isArchived := s.archived[r.PrimaryKey]; isArchived && r.CreatedAt.Before(cutoff) {
			n++
			continue
		}
		kept = append(kept, r)
	}
	s.rows = kept
	s.deleted += n
	return n, nil
}

func TestColdTierMover_ArchivesPastCutoff(t *testing.T) {
	now := time.Now().UTC()
	src := &fakeSource{rows: []HotRow{
		{PrimaryKey: "a", CreatedAt: now.Add(-100 * 24 * time.Hour), Body: []byte("payload-a")},
		{PrimaryKey: "b", CreatedAt: now.Add(-10 * 24 * time.Hour), Body: []byte("payload-b")},
	}}
	cold := newFakeColdStore()
	pol := RetentionPolicy{DataKind: DataKindSignals, ColdAfter: 90, DeleteAfter: 365}
	mover := NewColdTierMover(pol, src, cold, "signals/")
	mover.now = func() time.Time { return now }

	archived, deleted, err := mover.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if archived != 1 || deleted != 0 {
		t.Fatalf("got archived=%d deleted=%d", archived, deleted)
	}
	if _, ok := cold.kv["signals/a.bin"]; !ok {
		t.Fatal("expected a.bin in cold store")
	}
	if _, ok := cold.kv["signals/b.bin"]; ok {
		t.Fatal("b should not be archived (under cutoff)")
	}
}

func TestColdTierMover_DeletesPastDeleteCutoff(t *testing.T) {
	now := time.Now().UTC()
	src := &fakeSource{
		rows:     []HotRow{{PrimaryKey: "a", CreatedAt: now.Add(-400 * 24 * time.Hour), Body: []byte("x")}},
		archived: map[string]string{"a": "s3://bucket/signals/a.bin"},
	}
	pol := RetentionPolicy{DataKind: DataKindSignals, ColdAfter: 90, DeleteAfter: 365}
	mover := NewColdTierMover(pol, src, newFakeColdStore(), "signals/")
	mover.now = func() time.Time { return now }
	_, deleted, err := mover.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 delete, got %d", deleted)
	}
}

func TestColdTierMover_RejectsPolicyWithoutColdTier(t *testing.T) {
	pol := RetentionPolicy{DataKind: DataKindLogs, HotDays: 3, DeleteAfter: 3}
	mover := NewColdTierMover(pol, &fakeSource{}, newFakeColdStore(), "logs/")
	_, _, err := mover.Run(context.Background())
	if !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("got %v, want ErrInvalidPolicy", err)
	}
}

type fakeDeleter struct{ count int }

func (d *fakeDeleter) DeleteOlderThan(_ context.Context, _ time.Time) (int, error) {
	d.count++
	return 7, nil
}

func TestWorker_HotDeleteOnlyPath(t *testing.T) {
	pol := RetentionPolicy{DataKind: DataKindLogs, HotDays: 3, DeleteAfter: 3}
	d := &fakeDeleter{}
	w := NewWorker(pol, nil, d)
	stats, err := w.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deleted != 7 || d.count != 1 {
		t.Fatalf("got %+v calls=%d", stats, d.count)
	}
}

func TestWorker_NothingConfiguredErrors(t *testing.T) {
	pol := RetentionPolicy{DataKind: DataKindLogs}
	w := NewWorker(pol, nil, nil)
	_, err := w.Run(context.Background())
	if !errors.Is(err, ErrNothingConfigured) {
		t.Fatalf("got %v", err)
	}
}
