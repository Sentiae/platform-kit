package kafka

import (
	"testing"
	"time"
)

// TestSecurityEvents_TopicAndOwnership pins every security-domain event (plus
// vigil's code.graph.updated) to its derived wire topic and owner. The topic
// strings are asserted literally: a consumer that derives its subscription
// from the taxonomy starves silently if derivation drifts.
func TestSecurityEvents_TopicAndOwnership(t *testing.T) {
	cases := []struct {
		constVal string
		wantType string
		wantTopc string
		wantDom  string
	}{
		{EventSecurityFindingCreated, "security.finding.created", "sentiae.security.finding", "security"},
		{EventSecurityFindingUpdated, "security.finding.updated", "sentiae.security.finding", "security"},
		{EventSecurityFindingResolved, "security.finding.resolved", "sentiae.security.finding", "security"},
		{EventSecurityFindingSLABreach, "security.finding.sla_breach", "sentiae.security.finding", "security"},
		{EventSecurityAlertCritical, "security.alert.critical", "sentiae.security.alert", "security"},
		{EventSecurityScanStarted, "security.scan.started", "sentiae.security.scan", "security"},
		{EventSecurityScanCompleted, "security.scan.completed", "sentiae.security.scan", "security"},
		{EventSecurityScanFailed, "security.scan.failed", "sentiae.security.scan", "security"},
		{EventSecuritySecretDetected, "security.secret.detected", "sentiae.security.secret", "security"},
		{EventSecurityAssetDiscovered, "security.asset.discovered", "sentiae.security.asset", "security"},
		{EventSecurityAgentOffline, "security.agent.offline", "sentiae.security.agent", "security"},
		{EventSecurityAttackChainDetected, "security.attack_chain.detected", "sentiae.security.attack_chain", "security"},
		{EventCodeGraphUpdated, "code.graph.updated", "sentiae.code.graph", "code"},
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
			if got := e.FullTopic("sentiae"); got != tc.wantTopc {
				t.Fatalf("wire topic = %q, want %q", got, tc.wantTopc)
			}
			if got, want := e.Owner, "vigil-service"; got != want {
				t.Fatalf("producer (Owner) = %q, want %q", got, want)
			}
			if got := e.Domain; got != tc.wantDom {
				t.Fatalf("domain = %q, want %q", got, tc.wantDom)
			}
		})
	}
}

func TestFoundryLLMAudit_TopicAndOwnership(t *testing.T) {
	if EventFoundryLLMAudit != "foundry.llm.audit" {
		t.Fatalf("unexpected event type constant: %q", EventFoundryLLMAudit)
	}
	e, ok := LookupEvent(EventFoundryLLMAudit)
	if !ok {
		t.Fatalf("event %q not registered", EventFoundryLLMAudit)
	}
	if got, want := e.FullTopic("sentiae"), "sentiae.foundry.llm"; got != want {
		t.Fatalf("wire topic = %q, want %q", got, want)
	}
	if got, want := e.Owner, "foundry-service"; got != want {
		t.Fatalf("producer (Owner) = %q, want %q", got, want)
	}

	// Positive control: the full payload foundry's emitter builds.
	data := EventData{
		ActorType:      "system",
		ResourceType:   "llm_audit",
		ResourceID:     "1c5c9f6c-9e6e-4a2f-9a7f-2f5d0a1b3c4d",
		OrganizationID: "8f1d3c1e-1a2b-4c3d-9e8f-7a6b5c4d3e2f",
		Timestamp:      time.Now().UTC(),
		Metadata: map[string]any{
			"audit_id":           "1c5c9f6c-9e6e-4a2f-9a7f-2f5d0a1b3c4d",
			"organization_id":    "8f1d3c1e-1a2b-4c3d-9e8f-7a6b5c4d3e2f",
			"provider":           "anthropic",
			"model":              "claude-opus-5",
			"prompt_tokens":      1200,
			"completion_tokens":  340,
			"cache_read_tokens":  900,
			"cache_write_tokens": 0,
			"total_tokens":       1540,
			"latency_ms":         820,
			"status":             "success",
			"error":              "",
			"cost_usd":           0.0412,
			"tool_name":          "read_file",
			"created_at":         time.Now().UTC().Format(time.RFC3339),
			"user_id":            "2b2b2b2b-0000-4000-8000-000000000001",
			"agent_id":           "2b2b2b2b-0000-4000-8000-000000000002",
			"team_id":            "2b2b2b2b-0000-4000-8000-000000000003",
			"feature_id":         "2b2b2b2b-0000-4000-8000-000000000004",
			"spec_id":            "2b2b2b2b-0000-4000-8000-000000000005",
			"run_id":             "2b2b2b2b-0000-4000-8000-000000000006",
			"session_id":         "2b2b2b2b-0000-4000-8000-000000000007",
		},
	}
	if err := ValidateEventPayload(EventFoundryLLMAudit, data); err != nil {
		t.Fatalf("expected the emitter payload to validate, got: %v", err)
	}
}

