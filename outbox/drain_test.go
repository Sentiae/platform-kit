package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	prommetrics "github.com/prometheus/client_golang/prometheus/testutil"

	pkkafka "github.com/sentiae/platform-kit/kafka"
)

// fakeRepo is an in-memory Repo that records the drain's state transitions.
type fakeRepo struct {
	due     []Row
	fetched bool
	counts  map[Status]int

	sending []uuid.UUID
	sent    []uuid.UUID
	failed  []failCall
}

type failCall struct {
	id          uuid.UUID
	attempts    int
	nextRetryAt time.Time
	lastErr     string
	dead        bool
}

func (f *fakeRepo) Append(context.Context, Message) error { return nil }

func (f *fakeRepo) FetchDue(context.Context, int) ([]Row, error) {
	if f.fetched {
		return nil, nil
	}
	f.fetched = true
	return f.due, nil
}

func (f *fakeRepo) MarkSending(_ context.Context, id uuid.UUID) error {
	f.sending = append(f.sending, id)
	return nil
}

func (f *fakeRepo) MarkSent(_ context.Context, id uuid.UUID) error {
	f.sent = append(f.sent, id)
	return nil
}

func (f *fakeRepo) MarkFailed(_ context.Context, id uuid.UUID, attempts int, nextRetryAt time.Time, lastErr string, dead bool) error {
	f.failed = append(f.failed, failCall{id: id, attempts: attempts, nextRetryAt: nextRetryAt, lastErr: lastErr, dead: dead})
	return nil
}

func (f *fakeRepo) CountByStatus(context.Context) (map[Status]int, error) {
	return f.counts, nil
}

// fakePublisher returns a fixed error (nil = success) and records topics.
type fakePublisher struct {
	err       error
	published []string
}

func (p *fakePublisher) Publish(_ context.Context, eventType string, _ pkkafka.EventData) error {
	if p.err != nil {
		return p.err
	}
	p.published = append(p.published, eventType)
	return nil
}
func (p *fakePublisher) PublishBatch(context.Context, []pkkafka.Event) error { return nil }
func (p *fakePublisher) EnsureTopics(context.Context) error                  { return nil }
func (p *fakePublisher) Close() error                                        { return nil }

func newRow(t *testing.T, attempts, maxRetries int) Row {
	t.Helper()
	return Row{
		ID:         uuid.New(),
		Topic:      "identity.user.updated",
		Key:        "u1",
		Payload:    mustPayload(t, pkkafka.EventData{ResourceType: "user", ResourceID: "u1", Timestamp: time.Now().UTC()}),
		Status:     StatusPending,
		Attempts:   attempts,
		MaxRetries: maxRetries,
		CreatedAt:  time.Now().UTC(),
	}
}

func TestDrainPublishSuccess(t *testing.T) {
	row := newRow(t, 0, 10)
	repo := &fakeRepo{due: []Row{row}, counts: map[Status]int{StatusPending: 1}}
	pub := &fakePublisher{}
	d := NewDrainer(repo, pub, nil, DrainConfig{})

	if err := d.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(repo.sending) != 1 || repo.sending[0] != row.ID {
		t.Fatalf("expected MarkSending for %s, got %v", row.ID, repo.sending)
	}
	if len(repo.sent) != 1 || repo.sent[0] != row.ID {
		t.Fatalf("expected MarkSent for %s, got %v", row.ID, repo.sent)
	}
	if len(repo.failed) != 0 {
		t.Fatalf("expected no MarkFailed, got %v", repo.failed)
	}
	if len(pub.published) != 1 || pub.published[0] != row.Topic {
		t.Fatalf("expected publish of %s, got %v", row.Topic, pub.published)
	}
}

func TestDrainPublishFailureBelowMaxRetries(t *testing.T) {
	row := newRow(t, 0, 3) // attempts becomes 1, below max 3
	repo := &fakeRepo{due: []Row{row}, counts: map[Status]int{StatusPending: 1}}
	pub := &fakePublisher{err: errors.New("broker down")}
	d := NewDrainer(repo, pub, nil, DrainConfig{})

	before := time.Now().UTC()
	if err := d.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(repo.sent) != 0 {
		t.Fatalf("expected no MarkSent, got %v", repo.sent)
	}
	if len(repo.failed) != 1 {
		t.Fatalf("expected one MarkFailed, got %d", len(repo.failed))
	}
	fc := repo.failed[0]
	if fc.dead {
		t.Fatalf("expected dead=false below max retries")
	}
	if fc.attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", fc.attempts)
	}
	if !fc.nextRetryAt.After(before) {
		t.Fatalf("expected next_retry_at in the future, got %v (before=%v)", fc.nextRetryAt, before)
	}
}

func TestDrainPublishFailureAtMaxRetriesDeadLetters(t *testing.T) {
	deadBefore := prommetrics.ToFloat64(outboxDeadTotal)

	row := newRow(t, 2, 3) // attempts becomes 3, hits max 3 -> dead
	repo := &fakeRepo{due: []Row{row}, counts: map[Status]int{StatusPending: 1}}
	pub := &fakePublisher{err: errors.New("broker down")}
	d := NewDrainer(repo, pub, nil, DrainConfig{})

	if err := d.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(repo.failed) != 1 {
		t.Fatalf("expected one MarkFailed, got %d", len(repo.failed))
	}
	fc := repo.failed[0]
	if !fc.dead {
		t.Fatalf("expected dead=true at max retries")
	}
	if fc.attempts != 3 {
		t.Fatalf("expected attempts=3, got %d", fc.attempts)
	}

	deadAfter := prommetrics.ToFloat64(outboxDeadTotal)
	if deadAfter-deadBefore != 1 {
		t.Fatalf("expected outbox_dead_total to increment by 1, got delta %v", deadAfter-deadBefore)
	}
}

func TestDrainUsesDefaultMaxRetriesWhenRowUnset(t *testing.T) {
	row := newRow(t, 0, 0) // row MaxRetries unset -> drain default
	repo := &fakeRepo{due: []Row{row}, counts: map[Status]int{StatusPending: 1}}
	pub := &fakePublisher{err: errors.New("broker down")}
	d := NewDrainer(repo, pub, nil, DrainConfig{DefaultMaxRetries: 2})

	if err := d.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(repo.failed) != 1 || repo.failed[0].dead {
		t.Fatalf("attempt 1 of default-max 2 should retry (dead=false), got %v", repo.failed)
	}
}
