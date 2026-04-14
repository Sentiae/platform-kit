package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/segmentio/kafka-go"
)

// Ensure KafkaConsumer implements Consumer.
var _ Consumer = (*KafkaConsumer)(nil)

// ConsumerConfig holds Kafka consumer settings.
type ConsumerConfig struct {
	Brokers        []string       // Kafka broker addresses
	GroupID        string         // Consumer group ID
	Topics         []string       // Topics to consume from
	MinBytes       int            // Min batch size (default: 1)
	MaxBytes       int            // Max batch size (default: 10MB)
	MaxWait        time.Duration  // Max wait for batch (default: 10s)
	StartOffset    int64          // kafka.FirstOffset or kafka.LastOffset (default: LastOffset)
	MaxRetries     int            // Max handler retries before dead-letter (default: 3)
	DeadLetterFunc DeadLetterFunc // Called when a message exhausts retries (optional)
	Logger         *slog.Logger

	// DisableSchemaValidation skips the per-message taxonomy check. Default
	// false. When enabled, every message's payload is validated against the
	// registered schema before the handler is invoked; on failure the
	// message is routed to DLQ with reason="schema_validation_failed".
	DisableSchemaValidation bool
}

// DeadLetterFunc is called when a message fails after all retries.
// Implementations may publish to a dead-letter topic, log, send an alert, etc.
type DeadLetterFunc func(ctx context.Context, msg FailedMessage)

// FailedMessage contains the original message and the last error.
type FailedMessage struct {
	Topic      string
	Key        []byte
	Value      []byte
	Headers    []kafka.Header
	Offset     int64
	Partition  int
	EventType  string
	RetryCount int
	LastError  error
}

func (c *ConsumerConfig) defaults() {
	if c.MinBytes == 0 {
		c.MinBytes = 1
	}
	if c.MaxBytes == 0 {
		c.MaxBytes = 10e6 // 10MB
	}
	if c.MaxWait == 0 {
		c.MaxWait = 10 * time.Second
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = 3
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// KafkaConsumer consumes CloudEvent messages from Kafka.
type KafkaConsumer struct {
	cfg      ConsumerConfig
	handlers map[string]EventHandler
	readers  []*kafka.Reader
	mu       sync.Mutex

	// health tracks per-topic last-processed info for /healthz/events.
	health   map[string]*topicHealth
	healthMu sync.RWMutex
}

// topicHealth is updated on every processed message.
type topicHealth struct {
	Topic          string    `json:"topic"`
	GroupID        string    `json:"group_id"`
	LastOffset     int64     `json:"last_offset"`
	LastEventType  string    `json:"last_event_type"`
	LastProcessed  time.Time `json:"last_processed"`
	MessagesOK     int64     `json:"messages_ok"`
	MessagesFailed int64     `json:"messages_failed"`
	Lag            int64     `json:"lag"`
}

// NewConsumer creates a new Kafka consumer.
func NewConsumer(cfg ConsumerConfig) (*KafkaConsumer, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("kafka: at least one broker is required")
	}
	if cfg.GroupID == "" {
		return nil, fmt.Errorf("kafka: group ID is required")
	}
	if len(cfg.Topics) == 0 {
		return nil, fmt.Errorf("kafka: at least one topic is required")
	}
	cfg.defaults()

	return &KafkaConsumer{
		cfg:      cfg,
		handlers: make(map[string]EventHandler),
		health:   make(map[string]*topicHealth),
	}, nil
}

// Health returns a snapshot of per-topic processing stats. Safe for /healthz.
func (c *KafkaConsumer) Health() []topicHealth {
	c.healthMu.RLock()
	defer c.healthMu.RUnlock()
	out := make([]topicHealth, 0, len(c.health))
	for _, h := range c.health {
		// Update lag lazily from the current reader stats.
		snap := *h
		for _, r := range c.readers {
			if r.Config().Topic == h.Topic {
				snap.Lag = r.Lag()
				break
			}
		}
		out = append(out, snap)
	}
	return out
}

// SubscribedTypes returns the set of event types this consumer has handlers for.
func (c *KafkaConsumer) SubscribedTypes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.handlers))
	for t := range c.handlers {
		out = append(out, t)
	}
	return out
}

// Subscribe registers a handler for a specific event type.
func (c *KafkaConsumer) Subscribe(eventType string, handler EventHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[eventType] = handler
}

// Start begins consuming messages. It blocks until ctx is cancelled.
func (c *KafkaConsumer) Start(ctx context.Context) error {
	c.mu.Lock()
	var wg sync.WaitGroup
	for _, topic := range c.cfg.Topics {
		reader := kafka.NewReader(kafka.ReaderConfig{
			Brokers:     c.cfg.Brokers,
			GroupID:     c.cfg.GroupID,
			Topic:       topic,
			MinBytes:    c.cfg.MinBytes,
			MaxBytes:    c.cfg.MaxBytes,
			MaxWait:     c.cfg.MaxWait,
			StartOffset: c.cfg.StartOffset,
		})
		c.readers = append(c.readers, reader)

		wg.Add(1)
		go func(r *kafka.Reader, t string) {
			defer wg.Done()
			c.consumeTopic(ctx, r, t)
		}(reader, topic)
	}
	c.mu.Unlock()

	wg.Wait()
	return nil
}

