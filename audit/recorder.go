// Package audit provides the shared admin-action audit recorder used by
// every Sentiae service. It writes a durable per-service log row via the
// supplied GORM DB and simultaneously publishes a CloudEvent on the
// Kafka topic `sentiae.admin.action.recorded` so a central aggregator
// (ops-service) can fan-in into a cross-service view for platform
// admins. Closes FEATURES.md §18.1 "Audit log — every admin action is
// recorded" and §18.2 global admin telemetry.
//
// Typical wiring:
//
//	recorder := audit.NewRecorder(db, publisher, "identity-service")
//	_ = recorder.Record(ctx, audit.ActorRef{Type: audit.ActorUser, ID: actorID.String()},
//	    "user.suspended",
//	    audit.TargetRef{Type: "user", ID: targetID.String(), OrganizationID: orgID.String()},
//	    map[string]any{"reason": "spam"},
//	)
//
// The recorder is safe for concurrent use. On DB error it still
// attempts to publish; on publish error it still reports the persisted
// write as success because the durable row is the source of truth.
package audit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/sentiae/platform-kit/kafka"
)

// ActorType enumerates the kinds of actor that can perform an admin
// action. Matches the vocabulary used by per-service audit_trail_entries
// tables so rows are easy to merge on the central aggregator.
type ActorType string

const (
	// ActorUser is a human user (platform admin, support staff, org
	// admin performing admin actions in their own workspace).
	ActorUser ActorType = "user"
	// ActorAgent is an autonomous agent (e.g. Eve) acting on a user's
	// behalf with a delegated token.
	ActorAgent ActorType = "agent"
	// ActorService is another Sentiae service making a cross-service
	// admin call.
	ActorService ActorType = "service"
	// ActorSystem is an automated scheduler / cron without a human
	// principal (e.g. credential rotation monitor).
	ActorSystem ActorType = "system"
)

// ActorRef identifies who performed an admin action.
type ActorRef struct {
	// Type classifies the actor. Required.
	Type ActorType
	// ID is the canonical identifier for the actor (user UUID, agent
	// ID, service name). Required.
	ID string
	// Email is an optional denormalized convenience so admin consoles
	// can show a readable label without joining identity-service.
	Email string
	// IPAddress of the request, if known.
	IPAddress string
	// UserAgent of the request, if known.
	UserAgent string
}

// TargetRef identifies what the admin action was performed on.
type TargetRef struct {
	// Type is a short resource kind (e.g. "user", "team",
	// "feature_flag", "organization"). Required.
	Type string
	// ID is the canonical identifier of the target. Required.
	ID string
	// OrganizationID scopes the target to an org when applicable.
	// Empty for platform-wide actions (e.g. a platform feature flag
	// toggle that isn't org-scoped).
	OrganizationID string
}

// Event is the exported representation of a recorded action, persisted
// to the per-service admin_audit_log table and emitted on Kafka.
type Event struct {
	// ID is a UUIDv4 for the event.
	ID uuid.UUID `json:"id" gorm:"type:uuid;primary_key"`
	// Service is the emitting service name (e.g. "identity-service").
	// Indexed on the aggregator so UI can filter "show me everything
	// from identity".
	Service string `json:"service" gorm:"type:varchar(64);not null;index"`
	// ActorType / ActorID / ActorEmail / IPAddress / UserAgent mirror
	// ActorRef.
	ActorType  ActorType `json:"actor_type" gorm:"type:varchar(20);not null;index"`
	ActorID    string    `json:"actor_id" gorm:"type:varchar(128);not null;index"`
	ActorEmail string    `json:"actor_email,omitempty" gorm:"type:varchar(320)"`
	IPAddress  string    `json:"ip_address,omitempty" gorm:"type:varchar(45)"`
	UserAgent  string    `json:"user_agent,omitempty" gorm:"type:varchar(500)"`
	// Action is a dotted verb like "user.suspended" or
	// "feature_flag.toggled". Required.
	Action string `json:"action" gorm:"type:varchar(128);not null;index"`
	// TargetType / TargetID / OrganizationID mirror TargetRef.
	TargetType     string `json:"target_type" gorm:"type:varchar(64);not null"`
	TargetID       string `json:"target_id" gorm:"type:varchar(255);not null"`
	OrganizationID string `json:"organization_id,omitempty" gorm:"type:varchar(64);index"`
	// Metadata is a free-form JSON blob for action-specific fields
	// (old_value/new_value, reason, etc.).
	Metadata JSONMap `json:"metadata,omitempty" gorm:"type:jsonb"`
	// CreatedAt is the server-assigned time of recording.
	CreatedAt time.Time `json:"created_at" gorm:"not null;index"`
}

