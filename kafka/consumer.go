package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/segmentio/kafka-go"
)

// Ensure KafkaConsumer implements Consumer.
var _ Consumer = (*KafkaConsumer)(nil)

// dlqSink is the write seam for the default dead-letter writer. Satisfied by
// *kafka.Writer; tests inject a fake to assert dead-letter routing without a
// live broker.
type dlqSink interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
}

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

	// MaxConsecutiveErrors is the number of consecutive FetchMessage failures
	// before the reader is closed and recreated with exponential backoff.
	// Default: 5.
	MaxConsecutiveErrors int

	// ReconnectBaseBackoff is the initial wait before recreating a failed
	// reader. Doubles on each successive failure up to ReconnectMaxBackoff.
	// Default: 2s.
	ReconnectBaseBackoff time.Duration

	// ReconnectMaxBackoff caps exponential backoff for reader recreation.
	// Default: 30s.
	ReconnectMaxBackoff time.Duration

	// AssignmentDeadline bounds how long the assignment-assertion goroutine
	// waits for the consumer group to receive at least one partition before
	// concluding the group is stable with zero partitions (the silent
	// no-fetch failure mode). Default: 60s.
	AssignmentDeadline time.Duration

	// AssignmentPollInterval is how often the assignment-assertion goroutine
	// polls the group coordinator for partition assignments. Default: 5s.
	AssignmentPollInterval time.Duration

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
	if c.MaxConsecutiveErrors == 0 {
		c.MaxConsecutiveErrors = 5
	}
	if c.ReconnectBaseBackoff == 0 {
		c.ReconnectBaseBackoff = 2 * time.Second
	}
	if c.ReconnectMaxBackoff == 0 {
		c.ReconnectMaxBackoff = 30 * time.Second
	}
	if c.AssignmentDeadline == 0 {
		c.AssignmentDeadline = 60 * time.Second
	}
	if c.AssignmentPollInterval == 0 {
		c.AssignmentPollInterval = 5 * time.Second
	}
}

// groupDescriber is the seam used to query the consumer group's current
// partition assignments. Satisfied by *kafka.Client; tests inject a fake.
type groupDescriber interface {
	DescribeGroups(ctx context.Context, req *kafka.DescribeGroupsRequest) (*kafka.DescribeGroupsResponse, error)
}

// KafkaConsumer consumes CloudEvent messages from Kafka.
type KafkaConsumer struct {
	cfg       ConsumerConfig
	handlers  map[string]EventHandler
	reader    *kafka.Reader
	describer groupDescriber
	dlqWriter dlqSink
	mu        sync.Mutex

	// assignErr holds the fatal "stable group with zero partitions" error
	// once the assignment-assertion goroutine concludes. nil until then.
	assignErr atomic.Pointer[error]

	// health tracks per-topic last-processed info for /healthz/events.
	health   map[string]*topicHealth
	healthMu sync.RWMutex
}

// topicHealth is updated on every processed message.
type topicHealth struct {
	Topic                string    `json:"topic"`
	GroupID              string    `json:"group_id"`
	LastOffset           int64     `json:"last_offset"`
	LastEventType        string    `json:"last_event_type"`
	LastProcessed        time.Time `json:"last_processed"`
	MessagesOK           int64     `json:"messages_ok"`
	MessagesFailed       int64     `json:"messages_failed"`
	MessagesDeadLettered int64     `json:"messages_dead_lettered"`
	Lag                  int64     `json:"lag"`
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
		cfg:       cfg,
		handlers:  make(map[string]EventHandler),
		health:    make(map[string]*topicHealth),
		describer: &kafka.Client{Addr: kafka.TCP(cfg.Brokers...)},
		// Default dead-letter sink: raw-bytes writer to <topic>.dlq. Ensures
		// poison messages are durably parked by construction even when no
		// cfg.DeadLetterFunc override is wired (the fleet default). Per-message
		// Topic is set at write time, so the writer's own Topic stays empty.
		dlqWriter: &kafka.Writer{
			Addr:                   kafka.TCP(cfg.Brokers...),
			Balancer:               &kafka.LeastBytes{},
			RequiredAcks:           kafka.RequireAll,
			AllowAutoTopicCreation: true,
		},
	}, nil
}

