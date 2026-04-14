package kafka

import (
	"context"
	"testing"
	"time"
)

func TestRegistryIntegrity(t *testing.T) {
	events := AllEvents()
	if len(events) == 0 {
		t.Fatal("registry is empty")
	}
	seen := map[string]bool{}
	for _, e := range events {
		if e.Type == "" {
			t.Error("event with empty type")
		}
		if err := ValidateEventType(e.Type); err != nil {
			t.Errorf("invalid event name %q: %v", e.Type, err)
		}
		if seen[e.Type] {
			t.Errorf("duplicate event type %q", e.Type)
		}
		seen[e.Type] = true

		if e.Schema == "" {
			t.Errorf("event %s has no schema", e.Type)
		}
		if _, err := compileSchema(e.Type, e.Schema); err != nil {
			t.Errorf("schema for %s failed to compile: %v", e.Type, err)
		}
		if e.Domain == "" {
			t.Errorf("event %s has no domain", e.Type)
		}
		if e.Owner == "" {
			t.Errorf("event %s has no owner", e.Type)
		}
	}
}

func TestValidatePayload_MissingRequired(t *testing.T) {
	data := EventData{
		ResourceType: "spec",
		ResourceID:   "abc",
		Timestamp:    time.Now().UTC(),
		// missing required "title" + "status" in metadata
	}
	err := ValidateEventPayload(EventWorkSpecCreated, data)
	if err == nil {
		t.Fatal("expected validation to fail for missing title/status")
	}
}

func TestValidatePayload_OK(t *testing.T) {
	data := EventData{
		ResourceType: "spec",
		ResourceID:   "abc",
		Timestamp:    time.Now().UTC(),
		Metadata: map[string]any{
			"title":  "Checkout refactor",
			"status": "draft",
		},
	}
	if err := ValidateEventPayload(EventWorkSpecCreated, data); err != nil {
		t.Fatalf("expected OK, got: %v", err)
	}
}

func TestValidatePayload_UnknownEvent(t *testing.T) {
	err := ValidateEventPayload("nonsense.event.type", EventData{
		ResourceType: "x", ResourceID: "y", Timestamp: time.Now(),
	})
	if err == nil {
		t.Fatal("expected error for unregistered event")
	}
}

func TestNoopPublisher_UnaffectedByValidation(t *testing.T) {
	// The no-op publisher should accept anything — it has no validation hook.
	np := NewNoopPublisher()
	err := np.Publish(context.Background(), "whatever.goes.here", EventData{})
	if err != nil {
		t.Fatalf("noop should never fail: %v", err)
	}
}
