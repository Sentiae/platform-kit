package kafka

import (
	"testing"
	"time"
)

// nodeEventFixture returns a fully-populated EventData for one of the four
// node-registry events (DESIGN node-as-repository §3.4 C envelope) plus the
// metadata key whose removal must make validation fail.
func nodeEventFixture(eventType string) (EventData, string) {
	base := EventData{
		ActorID:      "node-service",
		ActorType:    "service",
		ResourceType: "node_version",
		ResourceID:   "acme/hello@1.0.0",
		Timestamp:    time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
	}
	switch eventType {
	case EventNodeVersionIngested:
		base.Metadata = map[string]any{
			"qualified_name":  "acme/hello",
			"semver":          "1.0.0",
			"repo_ref":        "acme/hello.node",
			"commit_sha":      "0123456789abcdef0123456789abcdef01234567",
			"tag_sha":         "89abcdef0123456789abcdef0123456789abcdef",
			"source_digest":   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"implementations": []string{"go", "typescript"},
			"tier":            "community",
		}
		return base, "source_digest"
	case EventNodeVersionRejected:
		base.Metadata = map[string]any{
			"qualified_name": "acme/hello",
			"semver":         "1.0.0",
			"repo_ref":       "acme/hello.node",
			"reason":         "version_conflict",
			"detail":         "1.0.0 already points at another commit",
		}
		return base, "reason"
	case EventNodeVersionPublished:
		base.Metadata = map[string]any{
			"qualified_name": "acme/hello",
			"semver":         "1.0.0",
			"commit_sha":     "0123456789abcdef0123456789abcdef01234567",
			"source_digest":  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"bundles": []map[string]any{
				{"language": "go", "image_ref": "registry/acme/hello:1.0.0-go", "digest": "sha256:abc"},
			},
		}
		return base, "bundles"
	case EventDeliveryNodeBundleBuilt:
		base.ActorID = "delivery-service"
		base.Metadata = map[string]any{
			"qualified_name": "acme/hello",
			"semver":         "1.0.0",
			"language":       "go",
			"image_ref":      "registry/acme/hello:1.0.0-go",
			"digest":         "sha256:abc",
			"source_digest":  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}
		return base, "digest"
	}
	panic("no fixture for " + eventType)
}

// TestNodeEventsRegistered proves the four node-registry events are in the
// shared taxonomy with the domain, owner and on-the-wire topic DESIGN §3.4 A
// pins. Control: comment out `registry[e.Type] = e` in
// event_taxonomy_node.go's init() → LookupEvent misses and this test is red.
func TestNodeEventsRegistered(t *testing.T) {
	for _, tt := range []struct {
		eventType string
		domain    string
		owner     string
		topic     string
	}{
		{EventNodeVersionIngested, "node", "node-service", "sentiae.node.version"},
		{EventNodeVersionRejected, "node", "node-service", "sentiae.node.version"},
		{EventNodeVersionPublished, "node", "node-service", "sentiae.node.version"},
		{EventDeliveryNodeBundleBuilt, "delivery", "delivery-service", "sentiae.delivery.node_bundle"},
	} {
		t.Run(tt.eventType, func(t *testing.T) {
			e, ok := LookupEvent(tt.eventType)
			if !ok {
				t.Fatalf("event %q is not registered in the taxonomy", tt.eventType)
			}
			if e.Domain != tt.domain {
				t.Errorf("domain = %q, want %q", e.Domain, tt.domain)
			}
			if e.Owner != tt.owner {
				t.Errorf("owner = %q, want %q", e.Owner, tt.owner)
			}
			if got := e.FullTopic("sentiae"); got != tt.topic {
				t.Errorf("FullTopic(\"sentiae\") = %q, want %q", got, tt.topic)
			}
		})
	}
}

// TestNodeEventSchemas_RequiredKeys proves each node-registry schema actually
// enforces its required metadata: the full payload validates (the positive
// anchor) and the same payload minus one required key does not. Control: drop
// "reason" from node.version.rejected's required list in
// event_taxonomy_node.go → the rejected sub-test's negative case validates and
// this test is red.
func TestNodeEventSchemas_RequiredKeys(t *testing.T) {
	for _, eventType := range []string{
		EventNodeVersionIngested,
		EventNodeVersionRejected,
		EventNodeVersionPublished,
		EventDeliveryNodeBundleBuilt,
	} {
		t.Run(eventType, func(t *testing.T) {
			full, dropKey := nodeEventFixture(eventType)
			if err := ValidateEventPayload(eventType, full); err != nil {
				t.Fatalf("full payload must validate: %v", err)
			}

			trimmed, _ := nodeEventFixture(eventType)
			meta := make(map[string]any, len(trimmed.Metadata))
			for k, v := range trimmed.Metadata {
				if k == dropKey {
					continue
				}
				meta[k] = v
			}
			trimmed.Metadata = meta
			if err := ValidateEventPayload(eventType, trimmed); err == nil {
				t.Fatalf("payload without required metadata key %q must be refused", dropKey)
			}
		})
	}
}