// Health returns a snapshot of per-topic processing stats. Safe for /healthz.
// Lag is recorded per-message at process time (HighWaterMark - Offset - 1);
// under a GroupTopics reader Config().Topic is empty so no reader lookup is
// possible or needed here.
func (c *KafkaConsumer) Health() []topicHealth {
	c.healthMu.RLock()
	defer c.healthMu.RUnlock()
	out := make([]topicHealth, 0, len(c.health))
	for _, h := range c.health {
		out = append(out, *h)
	}
	return out
}

// AssignmentError returns the fatal error recorded when the consumer group
// reached the assignment deadline with zero partitions assigned (messages
// will never be fetched). Returns nil when the group is healthy or the
// assertion has not yet concluded.
func (c *KafkaConsumer) AssignmentError() error {
	if p := c.assignErr.Load(); p != nil {
		return *p
	}
	return nil
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
//
// A single reader is created over all configured topics via the segmentio
// GroupTopics API — one member of the consumer group subscribing to every
// topic. This is deliberate: creating one single-topic reader per topic under
// a shared GroupID produces a Stable group where each member is assigned zero
// partitions, so messages are never fetched (observed live 2026-07-05).
//
// Errors from the underlying segmentio Reader (rebalance failures,
// coordinator disconnects, group session timeouts) are routed through
// cfg.Logger so operational issues become visible in logs; previously
// those errors were silent, which allowed a consumer to appear "started"
// indefinitely while actually not consuming anything.
//
// Start also launches an assignment-assertion goroutine that fails loud if
// the group never receives a partition assignment (see AssignmentError).
func (c *KafkaConsumer) Start(ctx context.Context) error {
	reader := c.buildReader()
	c.mu.Lock()
	c.reader = reader
	c.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				c.cfg.Logger.Error("assignment assertion goroutine panicked",
					"group", c.cfg.GroupID, "panic", r)
			}
		}()
		c.assertAssignment(ctx)
	}()

	c.consumeLoop(ctx)
	return nil
}

// buildReader creates the single kafka.Reader over all configured topics using
// the consumer's settings. Extracted so Start and recreateReader share it.
func (c *KafkaConsumer) buildReader() *kafka.Reader {
	return kafka.NewReader(kafka.ReaderConfig{
		Brokers:     c.cfg.Brokers,
		GroupID:     c.cfg.GroupID,
		GroupTopics: c.cfg.Topics,
		MinBytes:    c.cfg.MinBytes,
		MaxBytes:    c.cfg.MaxBytes,
		MaxWait:     c.cfg.MaxWait,
		StartOffset: c.cfg.StartOffset,
		// Logger surfaces group-lifecycle reader events (joins, rebalances,
		// partition assignments) at INFO and routine fetch/commit chatter at
		// DEBUG. ErrorLogger surfaces fetch/heartbeat/group errors that would
		// otherwise be swallowed — the common failure mode here is a consumer
		// evicted from the group during a rebalance with nobody logging it.
		Logger:      kafkaLoggerFromSlog(c.cfg.Logger, false),
		ErrorLogger: kafkaLoggerFromSlog(c.cfg.Logger, true),
	})
}

// recreateReader closes old, waits backoff, then builds and stores a fresh
// reader. Returns false if ctx is cancelled during the backoff wait.
func (c *KafkaConsumer) recreateReader(ctx context.Context, old *kafka.Reader, backoff time.Duration) bool {
	c.mu.Lock()
	_ = old.Close()
	c.mu.Unlock()

	select {
	case <-time.After(backoff):
	case <-ctx.Done():
		return false
	}

	fresh := c.buildReader()
	c.mu.Lock()
	c.reader = fresh
	c.mu.Unlock()

	c.cfg.Logger.Info("consumer reconnected", "group", c.cfg.GroupID)
	return true
}

// groupLifecycleMarkers are case-insensitive substrings of segmentio kafka-go
// log messages that indicate consumer-group lifecycle events (join / leave /
// rebalance / sync / partition assignment). Matched messages are promoted to
// INFO; everything else (fetch loop, offset commit chatter) stays at DEBUG.
//
// Each marker is proven to occur in kafka-go@v0.4.50 source:
//   - "joined group"                        consumergroup.go:806, :952
//   - "leaving group"                       consumergroup.go:1212
//   - "rebalancing group"                   consumergroup.go:530
//   - "sync group finished"                 consumergroup.go:1104
//   - "received empty assignments"          consumergroup.go:1099
//   - "assigned member/topic/partitions"    consumergroup.go:966
//   - "subscribed to topics and partitions" reader.go:141
var groupLifecycleMarkers = []string{
	"joined group",
	"leaving group",
	"rebalancing group",
	"sync group finished",
	"received empty assignments",
	"assigned member/topic/partitions",
	"subscribed to topics and partitions",
}

