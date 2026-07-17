package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	pkkafka "github.com/sentiae/platform-kit/kafka"
)

// defaultMaxRetries is the per-row retry ceiling when neither the message nor
// an Option overrides it.
const defaultMaxRetries = 10

// outboxModel is the GORM model for the outbox_messages table. It carries the
// status ladder, per-row max_retries, and next_retry_at that the drain gates on.
type outboxModel struct {
	ID          uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Topic       string     `gorm:"not null"`
	Key         string     `gorm:"not null"`
	Payload     []byte     `gorm:"type:jsonb;not null"`
	Headers     []byte     `gorm:"type:jsonb"`
	Status      string     `gorm:"not null;default:'pending';index:outbox_due_idx,priority:1"`
	Attempts    int        `gorm:"not null;default:0"`
	MaxRetries  int        `gorm:"column:max_retries;not null;default:10"`
	NextRetryAt *time.Time `gorm:"column:next_retry_at;index:outbox_due_idx,priority:2"`
	LastError   string     `gorm:"column:last_error"`
	CreatedAt   time.Time  `gorm:"not null;default:now()"`
	SentAt      *time.Time `gorm:"column:sent_at"`
}

// TableName pins the model to the constitution's outbox_messages table (§19).
func (outboxModel) TableName() string { return "outbox_messages" }

// GormRepo is the GORM-backed Repo (and therefore Writer). It is the single
// concrete type an adopting service wires.
type GormRepo struct {
	db                *gorm.DB
	dbFromCtx         func(context.Context, *gorm.DB) *gorm.DB
	defaultMaxRetries int
}

var _ Repo = (*GormRepo)(nil)

// Option configures a GormRepo.
type Option func(*GormRepo)

// WithDefaultMaxRetries overrides the per-row retry ceiling used when a Message
// does not set its own MaxRetries. Non-positive values are ignored.
func WithDefaultMaxRetries(n int) Option {
	return func(r *GormRepo) {
		if n > 0 {
			r.defaultMaxRetries = n
		}
	}
}

