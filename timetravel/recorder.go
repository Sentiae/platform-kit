// Package timetravel is the platform-wide entity snapshot recorder
// that feeds §13.1 point-in-time state queries.
//
// Every domain service owning a mutable entity (feature, spec,
// deployment, incident, repository, session, agent, proposal, ...)
// calls Recorder.RecordEntity on every successful create/update/delete.
// The implementations decide whether to write synchronously to a local
// entity_snapshots table (GORMRecorder) or to publish a Kafka event
// that a dedicated consumer persists out-of-band (KafkaRecorder).
//
// The table is intentionally generic: (kind, id, payload jsonb,
// valid_from, valid_to, writer_service). It coexists with the richer
// ops-service time_travel_snapshots table which is org-scoped and
// enriched with ChangedBy/ChangeReason — the new table is the shared
// cross-service ledger that lets §13.1 compare "any entity at time T".
//
// Failures never surface to callers: time-travel is a best-effort
// observability mechanism, not a gate on the write path. Recorders log
// and move on.
package timetravel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Recorder is the sole contract domain services depend on. A nil
// Recorder is safe to call — use NoopRecorder or check the receiver
// in NewXxxUseCase wiring when the feature is disabled.
type Recorder interface {
	// RecordEntity captures the current state of an entity of the
	// given kind. id is the entity's primary key (UUID, string, or
	// int rendered as string). payload is marshaled to JSON; any
	// marshalable shape is acceptable.
	RecordEntity(ctx context.Context, kind, id string, payload any) error
}

// EntitySnapshot is the canonical row shape persisted by GORMRecorder.
// The receiver services autoMigrate this struct into their local DB.
//
// ChangedBy + ChangeReason feed §13.4 time-based permission audits: every
// mutation carries the acting user id and a free-text reason. Background
// workers and migrations pass uuid.Nil + "system" — the fields must be
// non-null so queries like "who edited this spec on T?" never return
// ambiguous holes. Callers thread the values through context with
// WithChangeContext; RecordEntity reads them via ChangeContextFromContext.
type EntitySnapshot struct {
	ID            uuid.UUID  `json:"id"             gorm:"column:id;type:uuid;primaryKey"`
	Kind          string     `json:"kind"           gorm:"column:kind;type:varchar(64);not null;index:idx_entity_snapshots_kind_id"`
	EntityID      string     `json:"entity_id"      gorm:"column:entity_id;type:varchar(128);not null;index:idx_entity_snapshots_kind_id"`
	Payload       []byte     `json:"payload"        gorm:"column:payload;type:jsonb;not null"`
	ValidFrom     time.Time  `json:"valid_from"     gorm:"column:valid_from;not null;index"`
	ValidTo       *time.Time `json:"valid_to,omitempty" gorm:"column:valid_to;index"`
	WriterService string     `json:"writer_service" gorm:"column:writer_service;type:varchar(64);not null;index"`
	ChangedBy     uuid.UUID  `json:"changed_by"     gorm:"column:changed_by;type:uuid;not null;default:'00000000-0000-0000-0000-000000000000';index"`
	ChangeReason  string     `json:"change_reason"  gorm:"column:change_reason;type:varchar(255);not null;default:'system'"`
	CreatedAt     time.Time  `json:"created_at"     gorm:"column:created_at;not null"`
}

// TableName binds the GORM model to the shared table name so every
// service writes into the same schema.
func (EntitySnapshot) TableName() string { return "entity_snapshots" }

// --- Change context ---------------------------------------------------------

// ChangeContext captures the acting user id and the human-facing reason
// behind a mutation. Call sites thread this through context so every
// downstream RecordEntity call can stamp the snapshot row without a
// wider signature change. Reason defaults to "system" when empty.
type ChangeContext struct {
	ChangedBy uuid.UUID
	Reason    string
}

type changeCtxKey struct{}

// TimeTravelChangeContext is the canonical context key. Call sites can
// also use WithChangeContext which hides the key type entirely.
var TimeTravelChangeContext changeCtxKey

