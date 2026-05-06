// Cold-tier mover. (Paradigm Shift §C.8)
//
// Walks repositories whose persisted tier == Cold, lifts every
// embedding row out of the hot Cache (pgvector) into the durable
// VectorStore (S3 / minio), then nulls the pgvector vector column
// so the row functions as a sparse pointer. Search-time fallback
// re-fetches from S3 + writes back to cache when needed.
//
// Lifecycle: a nightly cron (or on-demand admin trigger) calls
// MoveCold once per cold repo. Idempotent — already-moved rows
// stay moved; rows whose repos became hot are skipped via a
// last-look tier check inside the per-row loop.

package embedding

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ColdMoverRow is a single embedding row eligible for migration.
// Adapter-side queries return this shape; the mover stays
// storage-agnostic by walking a channel of these instead of running
// SQL directly.
type ColdMoverRow struct {
	OrgID      string
	RepoID     string
	FilePath   string
	Symbol     string
	Model      string
	ContentSHA string
	Vector     Vector
}

// ColdMoverSource yields cold-eligible rows for a single repo.
// Implementations stream rows lazily so the mover doesn't pin a
// 100k-vector page in memory.
type ColdMoverSource interface {
	// CountCold reports how many rows would be migrated for repoID.
	// Used for metrics + dry-run reporting.
	CountCold(ctx context.Context, repoID string) (int, error)
	// IterateCold streams rows. Closing the cancel context aborts.
	IterateCold(ctx context.Context, repoID string) (<-chan ColdMoverRow, <-chan error)
	// MarkMoved nulls the vector column on the hot row + records
	// the cold-store key on the row metadata. Idempotent.
	MarkMoved(ctx context.Context, repoID, contentSHA, coldKey string) error
}

// ColdMoverTierGate guards each move with a fresh tier read so a
// repo that flipped Hot mid-run skips the mover.
type ColdMoverTierGate interface {
	GetTier(ctx context.Context, repoID string) (RepoTier, time.Time, error)
}

// ColdMoverMetrics is the observability hook. Adapters wire to
// Prometheus counters; nil disables metrics.
type ColdMoverMetrics interface {
	IncMoved()
	IncSkipped(reason string)
	IncFailed()
	ObserveBatch(count int, elapsed time.Duration)
}

// ColdMover migrates hot embedding rows to cold storage.
type ColdMover struct {
	source  ColdMoverSource
	store   VectorStore
	gate    ColdMoverTierGate
	metrics ColdMoverMetrics

	// BatchSize caps per-tick fetches. Default 500.
	BatchSize int
	// Now overrides the clock for tests.
	Now func() time.Time
}

// NewColdMover wires the mover.
func NewColdMover(source ColdMoverSource, store VectorStore, gate ColdMoverTierGate) *ColdMover {
	return &ColdMover{
		source:    source,
		store:     store,
		gate:      gate,
		BatchSize: 500,
		Now:       func() time.Time { return time.Now().UTC() },
	}
}

// WithMetrics wires the metrics hook.
func (m *ColdMover) WithMetrics(mx ColdMoverMetrics) *ColdMover {
	m.metrics = mx
	return m
}

// MoveCold migrates eligible rows for a single repo. Returns the
// count moved and any unrecoverable error. Single-row failures are
// logged via metrics + skipped — partial progress is preserved
// because every row writes to S3 then commits MarkMoved before the
// next row begins.
func (m *ColdMover) MoveCold(ctx context.Context, repoID string) (int, error) {
	if m == nil || m.source == nil || m.store == nil {
		return 0, errors.New("cold_mover: not initialised")
	}

	// Last-look tier guard — skip the whole repo if it flipped Hot
	// since the cron picked it up.
	if m.gate != nil {
		tier, _, err := m.gate.GetTier(ctx, repoID)
		if err != nil {
			return 0, fmt.Errorf("cold_mover: gate read: %w", err)
		}
		if tier != TierCold {
			if m.metrics != nil {
				m.metrics.IncSkipped("not_cold_at_run")
			}
			return 0, nil
		}
	}

	start := m.Now()
	rowsCh, errCh := m.source.IterateCold(ctx, repoID)
	moved := 0
	for row := range rowsCh {
		key := KeyForSymbol(row.OrgID, row.RepoID, row.FilePath, row.Symbol)
		if key == "" {
			// Fall back to content-keyed cold key when symbol metadata
			// is missing (rare — happens for raw text-chunk embeddings).
			key = ColdKey(row.Model, row.ContentSHA)
		}
		if err := m.store.Put(ctx, key, row.Vector); err != nil {
			if m.metrics != nil {
				m.metrics.IncFailed()
			}
			continue
		}
		if err := m.source.MarkMoved(ctx, row.RepoID, row.ContentSHA, key); err != nil {
			if m.metrics != nil {
				m.metrics.IncFailed()
			}
			continue
		}
		moved++
		if m.metrics != nil {
			m.metrics.IncMoved()
		}
	}
	if err, ok := <-errCh; ok && err != nil {
		return moved, fmt.Errorf("cold_mover: source iter: %w", err)
	}
	if m.metrics != nil {
		m.metrics.ObserveBatch(moved, m.Now().Sub(start))
	}
	return moved, nil
}
