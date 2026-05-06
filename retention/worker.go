package retention

import (
	"context"
	"errors"
	"time"
)

// HotDeleter is the per-data-kind port for hard-deleting rows past
// their DeleteAfter cutoff when there is *no* cold tier (free plan
// signals/logs, free plan everything). (§H.3)
type HotDeleter interface {
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int, error)
}

// Worker enforces a RetentionPolicy by running cold-tier archive
// (when ColdAfter > 0) and/or hot deletes (when DeleteAfter > 0).
// Wiring lives in each service — ops-service gets one Worker per
// signal/log table, work-service for artifacts, etc.
type Worker struct {
	policy  RetentionPolicy
	mover   *ColdTierMover // nil when ColdAfter == 0
	deleter HotDeleter     // nil when there is no hot table to delete from
	now     func() time.Time
}

// NewWorker builds a worker that runs both passes if both are
// configured. Either may be nil; the worker no-ops the missing pass.
func NewWorker(policy RetentionPolicy, mover *ColdTierMover, deleter HotDeleter) *Worker {
	return &Worker{policy: policy, mover: mover, deleter: deleter, now: time.Now}
}

// Stats summarises one Run() pass.
type Stats struct {
	Archived        int
	Deleted         int
	GlacierMirrored int
}

// ErrNothingConfigured is returned when a policy has no archive +
// no delete step. Surfacing this as an error makes mis-seeded plans
// loud at boot rather than silently skipping retention.
var ErrNothingConfigured = errors.New("retention: policy has no archive or delete step")

// Run executes one pass. It is safe to call repeatedly.
func (w *Worker) Run(ctx context.Context) (Stats, error) {
	if err := w.policy.Validate(); err != nil {
		return Stats{}, err
	}
	if w.mover == nil && w.deleter == nil {
		return Stats{}, ErrNothingConfigured
	}
	var stats Stats
	if w.mover != nil {
		archived, deleted, err := w.mover.Run(ctx)
		if err != nil {
			return stats, err
		}
		stats.Archived = archived
		stats.Deleted += deleted
	}
	if w.deleter != nil && w.policy.DeleteAfter > 0 {
		cutoff := w.now().Add(-time.Duration(w.policy.DeleteAfter) * 24 * time.Hour)
		n, err := w.deleter.DeleteOlderThan(ctx, cutoff)
		if err != nil {
			return stats, err
		}
		stats.Deleted += n
	}
	return stats, nil
}