// TableName returns the per-service durable table.
func (Event) TableName() string { return "admin_audit_log" }

// CentralEvent mirrors Event but lives in the cross-service
// admin_audit_log_central table written by the aggregator consumer.
type CentralEvent struct {
	Event
}

// TableName returns the aggregated table.
func (CentralEvent) TableName() string { return "admin_audit_log_central" }

// JSONMap is a local alias so callers can populate metadata without
// importing per-service types.
type JSONMap map[string]any

// Recorder is the interface every service wires into its usecases.
type Recorder interface {
	Record(ctx context.Context, actor ActorRef, action string, target TargetRef, metadata map[string]any) error
}

// Topic is the Kafka topic the recorder publishes to. Fixed so the
// aggregator consumer can subscribe without configuration.
const Topic = "sentiae.admin.action.recorded"

// EventType is the CloudEvent type for recorded actions.
const EventType = "admin.action.recorded"

// recorder is the default Recorder implementation.
type recorder struct {
	db      *gorm.DB
	pub     kafka.Publisher
	service string
}

// NewRecorder builds a Recorder that writes to the supplied DB and
// publishes to kafka. Either dependency may be nil: with a nil DB the
// recorder only publishes; with a nil publisher it only persists. At
// least one must be non-nil.
func NewRecorder(db *gorm.DB, pub kafka.Publisher, service string) Recorder {
	if service == "" {
		service = "unknown"
	}
	return &recorder{db: db, pub: pub, service: service}
}

// Record persists and publishes an admin action. Validation errors
// return immediately; persistence or publish errors are returned but
// neither short-circuits the other.
func (r *recorder) Record(ctx context.Context, actor ActorRef, action string, target TargetRef, metadata map[string]any) error {
	if actor.Type == "" || actor.ID == "" {
		return errors.New("audit: actor type and id are required")
	}
	if action == "" {
		return errors.New("audit: action is required")
	}
	if target.Type == "" || target.ID == "" {
		return errors.New("audit: target type and id are required")
	}

	evt := Event{
		ID:             uuid.New(),
		Service:        r.service,
		ActorType:      actor.Type,
		ActorID:        actor.ID,
		ActorEmail:     actor.Email,
		IPAddress:      actor.IPAddress,
		UserAgent:      actor.UserAgent,
		Action:         action,
		TargetType:     target.Type,
		TargetID:       target.ID,
		OrganizationID: target.OrganizationID,
		Metadata:       JSONMap(metadata),
		CreatedAt:      time.Now().UTC(),
	}

	var dbErr, pubErr error
	if r.db != nil {
		dbErr = r.db.WithContext(ctx).Create(&evt).Error
	}
	if r.pub != nil {
		pubErr = r.pub.Publish(ctx, EventType, kafka.EventData{
			ActorID:        actor.ID,
			ActorType:      string(actor.Type),
			ResourceType:   target.Type,
			ResourceID:     target.ID,
			OrganizationID: target.OrganizationID,
			Metadata: map[string]any{
				"event_id":     evt.ID.String(),
				"service":      r.service,
				"action":       action,
				"actor_email":  actor.Email,
				"ip_address":   actor.IPAddress,
				"user_agent":   actor.UserAgent,
				"created_at":   evt.CreatedAt.Format(time.RFC3339Nano),
				"action_meta":  metadata,
			},
			Timestamp: evt.CreatedAt,
		})
	}

	// Surface the first non-nil error to the caller but do not lose the
	// other signal — wrap so tests / observability can see both.
	switch {
	case dbErr != nil && pubErr != nil:
		return fmt.Errorf("audit: persist: %w; publish: %v", dbErr, pubErr)
	case dbErr != nil:
		return fmt.Errorf("audit: persist: %w", dbErr)
	case pubErr != nil:
		return fmt.Errorf("audit: publish: %w", pubErr)
	}
	return nil
}
