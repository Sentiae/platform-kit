package kafka

import (
	"encoding/json"
	"testing"
	"time"
)

func TestValidateEventType(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		wantErr   bool
	}{
		{"valid three-part", "identity.user.registered", false},
		{"valid with underscore", "identity.user.password_changed", false},
		{"valid with numbers", "identity.user2.registered", false},
		{"valid four parts", "canvas.debug.session.created", false},
		{"valid five parts", "canvas.marketplace.publisher.connected", false},
		{"missing action", "identity.user", true},
		{"empty", "", true},
		{"uppercase", "Identity.User.Registered", true},
		{"starts with number", "1identity.user.registered", true},
		{"has spaces", "identity.user .registered", true},
		{"has hyphen", "identity.user-profile.registered", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEventType(tt.eventType)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateEventType(%q) error = %v, wantErr %v", tt.eventType, err, tt.wantErr)
			}
		})
	}
}

func TestTopicFromEventType(t *testing.T) {
	tests := []struct {
		name      string
		prefix    string
		eventType string
		want      string
	}{
		{
			name:      "standard three-part",
			prefix:    "sentiae",
			eventType: "identity.user.registered",
			want:      "sentiae.identity.user",
		},
		{
			name:      "with underscore action",
			prefix:    "sentiae",
			eventType: "identity.user.password_changed",
			want:      "sentiae.identity.user",
		},
		{
			name:      "work domain",
			prefix:    "sentiae",
			eventType: "work.item.created",
			want:      "sentiae.work.item",
		},
		{
			name:      "custom prefix",
			prefix:    "myapp",
			eventType: "auth.session.created",
			want:      "myapp.auth.session",
		},
		{
			name:      "four-part event type",
			prefix:    "sentiae",
			eventType: "canvas.debug.session.created",
			want:      "sentiae.canvas.debug",
		},
		{
			name:      "five-part event type",
			prefix:    "sentiae",
			eventType: "canvas.marketplace.publisher.connected",
			want:      "sentiae.canvas.marketplace",
		},
		{
			// Legacy constants carry the prefix; it must be stripped so the
			// topic is not doubled ("sentiae.sentiae.git").
			name:      "event type already carrying the prefix",
			prefix:    "sentiae",
			eventType: "sentiae.git.commit.created",
			want:      "sentiae.git.commit",
		},
		{
			name:      "prefixed three-part event type",
			prefix:    "sentiae",
			eventType: "sentiae.git.push",
			want:      "sentiae.git.push",
		},
		{
			name:      "prefixed under a custom prefix",
			prefix:    "myapp",
			eventType: "myapp.auth.session.created",
			want:      "myapp.auth.session",
		},
		{
			// A type carrying a DIFFERENT product's prefix is untouched.
			name:      "leading segment is not the configured prefix",
			prefix:    "myapp",
			eventType: "sentiae.git.commit.created",
			want:      "myapp.sentiae.git",
		},
		{
			// Only a LEADING occurrence is stripped — mid-string is real data.
			name:      "prefix appears mid-string",
			prefix:    "sentiae",
			eventType: "ops.sentiae.audit.recorded",
			want:      "sentiae.ops.sentiae",
		},
		{
			// Stripping an already-stripped type is a no-op.
			name:      "idempotent on unprefixed type",
			prefix:    "sentiae",
			eventType: "git.commit.created",
			want:      "sentiae.git.commit",
		},
		{
			name:      "single-segment event type",
			prefix:    "sentiae",
			eventType: "heartbeat",
			want:      "sentiae.heartbeat",
		},
		{
			name:      "single-segment event type equal to the prefix",
			prefix:    "sentiae",
			eventType: "sentiae",
			want:      "sentiae.sentiae",
		},
		{
			name:      "empty prefix leaves the type untouched",
			prefix:    "",
			eventType: ".git.commit.created",
			want:      "..git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := topicFromEventType(tt.prefix, tt.eventType)
			if got != tt.want {
				t.Errorf("topicFromEventType(%q, %q) = %q, want %q", tt.prefix, tt.eventType, got, tt.want)
			}
		})
	}
}

