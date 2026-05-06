package embedding

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeTierStore struct {
	mu    sync.Mutex
	tiers map[string]RepoTier
}

func newFakeTierStore() *fakeTierStore {
	return &fakeTierStore{tiers: map[string]RepoTier{}}
}

func (f *fakeTierStore) GetTier(_ context.Context, repoID string) (RepoTier, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tiers[repoID]
	if !ok {
		return TierCold, time.Time{}, nil
	}
	return t, time.Time{}, nil
}

func (f *fakeTierStore) SetTier(_ context.Context, repoID string, t RepoTier) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tiers[repoID] = t
	return nil
}

func TestActivityTier_RecordCommit_PromotesToHot(t *testing.T) {
	store := newFakeTierStore()
	store.tiers["r1"] = TierCold
	c := NewActivityTierClassifier(ActivityConfig{
		Reader: store, Writer: store,
	})
	if err := c.RecordCommit(context.Background(), "r1"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.GetTier(context.Background(), "r1")
	if got != TierHot {
		t.Errorf("expected hot, got %s", got)
	}
}

func TestActivityTier_RecordSearchHit_BelowThreshold(t *testing.T) {
	store := newFakeTierStore()
	store.tiers["r1"] = TierCold
	c := NewActivityTierClassifier(ActivityConfig{
		Reader: store, Writer: store, HitThreshold: 5,
	})
	for i := 0; i < 4; i++ {
		tier, err := c.RecordSearchHit(context.Background(), "r1")
		if err != nil {
			t.Fatal(err)
		}
		if tier != TierCold {
			t.Errorf("hit %d: expected cold, got %s", i, tier)
		}
	}
	got, _, _ := store.GetTier(context.Background(), "r1")
	if got != TierCold {
		t.Errorf("persisted tier flipped early: %s", got)
	}
}

func TestActivityTier_RecordSearchHit_ColdToWarm(t *testing.T) {
	store := newFakeTierStore()
	store.tiers["r1"] = TierCold
	c := NewActivityTierClassifier(ActivityConfig{
		Reader: store, Writer: store, HitThreshold: 3,
	})
	var got RepoTier
	for i := 0; i < 3; i++ {
		got, _ = c.RecordSearchHit(context.Background(), "r1")
	}
	if got != TierWarm {
		t.Errorf("expected warm after 3 hits, got %s", got)
	}
	persisted, _, _ := store.GetTier(context.Background(), "r1")
	if persisted != TierWarm {
		t.Errorf("persisted not warm: %s", persisted)
	}
}

func TestActivityTier_RecordSearchHit_WarmToHot(t *testing.T) {
	store := newFakeTierStore()
	store.tiers["r1"] = TierWarm
	c := NewActivityTierClassifier(ActivityConfig{
		Reader: store, Writer: store, HitThreshold: 2,
	})
	var got RepoTier
	for i := 0; i < 2; i++ {
		got, _ = c.RecordSearchHit(context.Background(), "r1")
	}
	if got != TierHot {
		t.Errorf("expected hot, got %s", got)
	}
}

func TestActivityTier_HotStaysHot(t *testing.T) {
	store := newFakeTierStore()
	store.tiers["r1"] = TierHot
	c := NewActivityTierClassifier(ActivityConfig{
		Reader: store, Writer: store, HitThreshold: 1,
	})
	got, _ := c.RecordSearchHit(context.Background(), "r1")
	if got != TierHot {
		t.Errorf("hot demoted to %s", got)
	}
}

func TestActivityTier_WindowExpiry(t *testing.T) {
	store := newFakeTierStore()
	store.tiers["r1"] = TierCold
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	c := NewActivityTierClassifier(ActivityConfig{
		Reader:       store,
		Writer:       store,
		HitThreshold: 5,
		HitWindow:    1 * time.Hour,
		Now:          func() time.Time { return now },
	})

	// 4 hits within window
	for i := 0; i < 4; i++ {
		c.RecordSearchHit(context.Background(), "r1")
	}
	if got, _, _ := store.GetTier(context.Background(), "r1"); got != TierCold {
		t.Errorf("early flip: %s", got)
	}

	// jump forward past window
	now = now.Add(2 * time.Hour)
	c.RecordSearchHit(context.Background(), "r1")
	if got := c.HitCount("r1"); got != 1 {
		t.Errorf("expected fresh bucket count 1, got %d", got)
	}
}

func TestActivityTier_EffectiveTier_FallbackOnReadError(t *testing.T) {
	c := NewActivityTierClassifier(ActivityConfig{
		Reader: errorReader{},
		Writer: newFakeTierStore(),
	})
	got, err := c.EffectiveTier(context.Background(), "r1", time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("expected fallback, got err %v", err)
	}
	if got != TierHot {
		t.Errorf("commit-time fallback should yield hot for recent activity, got %s", got)
	}
}

type errorReader struct{}

func (errorReader) GetTier(_ context.Context, _ string) (RepoTier, time.Time, error) {
	return "", time.Time{}, errors.New("db down")
}

func TestColdMover_SkipsWhenRepoNotCold(t *testing.T) {
	store := newFakeTierStore()
	store.tiers["r1"] = TierHot
	mover := NewColdMover(&fakeMoverSource{}, NewMemoryVectorStore(), store)
	mover.metrics = &countingMetrics{}
	moved, err := mover.MoveCold(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if moved != 0 {
		t.Errorf("expected 0 moves, got %d", moved)
	}
}

func TestColdMover_MovesEligibleRows(t *testing.T) {
	store := newFakeTierStore()
	store.tiers["r1"] = TierCold
	src := &fakeMoverSource{rows: []ColdMoverRow{
		{OrgID: "o1", RepoID: "r1", FilePath: "a.go", Symbol: "Foo", Vector: Vector{1, 2, 3}, ContentSHA: "sha1"},
		{OrgID: "o1", RepoID: "r1", FilePath: "b.go", Symbol: "Bar", Vector: Vector{4, 5, 6}, ContentSHA: "sha2"},
	}}
	cold := NewMemoryVectorStore()
	mover := NewColdMover(src, cold, store)

	moved, err := mover.MoveCold(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if moved != 2 {
		t.Errorf("expected 2, got %d", moved)
	}
	// verify both vectors landed in cold store
	v, err := cold.Get(context.Background(), KeyForSymbol("o1", "r1", "a.go", "Foo"))
	if err != nil || len(v) != 3 {
		t.Errorf("first vector missing/short: %v %v", v, err)
	}
	if len(src.markedKeys) != 2 {
		t.Errorf("expected 2 marks, got %d", len(src.markedKeys))
	}
}

type fakeMoverSource struct {
	rows       []ColdMoverRow
	markedKeys []string
}

func (f *fakeMoverSource) CountCold(_ context.Context, _ string) (int, error) {
	return len(f.rows), nil
}

func (f *fakeMoverSource) IterateCold(_ context.Context, _ string) (<-chan ColdMoverRow, <-chan error) {
	out := make(chan ColdMoverRow, len(f.rows))
	errCh := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errCh)
		for _, r := range f.rows {
			out <- r
		}
	}()
	return out, errCh
}

func (f *fakeMoverSource) MarkMoved(_ context.Context, _, _, key string) error {
	f.markedKeys = append(f.markedKeys, key)
	return nil
}

type countingMetrics struct {
	moved, skipped, failed int
}

func (m *countingMetrics) IncMoved()                      { m.moved++ }
func (m *countingMetrics) IncSkipped(reason string)       { m.skipped++ }
func (m *countingMetrics) IncFailed()                     { m.failed++ }
func (m *countingMetrics) ObserveBatch(int, time.Duration) {}
