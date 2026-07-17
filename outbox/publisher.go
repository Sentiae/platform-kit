package outbox

import (
	"context"
	"encoding/json"
	"fmt"

	pkkafka "github.com/sentiae/platform-kit/kafka"
)

// OutboxPublisher is the zero-call-site-change adapter (CLAUDE.md §19). It
// implements the same kafka.Publisher interface every event call site already
// depends on, but instead of publishing to Kafka it appends a durable outbox
// row through the Writer. An adopting service swaps its publisher for this and
// its existing .Publish(...) call sites are unchanged; the drain worker then
// publishes the row, rebuilding the CloudEvent envelope so the wire bytes match
// a direct publish. Because Append routes through the adopter's tx resolver, a
// call site carrying a GORM tx on its context JOINS that transaction (§19
// atomicity).
type OutboxPublisher struct {
	writer Writer
}

// NewOutboxPublisher wires the publisher to an outbox Writer (*GormRepo
// satisfies Writer).
func NewOutboxPublisher(writer Writer) *OutboxPublisher {
	return &OutboxPublisher{writer: writer}
}

var _ pkkafka.Publisher = (*OutboxPublisher)(nil)

// Publish validates the event type (so a bad type fails loudly at the call
// site, exactly as the real publisher does), marshals the data, and appends an
// outbox row keyed by the resource id. The stored Topic holds the EVENT TYPE;
// the drain hands it to the real publisher, which derives the Kafka topic. Note
// the row-level Append additionally validates the payload schema (D-162b), so a
// schema-invalid event also fails here and rolls the caller's transaction back.
func (p *OutboxPublisher) Publish(ctx context.Context, eventType string, data pkkafka.EventData) error {
	if err := pkkafka.ValidateEventType(eventType); err != nil {
		return err
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("outbox publish: marshal event data: %w", err)
	}
	return p.writer.Append(ctx, Message{
		Topic:   eventType,
		Key:     data.ResourceID,
		Payload: payload,
	})
}

// PublishBatch appends one outbox row per event. A non-empty IdempotencyKey is
// stored in Headers so the drain preserves the caller-supplied dedup key.
func (p *OutboxPublisher) PublishBatch(ctx context.Context, events []pkkafka.Event) error {
	for _, e := range events {
		if err := pkkafka.ValidateEventType(e.Type); err != nil {
			return err
		}
		payload, err := json.Marshal(e.Data)
		if err != nil {
			return fmt.Errorf("outbox publish batch: marshal event data for %s: %w", e.Type, err)
		}
		msg := Message{
			Topic:   e.Type,
			Key:     e.Data.ResourceID,
			Payload: payload,
		}
		if e.IdempotencyKey != "" {
			msg.Headers = map[string]string{"idempotency_key": e.IdempotencyKey}
		}
		if err := p.writer.Append(ctx, msg); err != nil {
			return err
		}
	}
	return nil
}

// EnsureTopics is a no-op: the REAL publisher (used by the drain) provisions
// topics when it publishes the drained rows.
func (p *OutboxPublisher) EnsureTopics(_ context.Context) error { return nil }

// Close is a no-op: the outbox holds no Kafka resources of its own.
func (p *OutboxPublisher) Close() error { return nil }