// kafkaLoggerFromSlog adapts a slog.Logger to segmentio's kafka.Logger
// interface. isError logs at Error. Non-error messages are promoted to Info
// when they match a group-lifecycle marker, else logged at Debug so routine
// fetch/commit chatter doesn't spam logs while group joins stay loud.
func kafkaLoggerFromSlog(sl *slog.Logger, isError bool) kafka.LoggerFunc {
	if sl == nil {
		sl = slog.Default()
	}
	return func(msg string, args ...any) {
		formatted := fmt.Sprintf(msg, args...)
		if isError {
			sl.Error("kafka reader", "message", formatted)
			return
		}
		lower := strings.ToLower(formatted)
		for _, m := range groupLifecycleMarkers {
			if strings.Contains(lower, m) {
				sl.Info("kafka reader", "message", formatted)
				return
			}
		}
		sl.Debug("kafka reader", "message", formatted)
	}
}

// consumeLoop runs the single fetch → handle → commit loop until ctx is
// cancelled, recreating the reader with exponential backoff after too many
// consecutive fetch failures.
func (c *KafkaConsumer) consumeLoop(ctx context.Context) {
	c.cfg.Logger.Info("consumer started", "group", c.cfg.GroupID, "topics", c.cfg.Topics)

	consecutiveErrors := 0
	backoff := c.cfg.ReconnectBaseBackoff

	for {
		c.mu.Lock()
		reader := c.reader
		c.mu.Unlock()

		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				c.cfg.Logger.Info("consumer stopping", "group", c.cfg.GroupID)
				return
			}
			consecutiveErrors++
			c.cfg.Logger.Error("fetch failed",
				"group", c.cfg.GroupID,
				"error", err,
				"consecutive_errors", consecutiveErrors,
				"max_before_reconnect", c.cfg.MaxConsecutiveErrors,
			)
			// After MaxConsecutiveErrors failures the reader is likely stuck
			// (evicted from group, coordinator unreachable, stale connection).
			// Close it, wait with exponential backoff, then recreate.
			if consecutiveErrors >= c.cfg.MaxConsecutiveErrors {
				c.cfg.Logger.Warn("too many consecutive fetch errors, recreating reader",
					"group", c.cfg.GroupID, "backoff", backoff)
				if !c.recreateReader(ctx, reader, backoff) {
					return // ctx cancelled during backoff wait
				}
				consecutiveErrors = 0
				backoff = min(backoff*2, c.cfg.ReconnectMaxBackoff)
			}
			continue
		}

		// Successful fetch — reset error counters.
		consecutiveErrors = 0
		backoff = c.cfg.ReconnectBaseBackoff

		// Commit after success OR after dead-lettering to avoid infinite retry
		// loops — every terminal branch of handleFetchedMessage returns
		// commit=true. The ONE exception is a failed DEFAULT dead-letter write
		// (broker unreachable): commit=false leaves the offset uncommitted so
		// the poison is redelivered and the DLQ write retried — no message loss.
		if commit := c.handleFetchedMessage(ctx, msg); !commit {
			c.cfg.Logger.Warn("not committing message: dead-letter write failed, will redeliver",
				"topic", msg.Topic,
				"offset", msg.Offset,
			)
			continue
		}

		if err := reader.CommitMessages(ctx, msg); err != nil {
			c.cfg.Logger.Error("commit failed",
				"topic", msg.Topic,
				"offset", msg.Offset,
				"error", err,
			)
		}
	}
}

