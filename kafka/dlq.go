package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
)

// DLQConfig configures per-topic dead-letter queue behavior.
type DLQConfig struct {
	// Enabled turns on DLQ routing for this consumer.
	Enabled bool

	// TopicSuffix is appended to the source topic name to form the
	// DLQ topic. Default: ".dlq".
	TopicSuffix string

	// MaxRetries is the number of processing attempts before a
	// message is routed to the DLQ. Default: 3.
	MaxRetries int
}

// DefaultDLQConfig returns a sensible default.
func DefaultDLQConfig() DLQConfig {
	return DLQConfig{
		Enabled:     true,
		TopicSuffix: ".dlq",
		MaxRetries:  3,
	}
}

// DLQProducer writes failed messages to their dead-letter topic.
// It wraps any Publisher interface so the message format is
// CloudEvents-compatible (same as the original event). The DLQ
// topic is formed by appending the suffix to the event type's
// derived topic name.
type DLQProducer struct {
	publisher Publisher
	suffix    string
}

// NewDLQProducer wraps a publisher for DLQ writes.
func NewDLQProducer(publisher Publisher, suffix string) *DLQProducer {
	if suffix == "" {
		suffix = ".dlq"
	}
	return &DLQProducer{publisher: publisher, suffix: suffix}
}

// Send routes a failed event to the DLQ topic. Error metadata
// (attempt count, last error) is embedded in the Metadata map.
func (d *DLQProducer) Send(ctx context.Context, sourceTopic string, eventType string, data EventData, attempt int, lastErr error) error {
	if d.publisher == nil {
		log.Printf("DLQ: no publisher configured, dropping event %s from %s", eventType, sourceTopic)
		return nil
	}
	if data.Metadata == nil {
		data.Metadata = make(map[string]any)
	}
	data.Metadata["dlq_source_topic"] = sourceTopic
	data.Metadata["dlq_attempt"] = fmt.Sprintf("%d", attempt)
	if lastErr != nil {
		data.Metadata["dlq_error"] = lastErr.Error()
	}
	dlqEventType := eventType + d.suffix
	return d.publisher.Publish(ctx, dlqEventType, data)
}

// AsHandler returns a DeadLetterFunc suitable for wiring into ConsumerConfig.
// When a message permanently fails, the handler decodes the CloudEvent, fans
// out the retry metadata into EventData.Metadata, and re-publishes it under
// the <eventType><suffix> topic via d.publisher.
func (d *DLQProducer) AsHandler(defaultSource string) DeadLetterFunc {
	if defaultSource == "" {
		defaultSource = "platform-kit/dlq"
	}
	return func(ctx context.Context, msg FailedMessage) {
		// Decode the CloudEvent to recover its EventData and preserve
		// upstream attribution fields (resource type, tenant, etc.).
		var event CloudEvent
		var data EventData
		if err := json.Unmarshal(msg.Value, &event); err == nil {
			_ = json.Unmarshal(event.Data, &data)
		}

		eventType := msg.EventType
		if eventType == "" {
			eventType = event.Type
		}
		if eventType == "" {
			// Fall back so we still produce a traceable DLQ topic.
			eventType = "unknown"
		}

		if err := d.Send(ctx, msg.Topic, eventType, data, msg.RetryCount, msg.LastError); err != nil {
			log.Printf("DLQ: failed to publish %s from %s (offset=%d): %v",
				eventType, msg.Topic, msg.Offset, err)
		}
	}
}

// DLQEntry is a single message read back from a DLQ topic for admin tooling.
type DLQEntry struct {
	Topic         string         `json:"topic"`
	Partition     int            `json:"partition"`
	Offset        int64          `json:"offset"`
	Time          time.Time      `json:"time"`
	EventType     string         `json:"event_type"`
	EventID       string         `json:"event_id,omitempty"`
	SourceTopic   string         `json:"source_topic,omitempty"`
	Attempts      string         `json:"attempts,omitempty"`
	LastError     string         `json:"last_error,omitempty"`
	ResourceType  string         `json:"resource_type,omitempty"`
	ResourceID    string         `json:"resource_id,omitempty"`
	Payload       any            `json:"payload,omitempty"`
	RawMetadata   map[string]any `json:"raw_metadata,omitempty"`
}

// ListDLQConfig parameterises ListDLQ.
type ListDLQConfig struct {
	Brokers    []string
	Topic      string
	Partition  int
	MaxEntries int           // cap on entries returned (default: 100)
	MaxWait    time.Duration // read deadline per message (default: 500ms)
}

// ListDLQ reads up to cfg.MaxEntries messages from a DLQ topic and returns them
// as structured entries. It does not commit offsets so admin tooling can run
// repeatedly without perturbing any consumer group. When the reader stalls
// (no new messages within MaxWait) it returns the entries read so far.
func ListDLQ(ctx context.Context, cfg ListDLQConfig) ([]DLQEntry, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("kafka: at least one broker is required")
	}
	if cfg.Topic == "" {
		return nil, fmt.Errorf("kafka: topic is required")
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 100
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = 500 * time.Millisecond
	}

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:   cfg.Brokers,
		Topic:     cfg.Topic,
		Partition: cfg.Partition,
		MinBytes:  1,
		MaxBytes:  10e6,
		MaxWait:   cfg.MaxWait,
	})
	defer reader.Close()

	entries := make([]DLQEntry, 0, cfg.MaxEntries)
	for len(entries) < cfg.MaxEntries {
		readCtx, cancel := context.WithTimeout(ctx, cfg.MaxWait)
		msg, err := reader.ReadMessage(readCtx)
		cancel()
		if err != nil {
			// Timeout or context done — return what we have.
			if ctx.Err() != nil {
				return entries, ctx.Err()
			}
			break
		}

		entry := DLQEntry{
			Topic:     msg.Topic,
			Partition: msg.Partition,
			Offset:    msg.Offset,
			Time:      msg.Time,
		}

		var event CloudEvent
		if err := json.Unmarshal(msg.Value, &event); err == nil {
			entry.EventType = event.Type
			entry.EventID = event.ID

			var data EventData
			if err := json.Unmarshal(event.Data, &data); err == nil {
				entry.ResourceType = data.ResourceType
				entry.ResourceID = data.ResourceID
				if data.Metadata != nil {
					entry.RawMetadata = data.Metadata
					if v, ok := data.Metadata["dlq_source_topic"].(string); ok {
						entry.SourceTopic = v
					}
					if v, ok := data.Metadata["dlq_attempt"].(string); ok {
						entry.Attempts = v
					}
					if v, ok := data.Metadata["dlq_error"].(string); ok {
						entry.LastError = v
					}
				}
			}
			entry.Payload = json.RawMessage(event.Data)
		} else {
			// Non-CloudEvent payload — surface raw bytes for debugging.
			entry.Payload = string(msg.Value)
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

// IsDLQTopic reports whether a topic name looks like a DLQ topic (ends with
// the standard DLQ suffix or any suffix starting with ".dlq").
func IsDLQTopic(topic string) bool {
	return strings.HasSuffix(topic, ".dlq") ||
		strings.Contains(topic, ".dlq.") ||
		strings.HasSuffix(topic, "-dlq")
}
