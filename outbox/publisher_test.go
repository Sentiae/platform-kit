package outbox

import (
	"context"
	"testing"
	"time"

	pkkafka "github.com/sentiae/platform-kit/kafka"
)

// captureWriter records the last appended message so the publisher adapter's
// mapping can be asserted in isolation from the taxonomy validation.
type captureWriter struct {
	last Message
	n    int
}

func (w *captureWriter) Append(_ context.Context, msg Message) error {
	w.last = msg
	w.n++
	return nil
}

func TestOutboxPublisherMapsEventToOutboxRow(t *testing.T) {
	w := &captureWriter{}
	p := NewOutboxPublisher(w)

	data := pkkafka.EventData{ResourceType: "user", ResourceID: "u42", Timestamp: time.Now().UTC()}
	if err := p.Publish(context.Background(), "identity.user.updated", data); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if w.n != 1 {
		t.Fatalf("expected one Append, got %d", w.n)
	}
	if w.last.Topic != "identity.user.updated" {
		t.Fatalf("Topic = %q, want the event type", w.last.Topic)
	}
	if w.last.Key != "u42" {
		t.Fatalf("Key = %q, want the resource id", w.last.Key)
	}
	if len(w.last.Payload) == 0 {
		t.Fatalf("expected a marshalled payload")
	}
}

func TestOutboxPublisherRejectsBadEventTypeAtCallSite(t *testing.T) {
	w := &captureWriter{}
	p := NewOutboxPublisher(w)

	if err := p.Publish(context.Background(), "notanevent", pkkafka.EventData{}); err == nil {
		t.Fatalf("expected error for malformed event type")
	}
	if w.n != 0 {
		t.Fatalf("bad type must not append a row, got %d appends", w.n)
	}
}

func TestOutboxPublisherBatchPreservesIdempotencyKey(t *testing.T) {
	w := &captureWriter{}
	p := NewOutboxPublisher(w)

	events := []pkkafka.Event{
		{
			Type:           "identity.user.updated",
			Data:           pkkafka.EventData{ResourceType: "user", ResourceID: "u1", Timestamp: time.Now().UTC()},
			IdempotencyKey: "dedup-1",
		},
	}
	if err := p.PublishBatch(context.Background(), events); err != nil {
		t.Fatalf("PublishBatch: %v", err)
	}
	if got := w.last.Headers["idempotency_key"]; got != "dedup-1" {
		t.Fatalf("idempotency_key header = %q, want dedup-1", got)
	}
}
