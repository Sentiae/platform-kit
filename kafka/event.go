// Package kafka provides CloudEvent-based Kafka publishing and consuming.
//
// Events follow CloudEvents 1.0 spec and use {domain}.{resource}.{action}
// naming (e.g., "identity.user.registered").
package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// eventNameRe validates {domain}.{resource}.{action} format.
var eventNameRe = regexp.MustCompile(`^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// ValidateEventType checks that eventType follows {domain}.{resource}.{action}.
func ValidateEventType(eventType string) error {
	if !eventNameRe.MatchString(eventType) {
		return fmt.Errorf("invalid event type %q: must match {domain}.{resource}.{action} (lowercase, e.g. identity.user.registered)", eventType)
	}
	return nil
}

// CloudEvent represents a CloudEvents 1.0 specification event.
type CloudEvent struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	Subject         string          `json:"subject,omitempty"`
	IdempotencyKey  string          `json:"idempotencykey,omitempty"`
	Data            json.RawMessage `json:"data"`
}

// EventData is the standard data payload embedded in CloudEvents.
type EventData struct {
	ActorID        string         `json:"actor_id"`
	ActorType      string         `json:"actor_type,omitempty"`
	ResourceType   string         `json:"resource_type"`
	ResourceID     string         `json:"resource_id"`
	OrganizationID string         `json:"organization_id,omitempty"`
	TeamID         string         `json:"team_id,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Timestamp      time.Time      `json:"timestamp"`
}

// Event pairs an event type with its data for batch publishing.
type Event struct {
	Type           string
	Data           EventData
	IdempotencyKey string // optional caller-supplied dedup key
}

// Publisher publishes events to Kafka.
type Publisher interface {
	Publish(ctx context.Context, eventType string, data EventData) error
	PublishBatch(ctx context.Context, events []Event) error
	Close() error
}

// Consumer consumes events from Kafka.
type Consumer interface {
	Subscribe(eventType string, handler EventHandler)
	Start(ctx context.Context) error
	Close() error
}

// EventHandler processes received CloudEvents.
type EventHandler func(ctx context.Context, event CloudEvent) error

// newCloudEvent builds a CloudEvent from a type and data.
func newCloudEvent(source, eventType string, data EventData, idempotencyKey string) (CloudEvent, []byte, error) {
	if data.Timestamp.IsZero() {
		data.Timestamp = time.Now().UTC()
	}

	rawData, err := json.Marshal(data)
	if err != nil {
		return CloudEvent{}, nil, fmt.Errorf("marshal event data: %w", err)
	}

	if idempotencyKey == "" {
		idempotencyKey = uuid.New().String()
	}

	ce := CloudEvent{
		SpecVersion:     "1.0",
		ID:              uuid.New().String(),
		Source:          source,
		Type:            eventType,
		Time:            data.Timestamp.Format(time.RFC3339),
		DataContentType: "application/json",
		Subject:         fmt.Sprintf("%s/%s", data.ResourceType, data.ResourceID),
		IdempotencyKey:  idempotencyKey,
		Data:            rawData,
	}

	payload, err := json.Marshal(ce)
	if err != nil {
		return CloudEvent{}, nil, fmt.Errorf("marshal cloud event: %w", err)
	}

	return ce, payload, nil
}

// topicFromEventType derives a Kafka topic from an event type.
// "identity.user.registered" with prefix "sentiae" → "sentiae.identity.user"
func topicFromEventType(prefix, eventType string) string {
	domain, resource := splitEventParts(eventType)
	if resource != "" {
		return fmt.Sprintf("%s.%s.%s", prefix, domain, resource)
	}
	return fmt.Sprintf("%s.%s", prefix, eventType)
}

// splitEventParts extracts domain and resource from "domain.resource.action".
func splitEventParts(eventType string) (domain, resource string) {
	first := -1
	second := -1
	for i, c := range eventType {
		if c == '.' {
			if first < 0 {
				first = i
			} else {
				second = i
				break
			}
		}
	}
	if first < 0 {
		return eventType, ""
	}
	domain = eventType[:first]
	if second < 0 {
		return domain, eventType[first+1:]
	}
	return domain, eventType[first+1 : second]
}
