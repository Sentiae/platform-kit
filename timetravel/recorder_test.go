package timetravel

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	pkafka "github.com/sentiae/platform-kit/kafka"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// cloudEventStub builds a minimal CloudEvent with the given Data bytes
// so consumer tests don't need a live Kafka.
func cloudEventStub(data []byte) pkafka.CloudEvent {
	return pkafka.CloudEvent{
		SpecVersion:     "1.0",
		ID:              "test-event-id",
		Source:          "test",
		Type:            EventType,
		Time:            time.Now().UTC().Format(time.RFC3339),
		DataContentType: "application/json",
		Data:            data,
	}
}

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func TestNoopRecorder(t *testing.T) {
	var r Recorder = NoopRecorder{}
	if err := r.RecordEntity(context.Background(), "feature", "abc", map[string]any{"x": 1}); err != nil {
		t.Fatalf("noop should not error: %v", err)
	}
}

func TestGORMRecorderInsertClosesPrevious(t *testing.T) {
	db := newTestDB(t)
	rec := NewGORMRecorder(db, "work-service", nil)

	ctx := context.Background()
	if err := rec.RecordEntity(ctx, "feature", "feat-1", map[string]any{"name": "Onboarding"}); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if err := rec.RecordEntity(ctx, "feature", "feat-1", map[string]any{"name": "Onboarding v2"}); err != nil {
		t.Fatalf("second record: %v", err)
	}

	var rows []EntitySnapshot
	if err := db.Order("valid_from ASC").Find(&rows).Error; err != nil {
		t.Fatalf("list rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].ValidTo == nil {
		t.Fatalf("expected first row's valid_to to be closed")
	}
	if rows[1].ValidTo != nil {
		t.Fatalf("expected second row's valid_to to be open, got %v", rows[1].ValidTo)
	}
	if rows[0].WriterService != "work-service" {
		t.Fatalf("writer service mismatch: %s", rows[0].WriterService)
	}
	if rows[1].Kind != "feature" || rows[1].EntityID != "feat-1" {
		t.Fatalf("wrong kind/id on second row: %+v", rows[1])
	}
	// Payload should be valid JSON that round-trips.
	var v map[string]any
	if err := json.Unmarshal(rows[1].Payload, &v); err != nil {
		t.Fatalf("payload not valid json: %v", err)
	}
	if v["name"] != "Onboarding v2" {
		t.Fatalf("payload content mismatch: %v", v)
	}
}

func TestGORMRecorderRejectsEmptyInputs(t *testing.T) {
	db := newTestDB(t)
	rec := NewGORMRecorder(db, "x", nil)
	if err := rec.RecordEntity(context.Background(), "", "id", 1); err == nil {
		t.Fatalf("expected error for empty kind")
	}
	if err := rec.RecordEntity(context.Background(), "kind", "", 1); err == nil {
		t.Fatalf("expected error for empty id")
	}
}

func TestGORMRecorderStampsTimes(t *testing.T) {
	db := newTestDB(t)
	rec := NewGORMRecorder(db, "ops-service", nil)
	before := time.Now().Add(-time.Second)
	if err := rec.RecordEntity(context.Background(), "deployment", "dep-1", map[string]any{"status": "pending"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	after := time.Now().Add(time.Second)
	var row EntitySnapshot
	if err := db.First(&row).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if row.ValidFrom.Before(before) || row.ValidFrom.After(after) {
		t.Fatalf("valid_from out of bounds: %v", row.ValidFrom)
	}
	if row.CreatedAt.Before(before) || row.CreatedAt.After(after) {
		t.Fatalf("created_at out of bounds: %v", row.CreatedAt)
	}
}

// TestConsumerHandleRoutesToSink validates the Kafka consumer's event
// decoding path without needing a live Kafka. We construct a synthetic
// CloudEvent and confirm the inner Recorder sees the decoded fields.
func TestConsumerHandleRoutesToSink(t *testing.T) {
	spy := &captureRecorder{}
	c := NewConsumer(spy, nil)

	// Simulate what KafkaRecorder.Publish would emit: the EventData
	// envelope has Metadata with kind/entity_id/payload. The platform
	// kafka publisher wraps this envelope in a CloudEvent whose Data
	// is the JSON of the envelope — we replicate that here.
	envelope := map[string]any{
		"metadata": map[string]any{
			"kind":           "feature",
			"entity_id":      "f-1",
			"writer_service": "work-service",
			"payload":        `{"name":"X"}`,
		},
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if err := c.handle(context.Background(), cloudEventStub(raw)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if spy.kind != "feature" || spy.id != "f-1" {
		t.Fatalf("consumer did not forward kind/id: %+v", spy)
	}
	if string(spy.payloadBytes) != `{"name":"X"}` {
		t.Fatalf("consumer did not forward payload verbatim: %s", string(spy.payloadBytes))
	}
}

// captureRecorder is a test spy that records the last RecordEntity
// call's arguments so assertions can inspect them.
type captureRecorder struct {
	kind         string
	id           string
	payloadBytes []byte
}

func (r *captureRecorder) RecordEntity(_ context.Context, kind, id string, payload any) error {
	r.kind = kind
	r.id = id
	if raw, ok := payload.(json.RawMessage); ok {
		r.payloadBytes = raw
	} else {
		b, _ := json.Marshal(payload)
		r.payloadBytes = b
	}
	return nil
}
