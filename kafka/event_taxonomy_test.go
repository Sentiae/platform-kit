package kafka

import (
	"context"
	"strings"
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

func TestCatalogSystemTopologyChanged_TopicAndOwnership(t *testing.T) {
	e, ok := LookupEvent(EventCatalogSystemTopologyChanged)
	if !ok {
		t.Fatalf("event %q not registered", EventCatalogSystemTopologyChanged)
	}
	if EventCatalogSystemTopologyChanged != "catalog.system.topology_changed" {
		t.Fatalf("unexpected event type constant: %q", EventCatalogSystemTopologyChanged)
	}
	if got, want := e.FullTopic("sentiae"), "sentiae.catalog.system"; got != want {
		t.Fatalf("wire topic = %q, want %q", got, want)
	}
	if got, want := e.Owner, "catalog-service"; got != want {
		t.Fatalf("producer (Owner) = %q, want %q", got, want)
	}
	if got, want := e.Domain, "catalog"; got != want {
		t.Fatalf("domain = %q, want %q", got, want)
	}
}

func TestWorkFeatureMembershipEvents_TopicAndOwnership(t *testing.T) {
	cases := []struct {
		constVal string
		wantType string
	}{
		{EventWorkFeatureProductAssigned, "work.feature.product_assigned"},
		{EventWorkFeatureComponentLinked, "work.feature.component_linked"},
		{EventWorkFeatureComponentUnlinked, "work.feature.component_unlinked"},
	}
	for _, tc := range cases {
		t.Run(tc.wantType, func(t *testing.T) {
			if tc.constVal != tc.wantType {
				t.Fatalf("unexpected event type constant: %q, want %q", tc.constVal, tc.wantType)
			}
			e, ok := LookupEvent(tc.constVal)
			if !ok {
				t.Fatalf("event %q not registered", tc.constVal)
			}
			if got, want := e.FullTopic("sentiae"), "sentiae.work.feature"; got != want {
				t.Fatalf("wire topic = %q, want %q", got, want)
			}
			if got, want := e.Owner, "work-service"; got != want {
				t.Fatalf("producer (Owner) = %q, want %q", got, want)
			}
			if got, want := e.Domain, "work"; got != want {
				t.Fatalf("domain = %q, want %q", got, want)
			}
		})
	}

	// The component_linked event must carry the relationship enum in its schema.
	linked, ok := LookupEvent(EventWorkFeatureComponentLinked)
	if !ok {
		t.Fatalf("event %q not registered", EventWorkFeatureComponentLinked)
	}
	for _, want := range []string{`"relationship"`, `"implements"`, `"depends_on"`, `"enum"`} {
		if !strings.Contains(linked.Schema, want) {
			t.Fatalf("component_linked schema missing %s; schema=%s", want, linked.Schema)
		}
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