// TestTopicFromEventTypeIdempotent asserts that feeding a derived topic back
// through the derivation yields the same topic — the prefix is never doubled,
// however many times normalisation runs.
func TestTopicFromEventTypeIdempotent(t *testing.T) {
	for _, eventType := range []string{
		"identity.user.registered",
		"sentiae.git.commit.created",
		"sentiae.git.push",
		"sentiae.timetravel.entity.changed",
	} {
		t.Run(eventType, func(t *testing.T) {
			once := topicFromEventType("sentiae", eventType)
			twice := topicFromEventType("sentiae", once)
			if once != twice {
				t.Errorf("not idempotent: %q → %q → %q", eventType, once, twice)
			}
		})
	}
}

func TestSplitEventParts(t *testing.T) {
	tests := []struct {
		eventType    string
		wantDomain   string
		wantResource string
	}{
		{"identity.user.registered", "identity", "user"},
		{"work.item.created", "work", "item"},
		{"singleword", "singleword", ""},
		{"domain.resource", "domain", "resource"},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			d, r := splitEventParts(tt.eventType)
			if d != tt.wantDomain || r != tt.wantResource {
				t.Errorf("splitEventParts(%q) = (%q, %q), want (%q, %q)",
					tt.eventType, d, r, tt.wantDomain, tt.wantResource)
			}
		})
	}
}

func TestNewCloudEvent(t *testing.T) {
	data := EventData{
		ActorID:      "user-123",
		ResourceType: "user",
		ResourceID:   "res-456",
		Timestamp:    time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}

	ce, payload, err := newCloudEvent("test-service", "identity.user.registered", data, "")
	if err != nil {
		t.Fatalf("newCloudEvent() error = %v", err)
	}

	if ce.SpecVersion != "1.0" {
		t.Errorf("SpecVersion = %q, want %q", ce.SpecVersion, "1.0")
	}
	if ce.Source != "test-service" {
		t.Errorf("Source = %q, want %q", ce.Source, "test-service")
	}
	if ce.Type != "identity.user.registered" {
		t.Errorf("Type = %q, want %q", ce.Type, "identity.user.registered")
	}
	if ce.DataContentType != "application/json" {
		t.Errorf("DataContentType = %q, want %q", ce.DataContentType, "application/json")
	}
	if ce.Subject != "user/res-456" {
		t.Errorf("Subject = %q, want %q", ce.Subject, "user/res-456")
	}
	if ce.ID == "" {
		t.Error("ID should not be empty")
	}
	if ce.IdempotencyKey == "" {
		t.Error("IdempotencyKey should be auto-generated when not provided")
	}
	if ce.Time != "2026-03-01T12:00:00Z" {
		t.Errorf("Time = %q, want %q", ce.Time, "2026-03-01T12:00:00Z")
	}

	// Verify payload is valid JSON
	var decoded CloudEvent
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if decoded.Type != "identity.user.registered" {
		t.Errorf("decoded Type = %q", decoded.Type)
	}

	// Verify Data can be decoded back to EventData
	var decodedData EventData
	if err := json.Unmarshal(decoded.Data, &decodedData); err != nil {
		t.Fatalf("failed to decode data: %v", err)
	}
	if decodedData.ActorID != "user-123" {
		t.Errorf("decoded ActorID = %q, want %q", decodedData.ActorID, "user-123")
	}
}

func TestNewCloudEventWithIdempotencyKey(t *testing.T) {
	data := EventData{
		ResourceType: "user",
		ResourceID:   "res-456",
	}

	ce, _, err := newCloudEvent("svc", "identity.user.created", data, "my-custom-key")
	if err != nil {
		t.Fatalf("newCloudEvent() error = %v", err)
	}
	if ce.IdempotencyKey != "my-custom-key" {
		t.Errorf("IdempotencyKey = %q, want %q", ce.IdempotencyKey, "my-custom-key")
	}
}

func TestNewCloudEventDefaultTimestamp(t *testing.T) {
	data := EventData{
		ResourceType: "user",
		ResourceID:   "res-456",
		// Timestamp is zero — should be auto-set
	}

	before := time.Now().UTC().Truncate(time.Second)
	ce, _, err := newCloudEvent("svc", "identity.user.created", data, "")
	if err != nil {
		t.Fatalf("newCloudEvent() error = %v", err)
	}
	after := time.Now().UTC().Truncate(time.Second).Add(time.Second)

	parsed, err := time.Parse(time.RFC3339, ce.Time)
	if err != nil {
		t.Fatalf("failed to parse time %q: %v", ce.Time, err)
	}
	if parsed.Before(before) || parsed.After(after) {
		t.Errorf("auto-set Time %v not in range [%v, %v]", parsed, before, after)
	}
}