// handleFetchedMessage decodes, dispatches, validates, retries, dead-letters
// and records health for a single fetched message. It never commits — the
// caller commits after this returns. The message's own Topic is used for
// health, DLQ, and log fields (each message carries its real topic under a
// GroupTopics reader). This is the unit-test seam: no reader required.
//
// It returns commit=true for every terminal outcome (including a successful
// dead-letter). It returns commit=false ONLY when the DEFAULT dead-letter
// write fails, so the caller leaves the offset uncommitted for redelivery.
func (c *KafkaConsumer) handleFetchedMessage(ctx context.Context, msg kafka.Message) (commit bool) {
	lag := msg.HighWaterMark - msg.Offset - 1
	if lag < 0 {
		lag = 0
	}

	var event CloudEvent
	if err := json.Unmarshal(msg.Value, &event); err != nil {
		c.cfg.Logger.Error("unmarshal failed",
			"topic", msg.Topic,
			"offset", msg.Offset,
			"error", err,
		)
		return c.sendToDeadLetter(ctx, msg, "", 0, fmt.Errorf("unmarshal: %w", err)) == nil
	}

	c.mu.Lock()
	handler, ok := c.handlers[event.Type]
	c.mu.Unlock()

	if !ok {
		c.cfg.Logger.Debug("no handler", "type", event.Type, "topic", msg.Topic)
		return true
	}

	// Schema validation: reject payloads that don't match the taxonomy.
	if !c.cfg.DisableSchemaValidation {
		if err := c.validateMessagePayload(event); err != nil {
			c.cfg.Logger.Error("schema validation failed",
				"topic", msg.Topic,
				"offset", msg.Offset,
				"event_type", event.Type,
				"error", err,
			)
			dlErr := c.sendToDeadLetter(ctx, msg, event.Type, 0, fmt.Errorf("schema_validation_failed: %w", err))
			c.recordFailure(msg.Topic, event.Type, msg.Offset, lag)
			return dlErr == nil
		}
	}

	// Retry loop — record success/failure only when terminal.
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
		dlErr := c.sendToDeadLetter(ctx, msg, event.Type, c.cfg.MaxRetries, lastErr)
		c.recordFailure(msg.Topic, event.Type, msg.Offset, lag)
		return dlErr == nil
	}

	c.recordSuccess(msg.Topic, event.Type, msg.Offset, lag)
	return true
}

