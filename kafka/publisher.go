package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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

	// DisableSchemaValidation turns off the event-taxonomy validation on
	// Publish / PublishBatch. Default false. Set to true ONLY for tests or
	// narrow migration contexts where a service is temporarily emitting
	// ad-hoc event types — production must keep validation on so the
	// registry remains the source of truth.
	DisableSchemaValidation bool

	// NumPartitions and ReplicationFactor configure topics created by
	// EnsureTopics. Defaults: 3 partitions, 1 replica (matches dev
	// single-broker cluster). Production deployments override.
	NumPartitions     int
	ReplicationFactor int
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
	// RequiredAcks 0 means "no acknowledgment" in kafka-go, which silently
	// drops durability. Default to -1 (all in-sync replicas) unless the
	// caller explicitly set a non-zero value. Callers wanting no-ack mode
	// must set RequiredAcks to an explicit positive sentinel and handle
	// mapping themselves, or accept this safe default.
	if c.RequiredAcks == 0 {
		c.RequiredAcks = -1
	}
	if c.NumPartitions == 0 {
		c.NumPartitions = 3
	}
	if c.ReplicationFactor == 0 {
		c.ReplicationFactor = 1
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
		Addr:                   kafka.TCP(cfg.Brokers...),
		Balancer:               &kafka.LeastBytes{},
		BatchSize:              cfg.BatchSize,
		BatchTimeout:           cfg.BatchTimeout,
		MaxAttempts:            cfg.MaxAttempts,
		WriteTimeout:           cfg.WriteTimeout,
		Async:                  cfg.Async,
		RequiredAcks:           kafka.RequiredAcks(cfg.RequiredAcks),
		AllowAutoTopicCreation: true,
	}

	return &KafkaPublisher{writer: writer, cfg: cfg}, nil
}

// Publish sends a single event to Kafka.
func (p *KafkaPublisher) Publish(ctx context.Context, eventType string, data EventData) error {
	if err := ValidateEventType(eventType); err != nil {
		return err
	}
	if !p.cfg.DisableSchemaValidation {
		if err := ValidateEventPayload(eventType, data); err != nil {
			return fmt.Errorf("publish rejected: %w", err)
		}
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
		if !p.cfg.DisableSchemaValidation {
			if err := ValidateEventPayload(e.Type, e.Data); err != nil {
				return fmt.Errorf("publish batch rejected for %s: %w", e.Type, err)
			}
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

// EnsureTopics pre-creates every topic derived from the registered event
// taxonomy, so producers don't race the broker's auto-create on first
// publish and consumers can attach to a known topic set on boot.
//
// Idempotent: TopicAlreadyExists is treated as success. Errors from any
// other broker response are aggregated and returned.
func (p *KafkaPublisher) EnsureTopics(ctx context.Context) error {
	topics := KnownTopics(p.cfg.TopicPrefix)
	return p.ensureTopics(ctx, topics)
}

// EnsureTopicsList lets callers explicitly supply a list (useful for
// tests or for services that publish ad-hoc topics outside the
// registry). Production code should prefer EnsureTopics.
func (p *KafkaPublisher) EnsureTopicsList(ctx context.Context, topics []string) error {
	return p.ensureTopics(ctx, topics)
}

func (p *KafkaPublisher) ensureTopics(ctx context.Context, topics []string) error {
	if len(topics) == 0 {
		return nil
	}
	if len(p.cfg.Brokers) == 0 {
		return fmt.Errorf("kafka: no brokers configured")
	}
	client := &kafka.Client{Addr: kafka.TCP(p.cfg.Brokers...), Timeout: p.cfg.WriteTimeout}

	configs := make([]kafka.TopicConfig, 0, len(topics))
	for _, t := range topics {
		configs = append(configs, kafka.TopicConfig{
			Topic:             t,
			NumPartitions:     p.cfg.NumPartitions,
			ReplicationFactor: p.cfg.ReplicationFactor,
		})
	}

	resp, err := client.CreateTopics(ctx, &kafka.CreateTopicsRequest{Topics: configs})
	if err != nil {
		return fmt.Errorf("kafka: create topics: %w", err)
	}

	var firstErr error
	created := 0
	exists := 0
	for topic, topicErr := range resp.Errors {
		if topicErr == nil {
			created++
			continue
		}
		// kafka-go returns the broker error directly; TopicAlreadyExists
		// is the only one we treat as success.
		if isTopicAlreadyExists(topicErr) {
			exists++
			continue
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("kafka: ensure topic %q: %w", topic, topicErr)
		}
	}

	p.cfg.Logger.InfoContext(ctx, "ensured kafka topics",
		"requested", len(topics),
		"created", created,
		"already_exists", exists,
		"errors", len(resp.Errors)-created-exists,
	)

	return firstErr
}

func isTopicAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	if kerr, ok := err.(kafka.Error); ok {
		return kerr == kafka.TopicAlreadyExists
	}
	// kafka-go can wrap broker errors in unexported types; fall back to
	// matching on the standard message broker fragment so the
	// idempotency contract holds regardless of wrapper shape.
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}
