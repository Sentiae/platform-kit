package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// fakeDLQSink is a dlqSink test double: it records every written message and
// can be primed to return an error to exercise the not-committed path.
type fakeDLQSink struct {
	mu       sync.Mutex
	messages []kafkago.Message
	err      error
}

func (f *fakeDLQSink) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.messages = append(f.messages, msgs...)
	return nil
}

func (f *fakeDLQSink) written() []kafkago.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]kafkago.Message, len(f.messages))
	copy(out, f.messages)
	return out
}

func headerValue(msg kafkago.Message, key string) (string, bool) {
	for _, h := range msg.Headers {
		if h.Key == key {
			return string(h.Value), true
		}
	}
	return "", false
}

// TestHandleFetchedMessage_DefaultDLQ exercises the default (no DeadLetterFunc)
// dead-letter writer: poison classes are routed to <topic>.dlq via the sink,
// commit is gated on the write result, and the override seam still wins.
func TestHandleFetchedMessage_DefaultDLQ(t *testing.T) {
	if err := RegisterExtensionEvent(RegisteredEvent{
		Type:        "test.defaultdlq.schema",
		Domain:      "test",
		Description: "default dlq schema test",
		Owner:       "platform-kit",
		Schema: `{
			"type": "object",
			"required": ["user_id"],
			"properties": {"user_id": {"type": "string"}}
		}`,
	}); err != nil {
		t.Fatalf("register schema: %v", err)
	}

	tests := []struct {
		name          string
		rawGarbage    bool   // send non-JSON bytes → unmarshal failure
		eventType     string // handler registration key + CloudEvent type
		data          string // CloudEvent data payload
		handlerErr    error  // handler return (nil = success)
		disableSchema bool
		useOverride   bool // wire cfg.DeadLetterFunc (override seam)
		sinkErr       error

		wantCommit       bool
		wantSinkWrites   int
		wantOverride     bool
		wantHandlerCalls int32
	}{
		{
			name:             "handler exhausts retries → default DLQ, committed",
			eventType:        "type.fail",
			data:             `{}`,
			handlerErr:       errTest,
			disableSchema:    true,
			wantCommit:       true,
			wantSinkWrites:   1,
			wantHandlerCalls: 3, // MaxRetries
		},
		{
			name:             "schema-invalid payload → default DLQ, handler never called",
			eventType:        "test.defaultdlq.schema",
			data:             `{"not_user_id":"oops"}`,
			disableSchema:    false,
			wantCommit:       true,
			wantSinkWrites:   1,
			wantHandlerCalls: 0,
		},
		{
			name:           "unmarshal garbage → default DLQ, committed",
			rawGarbage:     true,
			disableSchema:  true,
			wantCommit:     true,
			wantSinkWrites: 1,
		},
		{
			name:             "sink write error → not committed",
			eventType:        "type.fail",
			data:             `{}`,
			handlerErr:       errTest,
			disableSchema:    true,
			sinkErr:          errors.New("broker down"),
			wantCommit:       false,
			wantSinkWrites:   0, // fake records nothing when erroring
			wantHandlerCalls: 3,
		},
		{
			name:             "DeadLetterFunc override wins → default sink unused",
			eventType:        "type.fail",
			data:             `{}`,
			handlerErr:       errTest,
			disableSchema:    true,
			useOverride:      true,
			wantCommit:       true,
			wantSinkWrites:   0,
			wantOverride:     true,
			wantHandlerCalls: 3,
		},
		{
			name:             "success path → no dead-letter, committed",
			eventType:        "type.ok",
			data:             `{}`,
			handlerErr:       nil,
			disableSchema:    true,
			wantCommit:       true,
			wantSinkWrites:   0,
			wantHandlerCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &fakeDLQSink{err: tt.sinkErr}

			var handlerCalls int32
			var overrideCalls int32

			c := &KafkaConsumer{
				cfg: ConsumerConfig{
					Logger:                  noopLogger(),
					GroupID:                 "test-group",
					MaxRetries:              3,
					DisableSchemaValidation: tt.disableSchema,
				},
				handlers:  make(map[string]EventHandler),
				health:    make(map[string]*topicHealth),
				dlqWriter: sink,
			}
			if tt.useOverride {
				c.cfg.DeadLetterFunc = func(_ context.Context, _ FailedMessage) {
					atomic.AddInt32(&overrideCalls, 1)
				}
			}
			if tt.eventType != "" {
				c.handlers[tt.eventType] = func(_ context.Context, _ CloudEvent) error {
					atomic.AddInt32(&handlerCalls, 1)
					return tt.handlerErr
				}
			}

			var msg kafkago.Message
			if tt.rawGarbage {
				msg = kafkago.Message{
					Topic:         "topic1",
					Offset:        7,
					HighWaterMark: 8,
					Key:           []byte("k"),
					Value:         []byte("}{not json"),
				}
			} else {
				msg = ceMsg(t, "topic1", tt.eventType, 2, 5, tt.data)
			}

			commit := c.handleFetchedMessage(context.Background(), msg)

			if commit != tt.wantCommit {
				t.Errorf("commit = %v, want %v", commit, tt.wantCommit)
			}
			if got := atomic.LoadInt32(&handlerCalls); got != tt.wantHandlerCalls {
				t.Errorf("handler calls = %d, want %d", got, tt.wantHandlerCalls)
			}
			if got := atomic.LoadInt32(&overrideCalls); tt.wantOverride && got != 1 {
				t.Errorf("override calls = %d, want 1", got)
			} else if !tt.wantOverride && got != 0 {
				t.Errorf("override calls = %d, want 0", got)
			}

			written := sink.written()
			if len(written) != tt.wantSinkWrites {
				t.Fatalf("sink writes = %d, want %d", len(written), tt.wantSinkWrites)
			}

			if tt.wantSinkWrites == 1 {
				dl := written[0]
				if dl.Topic != "topic1.dlq" {
					t.Errorf("dlq topic = %q, want topic1.dlq", dl.Topic)
				}
				if string(dl.Value) != string(msg.Value) {
					t.Errorf("dlq value = %q, want original %q", dl.Value, msg.Value)
				}
				if _, ok := headerValue(dl, "dlq-error"); !ok {
					t.Errorf("dlq-error header missing: %+v", dl.Headers)
				}
				if src, _ := headerValue(dl, "dlq-source-topic"); src != "topic1" {
					t.Errorf("dlq-source-topic = %q, want topic1", src)
				}
			}
		})
	}
}

