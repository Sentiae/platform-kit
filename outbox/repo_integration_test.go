//go:build integration

package outbox

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	pkkafka "github.com/sentiae/platform-kit/kafka"
	"github.com/sentiae/platform-kit/testutil"
)

// newIntegrationRepo spins up a real Postgres, creates the outbox_messages
// table, and returns a GormRepo bound to it.
func newIntegrationRepo(t *testing.T) (*GormRepo, *gorm.DB) {
	t.Helper()
	db := testutil.NewTestDB(t, "")
	if err := db.AutoMigrate(&outboxModel{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return NewRepo(db, nil), db
}

func TestAppendValidEventPersistsPendingRow(t *testing.T) {
	repo, db := newIntegrationRepo(t)
	ctx := context.Background()

	msg := Message{
		Topic: "identity.user.updated",
		Key:   "u1",
		Payload: mustPayload(t, pkkafka.EventData{
			ResourceType: "user",
			ResourceID:   "u1",
			Timestamp:    time.Now().UTC(),
		}),
	}
	if err := repo.Append(ctx, msg); err != nil {
		t.Fatalf("Append: %v", err)
	}

	var rows []outboxModel
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Status != string(StatusPending) {
		t.Fatalf("status = %q, want pending", rows[0].Status)
	}
	if rows[0].Attempts != 0 {
		t.Fatalf("attempts = %d, want 0", rows[0].Attempts)
	}
	if rows[0].MaxRetries != defaultMaxRetries {
		t.Fatalf("max_retries = %d, want %d", rows[0].MaxRetries, defaultMaxRetries)
	}
	if rows[0].NextRetryAt != nil {
		t.Fatalf("next_retry_at = %v, want nil", rows[0].NextRetryAt)
	}
}

func TestAppendUnregisteredEventDoesNotPersist(t *testing.T) {
	repo, db := newIntegrationRepo(t)
	ctx := context.Background()

	msg := Message{
		Topic:   "foo.bar.baz",
		Key:     "u1",
		Payload: mustPayload(t, pkkafka.EventData{ResourceType: "user", ResourceID: "u1", Timestamp: time.Now().UTC()}),
	}
	if err := repo.Append(ctx, msg); err == nil {
		t.Fatalf("expected Append to reject an unregistered event type")
	}

	var count int64
	if err := db.Model(&outboxModel{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no persisted rows, got %d", count)
	}
}

func TestFetchDueAndStatusTransitions(t *testing.T) {
	repo, db := newIntegrationRepo(t)
	ctx := context.Background()

	appendValid(t, repo, ctx)

	due, err := repo.FetchDue(ctx, 10)
	if err != nil {
		t.Fatalf("FetchDue: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 due row, got %d", len(due))
	}
	id := due[0].ID

	if err := repo.MarkSending(ctx, id); err != nil {
		t.Fatalf("MarkSending: %v", err)
	}
	if got := statusOf(t, db, id); got != string(StatusSending) {
		t.Fatalf("status after MarkSending = %q, want sending", got)
	}

	if err := repo.MarkSent(ctx, id); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	var m outboxModel
	if err := db.First(&m, "id = ?", id).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if m.Status != string(StatusSent) {
		t.Fatalf("status after MarkSent = %q, want sent", m.Status)
	}
	if m.SentAt == nil {
		t.Fatalf("expected sent_at stamped")
	}
}

func TestMarkFailedRetryThenDead(t *testing.T) {
	repo, db := newIntegrationRepo(t)
	ctx := context.Background()
	appendValid(t, repo, ctx)

	due, err := repo.FetchDue(ctx, 10)
	if err != nil {
		t.Fatalf("FetchDue: %v", err)
	}
	id := due[0].ID

	next := time.Now().UTC().Add(time.Minute)
	if err := repo.MarkFailed(ctx, id, 1, next, "broker down", false); err != nil {
		t.Fatalf("MarkFailed retry: %v", err)
	}
	var m outboxModel
	if err := db.First(&m, "id = ?", id).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if m.Status != string(StatusPending) {
		t.Fatalf("status after retry MarkFailed = %q, want pending", m.Status)
	}
	if m.Attempts != 1 || m.NextRetryAt == nil {
		t.Fatalf("expected attempts=1 and next_retry_at set, got attempts=%d next=%v", m.Attempts, m.NextRetryAt)
	}

	// A row still inside its backoff window is NOT due.
	due, err = repo.FetchDue(ctx, 10)
	if err != nil {
		t.Fatalf("FetchDue (backoff): %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("expected 0 due rows during backoff, got %d", len(due))
	}

	if err := repo.MarkFailed(ctx, id, 10, time.Time{}, "broker down", true); err != nil {
		t.Fatalf("MarkFailed dead: %v", err)
	}
	if got := statusOf(t, db, id); got != string(StatusDead) {
		t.Fatalf("status after dead MarkFailed = %q, want dead", got)
	}
}

func TestCountByStatus(t *testing.T) {
	repo, _ := newIntegrationRepo(t)
	ctx := context.Background()
	appendValid(t, repo, ctx)
	appendValid(t, repo, ctx)

	counts, err := repo.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[StatusPending] != 2 {
		t.Fatalf("pending count = %d, want 2", counts[StatusPending])
	}
}

func appendValid(t *testing.T, repo *GormRepo, ctx context.Context) {
	t.Helper()
	msg := Message{
		Topic: "identity.user.updated",
		Key:   "u1",
		Payload: mustPayload(t, pkkafka.EventData{
			ResourceType: "user",
			ResourceID:   "u1",
			Timestamp:    time.Now().UTC(),
		}),
	}
	if err := repo.Append(ctx, msg); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

func statusOf(t *testing.T, db *gorm.DB, id uuid.UUID) string {
	t.Helper()
	var m outboxModel
	if err := db.First(&m, "id = ?", id).Error; err != nil {
		t.Fatalf("reload %s: %v", id, err)
	}
	return m.Status
}
