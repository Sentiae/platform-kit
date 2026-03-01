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
	Brokers        []string      // Kafka broker addresses
	GroupID        string        // Consumer group ID
	Topics         []string      // Topics to consume from
	MinBytes       int           // Min batch size (default: 1)
	MaxBytes       int           // Max batch size (default: 10MB)
	MaxWait        time.Duration // Max wait for batch (default: 10s)
	StartOffset    int64         // kafka.FirstOffset or kafka.LastOffset (default: LastOffset)
	MaxRetries     int           // Max handler retries before dead-letter (default: 3)
	DeadLetterFunc DeadLetterFunc // Called when a message exhausts retries (optional)
	Logger         *slog.Logger
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
	}, nil
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
