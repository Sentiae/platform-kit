package nudgeevents

import (
	"time"

	"github.com/google/uuid"
)

// EventType is the CloudEvent type field. One per state transition.
type EventType string

const (
	TypeDelivered  EventType = "foundry.nudge.delivered"
	TypeSeen       EventType = "foundry.nudge.seen"
	TypeActed      EventType = "foundry.nudge.acted"
	TypeDismissed  EventType = "foundry.nudge.dismissed"
	TypeSuppressed EventType = "foundry.nudge.suppressed"
	TypeExpired    EventType = "foundry.nudge.expired"
	TypeSuperseded EventType = "foundry.nudge.superseded"
)

// AllTypes returns every nudge event type. Used for bulk schema registration.
func AllTypes() []EventType {
	return []EventType{
		TypeDelivered,
		TypeSeen,
		TypeActed,
		TypeDismissed,
		TypeSuppressed,
		TypeExpired,
		TypeSuperseded,
	}
}

// Surface is where the nudge is shown to the user.
type Surface string

const (
	SurfaceBubble Surface = "bubble"
	SurfaceBadge  Surface = "badge"
	SurfaceInbox  Surface = "inbox"
)

// Base carries fields shared across every nudge event payload. Concrete event
// payloads embed Base.
type Base struct {
	NudgeID        uuid.UUID  `json:"nudge_id"`
	RuleID         string     `json:"rule_id"`
	UserID         uuid.UUID  `json:"user_id"`
	OrgID          uuid.UUID  `json:"org_id"`
	ProductID      *uuid.UUID `json:"product_id,omitempty"`
	BundleID       *uuid.UUID `json:"bundle_id,omitempty"`
	GroupKey       string     `json:"group_key"`
	Priority       int        `json:"priority"`
	Surface        Surface    `json:"surface"`
	TransitionedAt time.Time  `json:"transitioned_at"`
}

// DeliveredData is emitted when a nudge transitions pending → delivered.
type DeliveredData struct {
	Base
	ConversationID uuid.UUID `json:"conversation_id"`
	MessageID      uuid.UUID `json:"message_id"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// SeenData is emitted when frontend acknowledges render.
type SeenData struct {
	Base
}

// ActedData is emitted when user clicks a chip.
type ActedData struct {
	Base
	ChipID     string         `json:"chip_id"`
	ChipParams map[string]any `json:"chip_params,omitempty"`
}

// DismissedData is emitted when user dismisses or auto-dismiss timer fires.
type DismissedData struct {
	Base
	Reason string `json:"reason"` // "user", "auto_dismiss", "timeout"
}

// SuppressedData is emitted when budget / DND / group exclusion blocks delivery.
type SuppressedData struct {
	Base
	Reason string `json:"reason"` // "budget_exhausted", "dnd", "scope_cooldown", "engagement_decay"
}

// ExpiredData is emitted when expires_at passes before delivery / action.
type ExpiredData struct {
	Base
}

// SupersededData is emitted when bundle resolution replaces this nudge.
type SupersededData struct {
	Base
	SupersededByBundleID uuid.UUID `json:"superseded_by_bundle_id"`
}
