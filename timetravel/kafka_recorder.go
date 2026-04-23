// Kafka-based recorder + consumer.
//
// Write path:
//
//	KafkaRecorder publishes a `sentiae.timetravel.entity.changed` event
//	carrying (kind, id, payload, writer_service). Services that don't
//	own the entity_snapshots table use this to decouple their hot path
//	from snapshot persistence.
//
// Read path:
//
//	Consumer subscribes to the same event type and delegates to an
//	inner Recorder (typically a GORMRecorder backed by the ops-service
//	DB) to persist the row. This keeps the ops-service DB as the single
//	shared store without forcing every service to open a connection to
//	it.
package timetravel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	pkafka "github.com/sentiae/platform-kit/kafka"
)

// parseUUID is a small wrapper around uuid.Parse so call sites in the
// consumer don't pull in the uuid package explicitly — keeps the Kafka
// decode path readable.
func parseUUID(s string) (uuid.UUID, error) { return uuid.Parse(s) }

// EventType is the canonical Kafka event for snapshot fan-out.
const EventType = "sentiae.timetravel.entity.changed"

// kafkaPayload is the shape the event carries. Payload is a JSON blob
// (already marshaled) so the consumer re-persists it verbatim without
// a second marshal round-trip. ChangedBy/ChangeReason carry the §13.4
// permission-audit fields across the Kafka fan-out so the persisting
// consumer can stamp them on the row.
type kafkaPayload struct {
	Kind          string          `json:"kind"`
	EntityID      string          `json:"entity_id"`
	WriterService string          `json:"writer_service"`
	Payload       json.RawMessage `json:"payload"`
	ChangedBy     string          `json:"changed_by"`
	ChangeReason  string          `json:"change_reason"`
}

// KafkaRecorder publishes snapshot events so a central consumer can
// persist them. A nil publisher degrades to a silent no-op.
type KafkaRecorder struct {
	pub           pkafka.Publisher
	writerService string
	logger        *slog.Logger
}

// NewKafkaRecorder wires the publisher. writerService is recorded on
// every event so the consumer can tag the entity_snapshots row.
func NewKafkaRecorder(pub pkafka.Publisher, writerService string, logger *slog.Logger) *KafkaRecorder {
	if logger == nil {
		logger = slog.Default()
	}
	if writerService == "" {
		writerService = "unknown-service"
	}
	return &KafkaRecorder{pub: pub, writerService: writerService, logger: logger}
}

// RecordEntity publishes a timetravel.entity.changed event. Failure is
// logged + returned, never panicked; the caller is free to swallow.
func (r *KafkaRecorder) RecordEntity(ctx context.Context, kind, id string, payload any) error {
	if r == nil || r.pub == nil {
		return errors.New("kafka recorder not configured")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	cc := ChangeContextFromContext(ctx)
	meta := map[string]any{
		"kind":           kind,
		"entity_id":      id,
		"writer_service": r.writerService,
		"payload":        string(raw),
		"changed_by":     cc.ChangedBy.String(),
		"change_reason":  cc.Reason,
	}
	data := pkafka.EventData{
		ResourceType: "entity_snapshot",
		ResourceID:   kind + ":" + id,
		Metadata:     meta,
	}
	if err := r.pub.Publish(ctx, EventType, data); err != nil {
		r.logger.Warn("timetravel kafka publish failed",
			"error", err, "kind", kind, "id", id)
		return err
	}
	return nil
}

// --- Consumer ---------------------------------------------------------------

// Consumer wires a Kafka subscription to an inner Recorder. Services
// hosting the central entity_snapshots table (typically ops-service)
// run this to persist events from other services.
type Consumer struct {
	sink   Recorder
	logger *slog.Logger
}

// NewConsumer builds the consumer.
func NewConsumer(sink Recorder, logger *slog.Logger) *Consumer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Consumer{sink: sink, logger: logger}
}

// Register subscribes the consumer to the canonical event type on the
// supplied kafka.Consumer.
func (c *Consumer) Register(cons pkafka.Consumer) {
	cons.Subscribe(EventType, c.handle)
}

// handle is the EventHandler bound to EventType. It extracts the
// metadata fields emitted by KafkaRecorder and forwards to the sink.
func (c *Consumer) handle(ctx context.Context, event pkafka.CloudEvent) error {
	if c.sink == nil {
		return nil
	}
	var env struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(event.Data, &env); err != nil {
		c.logger.Warn("timetravel consumer decode failed",
			"error", err, "event_id", event.ID)
		return nil // don't fail the saga; best-effort
	}
	kind, _ := env.Metadata["kind"].(string)
	id, _ := env.Metadata["entity_id"].(string)
	payloadStr, _ := env.Metadata["payload"].(string)
	if kind == "" || id == "" {
		return nil
	}
	// Reconstruct the ChangeContext the upstream caller threaded so
	// the sink can stamp ChangedBy/ChangeReason on the persisted row.
	cc := ChangeContext{Reason: "system"}
	if rawReason, ok := env.Metadata["change_reason"].(string); ok && rawReason != "" {
		cc.Reason = rawReason
	}
	if rawBy, ok := env.Metadata["changed_by"].(string); ok && rawBy != "" {
		if parsed, perr := parseUUID(rawBy); perr == nil {
			cc.ChangedBy = parsed
		}
	}
	ctx = WithChangeContext(ctx, cc)
	// Forward as a json.RawMessage so the sink (GORMRecorder) marshals
	// it back into a jsonb column without re-stringifying.
	if err := c.sink.RecordEntity(ctx, kind, id, json.RawMessage(payloadStr)); err != nil {
		c.logger.Warn("timetravel consumer persist failed",
			"error", err, "kind", kind, "id", id)
	}
	return nil
}
