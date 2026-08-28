package kafka

import (
	"strings"
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

// nodeBundleRawPayload builds the EventData-shaped generic map a consumer
// hands to ValidateRawPayload (P2-SPEC §3.1 A): the envelope keys the shared
// dataSchema requires plus the metadata object under test.
func nodeBundleRawPayload(metadata map[string]any) map[string]any {
	return map[string]any{
		"actor_id":      "delivery-service",
		"actor_type":    "service",
		"resource_type": "deployment",
		"resource_id":   "acme/hello",
		"timestamp":     "2026-08-28T12:00:00Z",
		"metadata":      metadata,
	}
}

// nodeBundleFailedMetadata returns the five required metadata keys of
// delivery.node_bundle.failed, fully populated.
func nodeBundleFailedMetadata() map[string]any {
	return map[string]any{
		"qualified_name": "acme/hello",
		"semver":         "1.0.1",
		"language":       "go",
		"step":           "conformance",
		"detail":         `port out "out": declared but never emitted`,
	}
}

// TestNodeBundleFailed_Registered proves delivery.node_bundle.failed is in the
// shared taxonomy with the domain, owner and on-the-wire topic P2-SPEC §3.1 A
// pins — one topic (sentiae.delivery.node_bundle), two event types (G-5).
// Control: remove the EventDeliveryNodeBundleFailed entry from extras in
// event_taxonomy_node.go → LookupEvent misses and this test is red.
func TestNodeBundleFailed_Registered(t *testing.T) {
	e, ok := LookupEvent(EventDeliveryNodeBundleFailed)
	if !ok {
		t.Fatalf("event %q is not registered in the taxonomy", EventDeliveryNodeBundleFailed)
	}
	if e.Domain != "delivery" {
		t.Errorf("domain = %q, want %q", e.Domain, "delivery")
	}
	if e.Owner != "delivery-service" {
		t.Errorf("owner = %q, want %q", e.Owner, "delivery-service")
	}
	if got := e.FullTopic("sentiae"); got != "sentiae.delivery.node_bundle" {
		t.Errorf("FullTopic(\"sentiae\") = %q, want %q", got, "sentiae.delivery.node_bundle")
	}
}

// TestNodeBundleFailed_PayloadShape proves the failed schema enforces its five
// required metadata keys and both closed enums: a fully-populated payload
// validates (the positive anchor), one without "detail" does not, and a step or
// a language outside the P2-SPEC §3.1 A enum does not. Control: drop "detail"
// from the required list in event_taxonomy_node.go → the missing-detail case
// validates and this test is red.
func TestNodeBundleFailed_PayloadShape(t *testing.T) {
	t.Run("full payload validates", func(t *testing.T) {
		if err := ValidateRawPayload(EventDeliveryNodeBundleFailed, nodeBundleRawPayload(nodeBundleFailedMetadata())); err != nil {
			t.Fatalf("full payload must validate: %v", err)
		}
	})

	for _, tt := range []struct {
		name    string
		mutMeta func(map[string]any)
	}{
		{"missing detail", func(m map[string]any) { delete(m, "detail") }},
		{"step outside the enum", func(m map[string]any) { m["step"] = "sign" }},
		{"language outside the enum", func(m map[string]any) { m["language"] = "python" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			meta := nodeBundleFailedMetadata()
			tt.mutMeta(meta)
			if err := ValidateRawPayload(EventDeliveryNodeBundleFailed, nodeBundleRawPayload(meta)); err == nil {
				t.Fatalf("payload %q must be refused", tt.name)
			}
		})
	}
}

// TestNodeBundleBuilt_OptionalProvenanceKeys proves the three provenance keys
// P2-SPEC §3.1 A appends to delivery.node_bundle.built are declared but
// OPTIONAL: a payload carrying them validates, a payload without them still
// validates, and an empty image_ref is still refused (the positive anchor that
// the built schema is being enforced at all). Control: remove image_ref's
// minLength from event_taxonomy_node.go → the empty-image_ref case validates
// and this test is red.
func TestNodeBundleBuilt_OptionalProvenanceKeys(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"qualified_name": "acme/hello",
			"semver":         "1.0.1",
			"language":       "go",
			"image_ref":      "10.0.10.20:8078/acme/hello.node:1.0.1-go",
			"digest":         "sha256:" + strings.Repeat("a", 64),
			"source_digest":  strings.Repeat("b", 64),
		}
	}

	t.Run("with provenance keys", func(t *testing.T) {
		meta := base()
		meta["commit_sha"] = strings.Repeat("c", 40)
		meta["signature_digest"] = "sha256:" + strings.Repeat("d", 64)
		meta["scan_id"] = "scan-123"
		if err := ValidateRawPayload(EventDeliveryNodeBundleBuilt, nodeBundleRawPayload(meta)); err != nil {
			t.Fatalf("payload with the provenance keys must validate: %v", err)
		}
	})

	t.Run("without provenance keys", func(t *testing.T) {
		if err := ValidateRawPayload(EventDeliveryNodeBundleBuilt, nodeBundleRawPayload(base())); err != nil {
			t.Fatalf("payload without the provenance keys must still validate (they are optional): %v", err)
		}
	})

	t.Run("empty image_ref refused", func(t *testing.T) {
		meta := base()
		meta["image_ref"] = ""
		if err := ValidateRawPayload(EventDeliveryNodeBundleBuilt, nodeBundleRawPayload(meta)); err == nil {
			t.Fatalf("payload with an empty image_ref must be refused")
		}
	})
}