// assertAssignment polls the group coordinator until the consumer group has at
// least one partition assigned, or the deadline elapses with zero — in which
// case it records a fatal error (see AssignmentError) and logs it loud. It
// short-circuits to success the moment any message has been processed. It is
// an unexported method so tests can drive it directly without a real broker.
func (c *KafkaConsumer) assertAssignment(ctx context.Context) {
	deadline := time.Now().Add(c.cfg.AssignmentDeadline)
	ticker := time.NewTicker(c.cfg.AssignmentPollInterval)
	defer ticker.Stop()

	for {
		// Short-circuit: if any message has already been recorded, the group
		// is clearly assigned and fetching — no need to describe.
		c.healthMu.RLock()
		flowing := len(c.health) > 0
		c.healthMu.RUnlock()
		if flowing {
			c.cfg.Logger.Info("kafka consumer group is fetching messages (assignment confirmed)",
				"group", c.cfg.GroupID, "topics", c.cfg.Topics)
			return
		}

		if c.describer != nil {
			n, err := c.assignedPartitionCount(ctx)
			if err != nil {
				// Broker/coordinator may be warming up; keep polling.
				c.cfg.Logger.Debug("describe consumer group failed, will retry",
					"group", c.cfg.GroupID, "error", err)
			} else if n > 0 {
				c.cfg.Logger.Info("kafka consumer group received partition assignment",
					"group", c.cfg.GroupID, "topics", c.cfg.Topics, "partitions", n)
				return
			}
		}

		if !time.Now().Before(deadline) {
			err := fmt.Errorf(
				"kafka consumer group %q is stable with ZERO partitions assigned after %s for topics %v: "+
					"messages will never be fetched — check that partitions exist and no other member is monopolizing them",
				c.cfg.GroupID, c.cfg.AssignmentDeadline, c.cfg.Topics)
			c.assignErr.Store(&err)
			c.cfg.Logger.Error("kafka consumer group received no partition assignment before deadline",
				"group", c.cfg.GroupID,
				"topics", c.cfg.Topics,
				"deadline", c.cfg.AssignmentDeadline,
				"error", err,
			)
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// assignedPartitionCount describes the consumer group and totals the partitions
// assigned across all members. The segmentio client routes DescribeGroups to
// the group coordinator automatically (protocol.GroupMessage → findcoordinator,
// transport.go:684-696), so any broker address works.
func (c *KafkaConsumer) assignedPartitionCount(ctx context.Context) (int, error) {
	resp, err := c.describer.DescribeGroups(ctx, &kafka.DescribeGroupsRequest{
		GroupIDs: []string{c.cfg.GroupID},
	})
	if err != nil {
		return 0, err
	}
	total := 0
	for _, g := range resp.Groups {
		if g.GroupID != c.cfg.GroupID {
			continue
		}
		if g.Error != nil {
			return 0, g.Error
		}
		for _, m := range g.Members {
			for _, t := range m.MemberAssignments.Topics {
				total += len(t.Partitions)
			}
		}
	}
	return total, nil
}

// sendToDeadLetter routes a poison message to its dead-letter destination.
//
// When cfg.DeadLetterFunc is set it is invoked (the override seam) and the
// message is considered dead-lettered (returns nil). Otherwise the DEFAULT
// raw-bytes writer publishes the ORIGINAL key/value/headers to <topic>.dlq,
// annotated with dlq-* provenance headers, and returns the write error. A
// non-nil error means the message was NOT durably parked, so the caller must
// not commit the offset (it will be redelivered and retried).
func (c *KafkaConsumer) sendToDeadLetter(ctx context.Context, msg kafka.Message, eventType string, retryCount int, lastErr error) error {
	if c.cfg.DeadLetterFunc != nil {
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
		c.cfg.Logger.Error("message dead-lettered via DeadLetterFunc override",
			"topic", msg.Topic,
			"offset", msg.Offset,
			"event_type", eventType,
			"error", lastErr,
		)
		c.recordDeadLetter(msg.Topic)
		return nil
	}

	if c.dlqWriter == nil {
		err := fmt.Errorf("kafka: no dead-letter sink configured")
		c.cfg.Logger.Error("cannot dead-letter message: no sink configured",
			"topic", msg.Topic,
			"offset", msg.Offset,
			"event_type", eventType,
			"error", lastErr,
		)
		return err
	}

	dlqTopic := msg.Topic + ".dlq"
	errStr := ""
	if lastErr != nil {
		errStr = lastErr.Error()
	}

	// Preserve original headers, then append dlq-* provenance headers.
	headers := make([]kafka.Header, 0, len(msg.Headers)+7)
	headers = append(headers, msg.Headers...)
	headers = append(headers,
		kafka.Header{Key: "dlq-source-topic", Value: []byte(msg.Topic)},
		kafka.Header{Key: "dlq-partition", Value: []byte(strconv.Itoa(msg.Partition))},
		kafka.Header{Key: "dlq-offset", Value: []byte(strconv.FormatInt(msg.Offset, 10))},
		kafka.Header{Key: "dlq-event-type", Value: []byte(eventType)},
		kafka.Header{Key: "dlq-attempts", Value: []byte(strconv.Itoa(retryCount))},
		kafka.Header{Key: "dlq-error", Value: []byte(errStr)},
		kafka.Header{Key: "dlq-time", Value: []byte(time.Now().UTC().Format(time.RFC3339))},
	)

	if err := c.dlqWriter.WriteMessages(ctx, kafka.Message{
		Topic:   dlqTopic,
		Key:     msg.Key,
		Value:   msg.Value,
		Headers: headers,
	}); err != nil {
		c.cfg.Logger.Error("dead-letter write failed",
			"topic", dlqTopic,
			"source_offset", msg.Offset,
			"event_type", eventType,
			"error", err,
		)
		return fmt.Errorf("write dead-letter to %s: %w", dlqTopic, err)
	}

	c.cfg.Logger.Error("message dead-lettered",
		"topic", dlqTopic,
		"source_offset", msg.Offset,
		"event_type", eventType,
		"error", lastErr,
	)
	c.recordDeadLetter(msg.Topic)
	return nil
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

func (c *KafkaConsumer) recordSuccess(topic, eventType string, offset, lag int64) {
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
	h.Lag = lag
	c.healthMu.Unlock()
}

func (c *KafkaConsumer) recordFailure(topic, eventType string, offset, lag int64) {
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
	h.Lag = lag
	c.healthMu.Unlock()
}

// recordDeadLetter increments the per-topic dead-letter counter, keyed on the
// SOURCE topic (matching recordFailure). Called only after a message is durably
// routed to its dead-letter destination.
func (c *KafkaConsumer) recordDeadLetter(topic string) {
	c.healthMu.Lock()
	h, ok := c.health[topic]
	if !ok {
		h = &topicHealth{Topic: topic, GroupID: c.cfg.GroupID}
		c.health[topic] = h
	}
	h.MessagesDeadLettered++
	c.healthMu.Unlock()
}

// Close closes the reader and the default dead-letter writer.
func (c *KafkaConsumer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var errs []error
	if w, ok := c.dlqWriter.(*kafka.Writer); ok && w != nil {
		if err := w.Close(); err != nil {
			errs = append(errs, fmt.Errorf("kafka dlq writer close error: %w", err))
		}
	}

	if c.reader != nil {
		if err := c.reader.Close(); err != nil {
			errs = append(errs, fmt.Errorf("kafka close error: %w", err))
		}
		c.reader = nil
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