func TestSagaSpecShippingCompleted_TopicAndOwnership(t *testing.T) {
	if EventSagaSpecShippingCompleted != "saga.spec_shipping.completed" {
		t.Fatalf("unexpected event type constant: %q", EventSagaSpecShippingCompleted)
	}
	e, ok := LookupEvent(EventSagaSpecShippingCompleted)
	if !ok {
		t.Fatalf("event %q not registered", EventSagaSpecShippingCompleted)
	}
	if got, want := e.FullTopic("sentiae"), "sentiae.saga.spec_shipping"; got != want {
		t.Fatalf("wire topic = %q, want %q", got, want)
	}
	if got, want := e.Owner, "work-service"; got != want {
		t.Fatalf("producer (Owner) = %q, want %q", got, want)
	}
	data := EventData{
		ResourceType: "spec",
		ResourceID:   "spec-1",
		Timestamp:    time.Now().UTC(),
		Metadata: map[string]any{
			"saga_id": "spec-1",
			"title":   "Checkout refactor",
			"status":  "shipped",
		},
	}
	if err := ValidateEventPayload(EventSagaSpecShippingCompleted, data); err != nil {
		t.Fatalf("expected OK, got: %v", err)
	}
}

func TestSecurityFindingCreated_RejectsMissingFindingID(t *testing.T) {
	data := EventData{
		ResourceType: "finding",
		ResourceID:   "f-1",
		Timestamp:    time.Now().UTC(),
		Metadata: map[string]any{
			"severity": "high",
			"title":    "SQL injection",
		},
	}
	if err := ValidateEventPayload(EventSecurityFindingCreated, data); err == nil {
		t.Fatal("expected validation to fail without metadata.finding_id")
	}
}

func TestFoundryLLMAudit_RejectsMissingOrg(t *testing.T) {
	data := EventData{
		ResourceType: "llm_audit",
		ResourceID:   "a-1",
		Timestamp:    time.Now().UTC(),
		Metadata: map[string]any{
			"provider": "anthropic",
			"model":    "claude-opus-5",
		},
	}
	if err := ValidateEventPayload(EventFoundryLLMAudit, data); err == nil {
		t.Fatal("expected validation to fail without metadata.organization_id")
	}
}

// TestUnpublishedSecurityTypes_StayUnregistered proves the registration is an
// allowlist of types with a live publisher — not a security.* wildcard.
func TestUnpublishedSecurityTypes_StayUnregistered(t *testing.T) {
	for _, typ := range []string{
		"security.dast.vulnerability_found",
		"security.dast.scan_completed",
		"security.discovery.endpoints_found",
		"security.compliance.report",
		"saga.spec_shipping.started",
	} {
		if _, ok := LookupEvent(typ); ok {
			t.Errorf("event %q is registered but has no publisher", typ)
		}
	}
}