// WithChangeContext returns a derived context carrying the given
// ChangeContext. Passing uuid.Nil + empty reason is valid and is
// interpreted downstream as a system-driven change.
func WithChangeContext(ctx context.Context, cc ChangeContext) context.Context {
	return context.WithValue(ctx, TimeTravelChangeContext, cc)
}

// ChangeContextFromContext extracts the ChangeContext threaded through
// ctx, falling back to a zero-value system change when no context was
// installed. Reason is normalised to "system" for system-driven paths
// so the DB never stores empty strings.
func ChangeContextFromContext(ctx context.Context) ChangeContext {
	if ctx == nil {
		return ChangeContext{Reason: "system"}
	}
	if v, ok := ctx.Value(TimeTravelChangeContext).(ChangeContext); ok {
		if v.Reason == "" {
			v.Reason = "system"
		}
		return v
	}
	return ChangeContext{Reason: "system"}
}

// --- NoopRecorder -----------------------------------------------------------

// NoopRecorder is the safe default when no recorder is configured.
// It drops every call silently and is suitable for tests.
type NoopRecorder struct{}

// RecordEntity is a no-op.
func (NoopRecorder) RecordEntity(context.Context, string, string, any) error { return nil }

// --- GORMRecorder -----------------------------------------------------------

// GORMRecorder persists snapshots into a shared entity_snapshots table
// via GORM. The writerService label lets downstream queries distinguish
// which service produced a given row.
type GORMRecorder struct {
	db            *gorm.DB
	writerService string
	logger        *slog.Logger
}

// NewGORMRecorder builds a GORMRecorder. writerService should match the
// service binary name (e.g. "work-service") so queries can filter on
// producer.
func NewGORMRecorder(db *gorm.DB, writerService string, logger *slog.Logger) *GORMRecorder {
	if logger == nil {
		logger = slog.Default()
	}
	if writerService == "" {
		writerService = "unknown-service"
	}
	return &GORMRecorder{db: db, writerService: writerService, logger: logger}
}

// RecordEntity serializes payload, closes the previous open snapshot
// for (kind,id), and inserts a new row. Errors are returned so callers
// can alert, but the existing RecordEntity pattern in ops-service is to
// swallow them — pick the policy at the call site.
func (r *GORMRecorder) RecordEntity(ctx context.Context, kind, id string, payload any) error {
	if r == nil || r.db == nil {
		return errors.New("gorm recorder not configured")
	}
	if kind == "" || id == "" {
		return errors.New("kind and id are required")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	now := time.Now().UTC()

	// Close the previous open snapshot's validity window.
	if err := r.db.WithContext(ctx).
		Model(&EntitySnapshot{}).
		Where("kind = ? AND entity_id = ? AND valid_to IS NULL", kind, id).
		Update("valid_to", now).Error; err != nil {
		r.logger.Warn("entity_snapshots close previous failed",
			"error", err, "kind", kind, "id", id)
		// Do not abort — insertion of the new row is the more important
		// half; a missed close can be reconciled by a background sweeper.
	}

	cc := ChangeContextFromContext(ctx)
	row := &EntitySnapshot{
		ID:            uuid.New(),
		Kind:          kind,
		EntityID:      id,
		Payload:       data,
		ValidFrom:     now,
		WriterService: r.writerService,
		ChangedBy:     cc.ChangedBy,
		ChangeReason:  cc.Reason,
		CreatedAt:     now,
	}
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		r.logger.Warn("entity_snapshots insert failed",
			"error", err, "kind", kind, "id", id)
		return fmt.Errorf("insert snapshot: %w", err)
	}
	return nil
}

// AutoMigrate ensures the backing table exists. Call from each
// service's runAutoMigrations alongside its domain models.
func AutoMigrate(db *gorm.DB) error {
	if db == nil {
		return errors.New("db is nil")
	}
	return db.AutoMigrate(&EntitySnapshot{})
}
