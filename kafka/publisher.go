package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
)

// Ensure KafkaPublisher implements Publisher.
var _ Publisher = (*KafkaPublisher)(nil)

// PublisherConfig holds Kafka publisher settings.
type PublisherConfig struct {
	Brokers      []string      // Kafka broker addresses
	TopicPrefix  string        // Prefix for topic names (default: "sentiae")
	Source       string        // CloudEvent source (e.g., "identity-service")
	RequiredAcks int           // 0=none, 1=leader, -1=all (default: -1)
	Async        bool          // Async writes (faster but less durable)
	BatchSize    int           // Batch size (default: 100)
	BatchTimeout time.Duration // Batch flush interval (default: 10ms)
	MaxAttempts  int           // Max write retries (default: 3)
	WriteTimeout time.Duration // Write deadline (default: 10s)
	Logger       *slog.Logger
}

func (c *PublisherConfig) defaults() {
	if c.TopicPrefix == "" {
		c.TopicPrefix = "sentiae"
	}
	if c.BatchSize == 0 {
		c.BatchSize = 100
	}
	if c.BatchTimeout == 0 {
		c.BatchTimeout = 10 * time.Millisecond
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = 3
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = 10 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// KafkaPublisher publishes CloudEvent messages to Kafka.
type KafkaPublisher struct {
	writer *kafka.Writer
	cfg    PublisherConfig
}

// NewPublisher creates a new Kafka publisher.
func NewPublisher(cfg PublisherConfig) (*KafkaPublisher, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("kafka: at least one broker is required")
	}
	if cfg.Source == "" {
		return nil, fmt.Errorf("kafka: source is required")
	}
	cfg.defaults()

	writer := &kafka.Writer{
		Addr:         kafka.TCP(cfg.Brokers...),
		Balancer:     &kafka.LeastBytes{},
		BatchSize:    cfg.BatchSize,
		BatchTimeout: cfg.BatchTimeout,
		MaxAttempts:  cfg.MaxAttempts,
		WriteTimeout: cfg.WriteTimeout,
		Async:        cfg.Async,
		RequiredAcks: kafka.RequiredAcks(cfg.RequiredAcks),
	}

	return &KafkaPublisher{writer: writer, cfg: cfg}, nil
}

// Publish sends a single event to Kafka.
func (p *KafkaPublisher) Publish(ctx context.Context, eventType string, data EventData) error {
	if err := ValidateEventType(eventType); err != nil {
		return err
	}

	ce, payload, err := newCloudEvent(p.cfg.Source, eventType, data, "")
	if err != nil {
		return err
	}

	topic := topicFromEventType(p.cfg.TopicPrefix, eventType)

	msg := kafka.Message{
		Topic: topic,
		Key:   []byte(data.ResourceID),
		Value: payload,
		Headers: []kafka.Header{
			{Key: "ce-specversion", Value: []byte("1.0")},
			{Key: "ce-type", Value: []byte(eventType)},
			{Key: "ce-source", Value: []byte(p.cfg.Source)},
			{Key: "ce-id", Value: []byte(ce.ID)},
			{Key: "ce-idempotencykey", Value: []byte(ce.IdempotencyKey)},
			{Key: "content-type", Value: []byte("application/cloudevents+json")},
		},
	}

	if err := p.writer.WriteMessages(ctx, msg); err != nil {
		return fmt.Errorf("kafka publish: %w", err)
	}

	p.cfg.Logger.DebugContext(ctx, "event published",
		"event_type", eventType,
		"event_id", ce.ID,
		"topic", topic,
	)

	return nil
}

// PublishBatch sends multiple events in a single batch.
func (p *KafkaPublisher) PublishBatch(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}

	messages := make([]kafka.Message, 0, len(events))

	for _, e := range events {
		if err := ValidateEventType(e.Type); err != nil {
			return err
		}

		ce, payload, err := newCloudEvent(p.cfg.Source, e.Type, e.Data, e.IdempotencyKey)
		if err != nil {
			return err
		}

		topic := topicFromEventType(p.cfg.TopicPrefix, e.Type)

		messages = append(messages, kafka.Message{
			Topic: topic,
			Key:   []byte(e.Data.ResourceID),
			Value: payload,
			Headers: []kafka.Header{
				{Key: "ce-specversion", Value: []byte("1.0")},
				{Key: "ce-type", Value: []byte(e.Type)},
				{Key: "ce-source", Value: []byte(p.cfg.Source)},
				{Key: "ce-id", Value: []byte(ce.ID)},
				{Key: "ce-idempotencykey", Value: []byte(ce.IdempotencyKey)},
				{Key: "content-type", Value: []byte("application/cloudevents+json")},
			},
		})
	}

	if err := p.writer.WriteMessages(ctx, messages...); err != nil {
		return fmt.Errorf("kafka publish batch: %w", err)
	}

	p.cfg.Logger.DebugContext(ctx, "batch published", "count", len(events))

	return nil
}

// Close closes the underlying Kafka writer.
func (p *KafkaPublisher) Close() error {
	if p.writer != nil {
		return p.writer.Close()
	}
	return nil
}