func (c *KafkaConsumer) consumeTopic(ctx context.Context, reader *kafka.Reader, topic string) {
	c.cfg.Logger.Info("consumer started", "topic", topic, "group", c.cfg.GroupID)

	for {
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				c.cfg.Logger.Info("consumer stopping", "topic", topic)
				return
			}
			c.cfg.Logger.Error("fetch failed", "topic", topic, "error", err)
			continue
		}

		var event CloudEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			c.cfg.Logger.Error("unmarshal failed",
				"topic", topic,
				"offset", msg.Offset,
				"error", err,
			)
			c.sendToDeadLetter(ctx, msg, "", 0, fmt.Errorf("unmarshal: %w", err))
			_ = reader.CommitMessages(ctx, msg)
			continue
		}

		c.mu.Lock()
		handler, ok := c.handlers[event.Type]
		c.mu.Unlock()

		if !ok {
			c.cfg.Logger.Debug("no handler", "type", event.Type, "topic", topic)
			_ = reader.CommitMessages(ctx, msg)
			continue
		}

		// Schema validation: reject payloads that don't match the taxonomy.
		if !c.cfg.DisableSchemaValidation {
			if err := c.validateMessagePayload(event); err != nil {
				c.cfg.Logger.Error("schema validation failed",
					"topic", topic,
					"offset", msg.Offset,
					"event_type", event.Type,
					"error", err,
				)
				c.sendToDeadLetter(ctx, msg, event.Type, 0, fmt.Errorf("schema_validation_failed: %w", err))
				c.recordFailure(topic, event.Type, msg.Offset)
				_ = reader.CommitMessages(ctx, msg)
				continue
			}
		}

		// Retry loop — commit only on success.
		var lastErr error
		handled := false
		for attempt := 1; attempt <= c.cfg.MaxRetries; attempt++ {
			if err := handler(ctx, event); err != nil {
				lastErr = err
				c.cfg.Logger.Warn("handler failed",
					"event_type", event.Type,
					"event_id", event.ID,
					"attempt", attempt,
					"max_retries", c.cfg.MaxRetries,
					"error", err,
				)
				continue
			}
			handled = true
			break
		}

		if !handled {
			c.sendToDeadLetter(ctx, msg, event.Type, c.cfg.MaxRetries, lastErr)
			c.recordFailure(topic, event.Type, msg.Offset)
		} else {
			c.recordSuccess(topic, event.Type, msg.Offset)
		}

		// Commit after success OR after dead-lettering to avoid infinite retry loops.
		if err := reader.CommitMessages(ctx, msg); err != nil {
			c.cfg.Logger.Error("commit failed",
				"topic", topic,
				"offset", msg.Offset,
				"error", err,
			)
		}
	}
}

func (c *KafkaConsumer) sendToDeadLetter(ctx context.Context, msg kafka.Message, eventType string, retryCount int, lastErr error) {
	if c.cfg.DeadLetterFunc == nil {
		c.cfg.Logger.Error("message dead-lettered (no handler configured)",
			"topic", msg.Topic,
			"offset", msg.Offset,
			"event_type", eventType,
			"error", lastErr,
		)
		return
	}

	c.cfg.DeadLetterFunc(ctx, FailedMessage{
		Topic:      msg.Topic,
		Key:        msg.Key,
		Value:      msg.Value,
		Headers:    msg.Headers,
		Offset:     msg.Offset,
		Partition:  msg.Partition,
		EventType:  eventType,
		RetryCount: retryCount,
		LastError:  lastErr,
	})
}

// validateMessagePayload decodes the CloudEvent data and validates it
// against the registered schema for the event type.
func (c *KafkaConsumer) validateMessagePayload(event CloudEvent) error {
	var generic map[string]any
	if err := json.Unmarshal(event.Data, &generic); err != nil {
		return fmt.Errorf("data is not a JSON object: %w", err)
	}
	return ValidateRawPayload(event.Type, generic)
}

func (c *KafkaConsumer) recordSuccess(topic, eventType string, offset int64) {
	c.healthMu.Lock()
	h, ok := c.health[topic]
	if !ok {
		h = &topicHealth{Topic: topic, GroupID: c.cfg.GroupID}
		c.health[topic] = h
	}
	h.LastOffset = offset
	h.LastEventType = eventType
	h.LastProcessed = time.Now().UTC()
	h.MessagesOK++
	c.healthMu.Unlock()
}

func (c *KafkaConsumer) recordFailure(topic, eventType string, offset int64) {
	c.healthMu.Lock()
	h, ok := c.health[topic]
	if !ok {
		h = &topicHealth{Topic: topic, GroupID: c.cfg.GroupID}
		c.health[topic] = h
	}
	h.LastOffset = offset
	h.LastEventType = eventType
	h.LastProcessed = time.Now().UTC()
	h.MessagesFailed++
	c.healthMu.Unlock()
}

// Close closes all readers.
func (c *KafkaConsumer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var errs []error
	for _, reader := range c.readers {
		if err := reader.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	c.readers = nil

	if len(errs) > 0 {
		return fmt.Errorf("kafka close errors: %v", errs)
	}
	return nil
}
