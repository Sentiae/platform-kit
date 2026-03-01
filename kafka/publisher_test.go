package kafka

import (
	"testing"
)

func TestNewPublisher_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     PublisherConfig
		wantErr string
	}{
		{
			name:    "no brokers",
			cfg:     PublisherConfig{Source: "svc"},
			wantErr: "at least one broker",
		},
		{
			name:    "no source",
			cfg:     PublisherConfig{Brokers: []string{"localhost:9092"}},
			wantErr: "source is required",
		},
		{
			name: "valid config",
			cfg: PublisherConfig{
				Brokers: []string{"localhost:9092"},
				Source:  "test-service",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub, err := NewPublisher(tt.cfg)
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
			if pub == nil {
				t.Fatal("publisher should not be nil")
			}
			_ = pub.Close()
		})
	}
}

func TestNewPublisher_Defaults(t *testing.T) {
	pub, err := NewPublisher(PublisherConfig{
		Brokers: []string{"localhost:9092"},
		Source:  "test-service",
	})
	if err != nil {
		t.Fatalf("NewPublisher() error = %v", err)
	}
	defer pub.Close()

	if pub.cfg.TopicPrefix != "sentiae" {
		t.Errorf("TopicPrefix = %q, want %q", pub.cfg.TopicPrefix, "sentiae")
	}
	if pub.cfg.BatchSize != 100 {
		t.Errorf("BatchSize = %d, want %d", pub.cfg.BatchSize, 100)
	}
	if pub.cfg.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want %d", pub.cfg.MaxAttempts, 3)
	}
}

func TestPublisher_ValidatesEventType(t *testing.T) {
	pub, err := NewPublisher(PublisherConfig{
		Brokers: []string{"localhost:9092"},
		Source:  "test-service",
	})
	if err != nil {
		t.Fatalf("NewPublisher() error = %v", err)
	}
	defer pub.Close()

	tests := []struct {
		name      string
		eventType string
		wantErr   bool
	}{
		{"valid event type", "identity.user.registered", false},
		{"invalid - two parts", "identity.user", true},
		{"invalid - uppercase", "Identity.User.Registered", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We can't actually write to Kafka in unit tests,
			// but we can verify validation runs before the write attempt.
			// Invalid event types should fail immediately; valid ones will
			// fail at the WriteMessages call (no Kafka broker).
			data := EventData{
				ResourceType: "user",
				ResourceID:   "id-123",
			}
			err := pub.Publish(t.Context(), tt.eventType, data)
			if tt.wantErr {
				if err == nil {
					t.Error("expected validation error")
				}
				if !containsStr(err.Error(), "invalid event type") {
					t.Errorf("expected validation error, got: %v", err)
				}
			}
			// For valid event types, err will be a Kafka connection error — that's expected.
		})
	}
}

func TestPublisher_PublishBatchEmpty(t *testing.T) {
	pub, err := NewPublisher(PublisherConfig{
		Brokers: []string{"localhost:9092"},
		Source:  "test-service",
	})
	if err != nil {
		t.Fatalf("NewPublisher() error = %v", err)
	}
	defer pub.Close()

	// Empty batch should succeed immediately
	err = pub.PublishBatch(t.Context(), nil)
	if err != nil {
		t.Errorf("PublishBatch(nil) error = %v", err)
	}

	err = pub.PublishBatch(t.Context(), []Event{})
	if err != nil {
		t.Errorf("PublishBatch([]) error = %v", err)
	}
}

func TestPublisher_PublishBatchValidation(t *testing.T) {
	pub, err := NewPublisher(PublisherConfig{
		Brokers: []string{"localhost:9092"},
		Source:  "test-service",
	})
	if err != nil {
		t.Fatalf("NewPublisher() error = %v", err)
	}
	defer pub.Close()

	events := []Event{
		{Type: "identity.user.created", Data: EventData{ResourceType: "user", ResourceID: "1"}},
		{Type: "INVALID", Data: EventData{ResourceType: "user", ResourceID: "2"}},
	}

	err = pub.PublishBatch(t.Context(), events)
	if err == nil {
		t.Error("expected validation error for invalid event type in batch")
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