// NewRepo builds the outbox repository. dbFromCtx is the ADOPTING service's own
// transaction resolver (its dbFrom / DBFromCtx): it returns the tx on the
// context when the caller is inside one, else the fallback DB. The shared
// package cannot know a service's private tx-context key, so the adopter passes
// its resolver — that is how Append joins the caller's business transaction. A
// nil resolver defaults to a plain context-bound DB (no tx participation).
func NewRepo(db *gorm.DB, dbFromCtx func(context.Context, *gorm.DB) *gorm.DB, opts ...Option) *GormRepo {
	r := &GormRepo{
		db:                db,
		dbFromCtx:         dbFromCtx,
		defaultMaxRetries: defaultMaxRetries,
	}
	if r.dbFromCtx == nil {
		r.dbFromCtx = func(ctx context.Context, fallback *gorm.DB) *gorm.DB {
			return fallback.WithContext(ctx)
		}
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Append performs VALIDATE-AT-APPEND (the D-162b keystone) and inserts the row
// through the caller's transaction (via dbFromCtx) so it commits atomically with
// the domain write. Validation runs BEFORE the insert and touches no database:
// an invalid event type or a schema-invalid payload returns an error, so the
// caller's business transaction rolls back and the bad event never persists.
func (r *GormRepo) Append(ctx context.Context, msg Message) error {
	if err := pkkafka.ValidateEventType(msg.Topic); err != nil {
		return fmt.Errorf("outbox append: invalid event type: %w", err)
	}
	var data pkkafka.EventData
	if err := json.Unmarshal(msg.Payload, &data); err != nil {
		return fmt.Errorf("outbox append: decode payload for %q: %w", msg.Topic, err)
	}
	if err := pkkafka.ValidateEventPayload(msg.Topic, data); err != nil {
		return fmt.Errorf("outbox append: %w", err)
	}

	maxRetries := msg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = r.defaultMaxRetries
	}

	m := outboxModel{
		Topic:      msg.Topic,
		Key:        msg.Key,
		Payload:    msg.Payload,
		Status:     string(StatusPending),
		Attempts:   0,
		MaxRetries: maxRetries,
	}
	if len(msg.Headers) > 0 {
		hdrs, err := json.Marshal(msg.Headers)
		if err != nil {
			return fmt.Errorf("outbox append: marshal headers: %w", err)
		}
		m.Headers = hdrs
	}
	return r.dbFromCtx(ctx, r.db).Create(&m).Error
}

// FetchDue claims up to limit due rows FOR UPDATE SKIP LOCKED, oldest-first.
// This must run inside the drain's own transaction so the row locks hold across
// the subsequent MarkSending in the same drain cycle.
func (r *GormRepo) FetchDue(ctx context.Context, limit int) ([]Row, error) {
	var models []outboxModel
	err := r.dbFromCtx(ctx, r.db).
		Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)", string(StatusPending), time.Now().UTC()).
		Order("created_at ASC").
		Limit(limit).
		Find(&models).Error
	if err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(models))
	for i := range models {
		row, err := toRow(&models[i])
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// MarkSending flips the row to StatusSending for the current publish attempt.
func (r *GormRepo) MarkSending(ctx context.Context, id uuid.UUID) error {
	return r.dbFromCtx(ctx, r.db).
		Model(&outboxModel{}).
		Where("id = ?", id).
		Update("status", string(StatusSending)).Error
}

// MarkSent flips the row to StatusSent, stamps sent_at, and clears any prior
// error.
func (r *GormRepo) MarkSent(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	return r.dbFromCtx(ctx, r.db).
		Model(&outboxModel{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":     string(StatusSent),
			"sent_at":    now,
			"last_error": "",
		}).Error
}

// MarkFailed records the failed attempt. dead=true moves the row to StatusDead
// (retries exhausted); otherwise it returns to StatusPending with next_retry_at
// set for the backoff window.
func (r *GormRepo) MarkFailed(ctx context.Context, id uuid.UUID, attempts int, nextRetryAt time.Time, lastErr string, dead bool) error {
	updates := map[string]any{
		"attempts":   attempts,
		"last_error": lastErr,
	}
	if dead {
		updates["status"] = string(StatusDead)
	} else {
		updates["status"] = string(StatusPending)
		updates["next_retry_at"] = nextRetryAt
	}
	return r.dbFromCtx(ctx, r.db).
		Model(&outboxModel{}).
		Where("id = ?", id).
		Updates(updates).Error
}

// CountByStatus returns a per-status row count.
func (r *GormRepo) CountByStatus(ctx context.Context) (map[Status]int, error) {
	var rows []struct {
		Status string
		Count  int
	}
	err := r.dbFromCtx(ctx, r.db).
		Model(&outboxModel{}).
		Select("status, count(*) as count").
		Group("status").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[Status]int, len(rows))
	for _, row := range rows {
		out[Status(row.Status)] = row.Count
	}
	return out, nil
}

// toRow maps a GORM model to a Row, decoding the JSON headers.
func toRow(m *outboxModel) (Row, error) {
	row := Row{
		ID:          m.ID,
		Topic:       m.Topic,
		Key:         m.Key,
		Payload:     m.Payload,
		Status:      Status(m.Status),
		Attempts:    m.Attempts,
		MaxRetries:  m.MaxRetries,
		NextRetryAt: m.NextRetryAt,
		LastError:   m.LastError,
		CreatedAt:   m.CreatedAt,
		SentAt:      m.SentAt,
	}
	if len(m.Headers) > 0 {
		if err := json.Unmarshal(m.Headers, &row.Headers); err != nil {
			return Row{}, fmt.Errorf("outbox: decode headers for %s: %w", m.ID, err)
		}
	}
	return row, nil
}
