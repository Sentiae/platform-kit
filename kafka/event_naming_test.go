package kafka

import (
	"strings"
	"testing"
)

// knownLegacyDoublePrefix is the explicit allow-list of "sentiae.*" bare
// event types that ship in the taxonomy as legacy aliases. They are
// intentionally kept so existing consumers do not break; new constants must
// NOT be added here. The list exists solely so the naming-convention test
// fails loudly when anyone adds a *new* "sentiae.*" entry — at which point
// the CI signal is to either (a) drop the prefix or (b) register the event
// under the legacy "sentiae" domain AND add it to this list with a note.
var knownLegacyDoublePrefix = map[string]bool{
	"sentiae.git.repository.created":     true,
	"sentiae.git.repository.imported":    true,
	"sentiae.git.repository.forked":      true,
	"sentiae.git.push":                   true,
	"sentiae.git.commit.created":         true,
	"sentiae.git.pr.created":             true,
	"sentiae.git.pr.updated":             true,
	"sentiae.git.pr.merged":              true,
	"sentiae.git.pr.closed":              true,
	"sentiae.git.pr.review_requested":    true,
	"sentiae.git.pr.approved":            true,
	"sentiae.git.pr.changes_requested":   true,
	"sentiae.git.pr.review.submitted":    true,
	"sentiae.git.branch.created":         true,
	"sentiae.git.branch.deleted":         true,
	"sentiae.git.branch.protected_changed": true,
	"sentiae.git.session.created":        true,
	"sentiae.git.session.code_ready":     true,
	"sentiae.git.session.merged":         true,
	"sentiae.git.session.closed":         true,
	"sentiae.git.ai_review.completed":    true,
	"sentiae.git.release.created":        true,
	"sentiae.git.doc.stale":              true,
}

// knownDomainMismatch is the explicit allow-list of taxonomy entries whose
// leading segment does not match their declared Domain. The import-analyze
// saga family was registered under the "canvas" resource path (because
// canvas-service coordinates it) but logically belongs to the "saga" domain.
// New entries should not be added here without justification.
var knownDomainMismatch = map[string]bool{
	"canvas.saga.import_analyze_canvas.started":       true,
	"canvas.saga.import_analyze_canvas.nodes_created": true,
	"canvas.saga.import_analyze_canvas.edges_inferred": true,
	"canvas.saga.import_analyze_canvas.layout_applied": true,
	"canvas.saga.import_analyze_canvas.completed":     true,
	"canvas.saga.import_analyze_canvas.failed":        true,
}

// TestEventNamingConvention enforces the Sentiae taxonomy rule:
// every registered event type must be "<domain>.<resource>.<action>" or
// "<domain>.<resource>.<sub>.<action>" in all-lowercase underscored segments.
// The ValidateEventType regex covers the char class; this test additionally
// asserts that on-the-wire topics under the standard "sentiae" prefix are
// not already prefixed (to catch accidental "sentiae.canvas.node" event
// constants that would yield "sentiae.sentiae.canvas" topics) beyond the
// explicit legacy allow-list above.
//
// Closes Sentiae MEDIUM C5: CI test for topic naming standardization.
func TestEventNamingConvention(t *testing.T) {
	for _, e := range AllEvents() {
		e := e
		t.Run(e.Type, func(t *testing.T) {
			if err := ValidateEventType(e.Type); err != nil {
				t.Fatalf("event %q violates naming convention: %v", e.Type, err)
			}

			// Full topic under "sentiae" prefix must not double-prefix
			// except for the known legacy set.
			topic := e.FullTopic("sentiae")
			if strings.HasPrefix(topic, "sentiae.sentiae.") && !knownLegacyDoublePrefix[e.Type] {
				t.Errorf("event %q yields double-prefixed topic %q — strip the hard-coded prefix from the constant, or add it to knownLegacyDoublePrefix with a note", e.Type, topic)
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
