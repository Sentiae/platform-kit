package outbox

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Status is the lifecycle state of an outbox row. The ladder is
// pending → sending → sent, with a terminal dead state once a row exhausts its
// per-row max_retries.
type Status string

const (
	// StatusPending is the initial state: the row is queued and (once its
	// next_retry_at, if any, has elapsed) due for publication.
	StatusPending Status = "pending"
	// StatusSending marks a row the drainer has claimed for the current
	// publish attempt. A crash between sending and sent re-publishes on the
	// next tick (at-least-once); consumers must be idempotent.
	StatusSending Status = "sending"
	// StatusSent marks a row successfully published to Kafka.
	StatusSent Status = "sent"
	// StatusDead marks a row that exhausted its max_retries. It stays in the
	// table for operator inspection and is never re-published.
	StatusDead Status = "dead"
)

// Message is the append input: the event queued for publication. Topic holds
// the EVENT TYPE (e.g. "identity.user.registered"), NOT the Kafka topic — the
// drain hands it to the real publisher, which derives the wire topic. This
// matches the fleet convention (registry/git/codegen outboxes).
type Message struct {
	Topic   string            // event type, validated at Append against the taxonomy
	Key     string            // partition key (typically the resource id)
	Payload []byte            // CloudEvent data payload as JSON (a marshalled kafka.EventData)
	Headers map[string]string // optional per-message headers (e.g. idempotency_key)
	// MaxRetries caps publish attempts for THIS row. Zero means use the repo
	// default (WithDefaultMaxRetries, default 10).
	MaxRetries int
}

// Row is a fully persisted outbox row as read by the drain worker.
type Row struct {
	ID          uuid.UUID
	Topic       string
	Key         string
	Payload     []byte
	Headers     map[string]string
	Status      Status
	Attempts    int
	MaxRetries  int
	NextRetryAt *time.Time
	LastError   string
	CreatedAt   time.Time
	SentAt      *time.Time
}

// Writer appends an outbox row. Implementations MUST use the transaction
// already on the context so the row commits atomically with the originating
// domain write (CLAUDE.md §19); a direct kafka.Publish after commit is
// forbidden (§30.9).
type Writer interface {
	Append(ctx context.Context, msg Message) error
}

// Repo is the full persistence surface: the Writer plus the drain-side methods.
// One concrete type (GormRepo) implements both, so a service wires a single
// dependency.
type Repo interface {
	Writer

	// FetchDue returns up to limit rows that are pending and due
	// (next_retry_at is null or has elapsed), oldest-first, locked
	// FOR UPDATE SKIP LOCKED so concurrent drainers do not double-claim.
	FetchDue(ctx context.Context, limit int) ([]Row, error)
	// MarkSending flips a row to StatusSending for the current attempt.
	MarkSending(ctx context.Context, id uuid.UUID) error
	// MarkSent flips a row to StatusSent and stamps sent_at.
	MarkSent(ctx context.Context, id uuid.UUID) error
	// MarkFailed records a failed attempt. When dead is true the row moves to
	// StatusDead (retries exhausted); otherwise it returns to StatusPending
	// with next_retry_at set for the backoff window.
	MarkFailed(ctx context.Context, id uuid.UUID, attempts int, nextRetryAt time.Time, lastErr string, dead bool) error
	// CountByStatus returns a per-status count for observability (feeds the
	// outbox_unsent_count gauge).
	CountByStatus(ctx context.Context) (map[Status]int, error)
}

// backoff base and cap for the retry schedule. Matches permission-service's
// shape: exponential from 1s, capped at 5m.
const (
	backoffBase = time.Second
	backoffCap  = 5 * time.Minute
)

// backoff returns the delay before the given attempt (1-based) may be retried:
// 1s, 2s, 4s, 8s, … doubling per attempt, capped at 5m. Deterministic and
// overflow-safe, so it is unit-testable with no randomness.
func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	// Guard the shift so a large attempt count cannot overflow the duration.
	if shift >= 30 {
		return backoffCap
	}
	d := backoffBase << uint(shift)
	if d <= 0 || d > backoffCap {
		return backoffCap
	}
	return d
}
