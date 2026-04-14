package kafka

import (
	"context"
	"fmt"
	"log"
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
