package kafka

import (
	"strings"
	"testing"
)

// knownDomainMismatch is the explicit allow-list of taxonomy entries whose
// leading segment does not match their declared Domain. The import-analyze
// saga family was registered under the "canvas" resource path (because
// canvas-service coordinates it) but logically belongs to the "saga" domain.
// New entries should not be added here without justification.
var knownDomainMismatch = map[string]bool{
	"canvas.saga.import_analyze_canvas.started":        true,
	"canvas.saga.import_analyze_canvas.nodes_created":  true,
	"canvas.saga.import_analyze_canvas.edges_inferred": true,
	"canvas.saga.import_analyze_canvas.layout_applied": true,
	"canvas.saga.import_analyze_canvas.completed":      true,
	"canvas.saga.import_analyze_canvas.failed":         true,
}

// TestEventNamingConvention enforces the Sentiae taxonomy rule:
// every registered event type must be "<domain>.<resource>.<action>" or
// "<domain>.<resource>.<sub>.<action>" in all-lowercase underscored segments.
// The ValidateEventType regex covers the char class; this test additionally
// asserts that NO registered event yields a double-prefixed on-the-wire topic
// ("sentiae.sentiae.*"). Event constants that carry the prefix themselves are
// normalised by topicFromEventType, so a doubled topic here means the
// derivation regressed — no allow-list, no exceptions: a doubled topic is a
// topic no consumer subscribes to, and the event silently disappears.
//
// Closes Sentiae MEDIUM C5: CI test for topic naming standardization.
func TestEventNamingConvention(t *testing.T) {
	for _, e := range AllEvents() {
		e := e
		t.Run(e.Type, func(t *testing.T) {
			if err := ValidateEventType(e.Type); err != nil {
				t.Fatalf("event %q violates naming convention: %v", e.Type, err)
			}

			// Full topic under "sentiae" prefix must never double-prefix.
			topic := e.FullTopic("sentiae")
			if strings.HasPrefix(topic, "sentiae.sentiae.") || topic == "sentiae.sentiae" {
				t.Errorf("event %q yields double-prefixed topic %q — no consumer subscribes to a doubled topic, so the event would be silently lost", e.Type, topic)
			}

			// Domain must be the leading segment (with explicit exceptions).
			if e.Domain != "" && !knownDomainMismatch[e.Type] {
				leading := DomainOf(e.Type)
				// Legacy "sentiae.*" entries use Domain="sentiae" and their
				// leading segment is also "sentiae" — that matches.
				if leading != e.Domain {
					t.Errorf("event %q: leading segment %q does not match declared Domain %q",
						e.Type, leading, e.Domain)
				}
			}
		})
	}
}

// TestAllEventsHaveSchemasAndOwners tightens the existing registry integrity
// test by asserting every entry has both a schema AND an owner field,
// otherwise OpsGenie / runbooks can't tell who to page when a topic breaks.
func TestAllEventsHaveSchemasAndOwners(t *testing.T) {
	for _, e := range AllEvents() {
		if e.Schema == "" {
			t.Errorf("event %q has no schema", e.Type)
		}
		if e.Owner == "" {
			t.Errorf("event %q has no owner (can't page anyone on DLQ)", e.Type)
		}
		if e.Description == "" {
			t.Errorf("event %q has no description", e.Type)
		}
	}
}

// TestKnownTopicsAreUnique makes sure KnownTopics returns only kafka-safe
// strings and that every topic starts with the given prefix.
func TestKnownTopicsAreUnique(t *testing.T) {
	topics := KnownTopics("sentiae")
	seen := map[string]bool{}
	for _, t_ := range topics {
		if seen[t_] {
			t.Errorf("duplicate topic %q in KnownTopics", t_)
		}
		seen[t_] = true
		if !strings.HasPrefix(t_, "sentiae.") {
			t.Errorf("topic %q missing 'sentiae.' prefix", t_)
		}
	}
}
