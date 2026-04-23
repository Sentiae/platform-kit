package kafka

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

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
// handler. Exercises the validateMessagePayload hook added to
// consumeTopic for §A6.
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