// flakyDLQSink is a dlqSink test double that fails its first failFirst writes
// with failErr, then records every subsequent write. It models the FIRST-poison
// topic-creation race: the initial write to a fresh <topic>.dlq returns
// UnknownTopicOrPartition until broker-side auto-creation completes.
type flakyDLQSink struct {
	mu        sync.Mutex
	failFirst int
	failErr   error
	attempts  int
	messages  []kafkago.Message
}

func (f *flakyDLQSink) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.attempts <= f.failFirst {
		return f.failErr
	}
	f.messages = append(f.messages, msgs...)
	return nil
}

func (f *flakyDLQSink) stats() (attempts int, written []kafkago.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]kafkago.Message, len(f.messages))
	copy(out, f.messages)
	return f.attempts, out
}

// TestSendToDeadLetter_TopicCreateRace proves #dlq-first-poison-topic-race:
// when the first write to a not-yet-created <topic>.dlq fails with
// UnknownTopicOrPartition (broker auto-creation racing the write), the default
// writer retries in-session so the poison parks immediately and the offset
// commits — instead of sitting unparked until the next rebalance/restart. It
// also proves the fail-closed property is preserved: a genuinely different
// produce error is NOT retried and still withholds the commit, and exhausting
// the retry budget still withholds the commit rather than dropping the poison.
func TestSendToDeadLetter_TopicCreateRace(t *testing.T) {
	tests := []struct {
		name         string
		failFirst    int
		failErr      error
		retries      int // DeadLetterCreateRetries
		wantCommit   bool
		wantAttempts int
		wantParked   bool
	}{
		{
			name:         "first write races topic-create, retry parks in-session",
			failFirst:    1,
			failErr:      kafkago.UnknownTopicOrPartition,
			retries:      5,
			wantCommit:   true,
			wantAttempts: 2, // 1 race + 1 success
			wantParked:   true,
		},
		{
			name:         "several create-races then success, still parks in-session",
			failFirst:    3,
			failErr:      kafkago.UnknownTopicOrPartition,
			retries:      5,
			wantCommit:   true,
			wantAttempts: 4, // 3 races + 1 success
			wantParked:   true,
		},
		{
			name:         "topic-create race via wrapped error string is retried",
			failFirst:    1,
			failErr:      errors.New("kafka.(*Writer).WriteMessages: Unknown Topic Or Partition"),
			retries:      5,
			wantCommit:   true,
			wantAttempts: 2,
			wantParked:   true,
		},
		{
			name:         "genuine broker error is NOT retried and withholds commit (fail-closed)",
			failFirst:    99,
			failErr:      errors.New("broker down"),
			retries:      5,
			wantCommit:   false,
			wantAttempts: 1, // no retry on a non-topic error
			wantParked:   false,
		},
		{
			name:         "create race never clears within budget → withholds commit (no drop)",
			failFirst:    99,
			failErr:      kafkago.UnknownTopicOrPartition,
			retries:      2,
			wantCommit:   false,
			wantAttempts: 3, // retries + 1, all failed
			wantParked:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &flakyDLQSink{failFirst: tt.failFirst, failErr: tt.failErr}

			c := &KafkaConsumer{
				cfg: ConsumerConfig{
					Logger:                     noopLogger(),
					GroupID:                    "test-group",
					MaxRetries:                 3,
					DisableSchemaValidation:    true,
					DeadLetterCreateRetries:    tt.retries,
					DeadLetterCreateBackoff:    time.Millisecond, // keep the test fast
					DeadLetterCreateMaxBackoff: time.Millisecond,
				},
				handlers:  make(map[string]EventHandler),
				health:    make(map[string]*topicHealth),
				dlqWriter: sink,
			}
			// Handler always fails so the message exhausts retries and is
			// dead-lettered through the default writer path under test.
			c.handlers["type.fail"] = func(_ context.Context, _ CloudEvent) error {
				return errTest
			}

			msg := ceMsg(t, "topic1", "type.fail", 2, 5, `{}`)
			commit := c.handleFetchedMessage(context.Background(), msg)

			if commit != tt.wantCommit {
				t.Errorf("commit = %v, want %v", commit, tt.wantCommit)
			}

			attempts, written := sink.stats()
			if attempts != tt.wantAttempts {
				t.Errorf("sink write attempts = %d, want %d", attempts, tt.wantAttempts)
			}

			if tt.wantParked {
				if len(written) != 1 {
					t.Fatalf("parked messages = %d, want 1 (poison must park in-session)", len(written))
				}
				dl := written[0]
				if dl.Topic != "topic1.dlq" {
					t.Errorf("dlq topic = %q, want topic1.dlq", dl.Topic)
				}
				if string(dl.Value) != string(msg.Value) {
					t.Errorf("dlq value = %q, want original %q", dl.Value, msg.Value)
				}
				if src, _ := headerValue(dl, "dlq-source-topic"); src != "topic1" {
					t.Errorf("dlq-source-topic = %q, want topic1", src)
				}
			} else if len(written) != 0 {
				t.Errorf("parked messages = %d, want 0 (nothing must be recorded on a withheld commit)", len(written))
			}
		})
	}
}

// TestDeadLetterWriteWithRetry_ContextCancel proves the retry loop honors ctx
// cancellation during its backoff wait instead of spinning — it returns the
// context error, which withholds the commit (fail-closed) rather than dropping.
func TestDeadLetterWriteWithRetry_ContextCancel(t *testing.T) {
	sink := &flakyDLQSink{failFirst: 99, failErr: kafkago.UnknownTopicOrPartition}
	c := &KafkaConsumer{
		cfg: ConsumerConfig{
			Logger:                     noopLogger(),
			DeadLetterCreateRetries:    5,
			DeadLetterCreateBackoff:    time.Hour, // force the wait so cancel wins
			DeadLetterCreateMaxBackoff: time.Hour,
		},
		dlqWriter: sink,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.deadLetterWriteWithRetry(ctx, kafkago.Message{Topic: "topic1.dlq"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestNewConsumer_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ConsumerConfig
		wantErr string
	}{
		{
			name:    "no brokers",
			cfg:     ConsumerConfig{GroupID: "g", Topics: []string{"t"}},
			wantErr: "at least one broker",
		},
		{
			name:    "no group ID",
			cfg:     ConsumerConfig{Brokers: []string{"localhost:9092"}, Topics: []string{"t"}},
			wantErr: "group ID is required",
		},
		{
			name:    "no topics",
			cfg:     ConsumerConfig{Brokers: []string{"localhost:9092"}, GroupID: "g"},
			wantErr: "at least one topic",
		},
		{
			name: "valid config",
			cfg: ConsumerConfig{
				Brokers: []string{"localhost:9092"},
				GroupID: "test-group",
				Topics:  []string{"sentiae.identity.user"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewConsumer(tt.cfg)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q", tt.wantErr)
				}
				if !containsStr(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c == nil {
				t.Fatal("consumer should not be nil")
			}
		})
	}
}

func TestConsumer_Defaults(t *testing.T) {
	c, err := NewConsumer(ConsumerConfig{
		Brokers: []string{"localhost:9092"},
		GroupID: "test-group",
		Topics:  []string{"sentiae.identity.user"},
	})
	if err != nil {
		t.Fatalf("NewConsumer() error = %v", err)
	}

	if c.cfg.MinBytes != 1 {
		t.Errorf("MinBytes = %d, want 1", c.cfg.MinBytes)
	}
	if c.cfg.MaxBytes != 10e6 {
		t.Errorf("MaxBytes = %d, want 10e6", c.cfg.MaxBytes)
	}
	if c.cfg.MaxWait != 10*time.Second {
		t.Errorf("MaxWait = %v, want 10s", c.cfg.MaxWait)
	}
	if c.cfg.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", c.cfg.MaxRetries)
	}
}

func TestConsumer_Subscribe(t *testing.T) {
	c, err := NewConsumer(ConsumerConfig{
		Brokers: []string{"localhost:9092"},
		GroupID: "test-group",
		Topics:  []string{"sentiae.identity.user"},
	})
	if err != nil {
		t.Fatalf("NewConsumer() error = %v", err)
	}

	called := false
	c.Subscribe("identity.user.registered", func(ctx context.Context, event CloudEvent) error {
		called = true
		return nil
	})

	if _, ok := c.handlers["identity.user.registered"]; !ok {
		t.Error("handler should be registered")
	}

	// Subscribe is additive
	c.Subscribe("identity.user.deleted", func(ctx context.Context, event CloudEvent) error {
		return nil
	})

	if len(c.handlers) != 2 {
		t.Errorf("expected 2 handlers, got %d", len(c.handlers))
	}

	// Verify the handler is callable
	_ = c.handlers["identity.user.registered"](context.Background(), CloudEvent{})
	if !called {
		t.Error("handler should have been called")
	}
}

func TestConsumer_Close(t *testing.T) {
	c, err := NewConsumer(ConsumerConfig{
		Brokers: []string{"localhost:9092"},
		GroupID: "test-group",
		Topics:  []string{"sentiae.identity.user"},
	})
	if err != nil {
		t.Fatalf("NewConsumer() error = %v", err)
	}

	// Close with no readers should be fine
	if err := c.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

// TestConsumer_SchemaValidation_RoutesToDLQ makes sure payloads that
// don't match the registered taxonomy land in the DLQ instead of the
// handler. Exercises the validateMessagePayload hook used by
// handleFetchedMessage for §A6.
func TestConsumer_SchemaValidation_RoutesToDLQ(t *testing.T) {
	// Register a known event with a schema that requires `user_id`.
	if err := RegisterExtensionEvent(RegisteredEvent{
		Type:        "test.schema.required",
		Domain:      "test",
		Description: "test event for schema validation",
		Owner:       "platform-kit",
		Schema: `{
			"type": "object",
			"required": ["user_id"],
			"properties": {
				"user_id": {"type": "string"}
			}
		}`,
	}); err != nil {
		t.Fatalf("register schema: %v", err)
	}

	// Payload deliberately omits the required field.
	bad := map[string]any{"not_user_id": "oops"}

	c := &KafkaConsumer{
		cfg: ConsumerConfig{
			Logger: noopLogger(),
		},
		handlers: map[string]EventHandler{
			"test.schema.required": func(ctx context.Context, event CloudEvent) error { return nil },
		},
	}

	// validateMessagePayload is the unit under test.
	raw, _ := json.Marshal(bad)
	event := CloudEvent{Type: "test.schema.required", Data: raw}
	if err := c.validateMessagePayload(event); err == nil {
		t.Fatal("expected validation failure for missing user_id, got nil")
	}

	// Good payload passes.
	good := map[string]any{"user_id": "u-1"}
	raw, _ = json.Marshal(good)
	event = CloudEvent{Type: "test.schema.required", Data: raw}
	if err := c.validateMessagePayload(event); err != nil {
		t.Fatalf("expected valid payload to pass, got %v", err)
	}
}

func TestDeadLetterFunc(t *testing.T) {
	var mu sync.Mutex
	var captured *FailedMessage

	dlFunc := func(ctx context.Context, msg FailedMessage) {
		mu.Lock()
		defer mu.Unlock()
		captured = &msg
	}

	// Simulate what sendToDeadLetter does
	c := &KafkaConsumer{
		cfg: ConsumerConfig{
			DeadLetterFunc: dlFunc,
			Logger:         noopLogger(),
		},
		handlers: make(map[string]EventHandler),
		health:   make(map[string]*topicHealth),
	}

	testData := EventData{
		ResourceType: "user",
		ResourceID:   "u-123",
		Timestamp:    time.Now().UTC(),
	}
	rawData, _ := json.Marshal(testData)

	ce := CloudEvent{
		SpecVersion:     "1.0",
		ID:              "evt-1",
		Source:          "test",
		Type:            "identity.user.registered",
		Time:            time.Now().UTC().Format(time.RFC3339),
		DataContentType: "application/json",
		Data:            rawData,
	}
	payload, _ := json.Marshal(ce)

	c.sendToDeadLetter(context.Background(), fakeKafkaMsg("sentiae.identity.user", payload, 42), "identity.user.registered", 3, errTest)

	mu.Lock()
	defer mu.Unlock()

	if captured == nil {
		t.Fatal("dead letter func should have been called")
	}
	if captured.Topic != "sentiae.identity.user" {
		t.Errorf("Topic = %q, want %q", captured.Topic, "sentiae.identity.user")
	}
	if captured.EventType != "identity.user.registered" {
		t.Errorf("EventType = %q, want %q", captured.EventType, "identity.user.registered")
	}
	if captured.RetryCount != 3 {
		t.Errorf("RetryCount = %d, want 3", captured.RetryCount)
	}
	if captured.LastError != errTest {
		t.Errorf("LastError = %v, want %v", captured.LastError, errTest)
	}
	if captured.Offset != 42 {
		t.Errorf("Offset = %d, want 42", captured.Offset)
	}
}
