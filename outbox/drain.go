package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	pkkafka "github.com/sentiae/platform-kit/kafka"
	"github.com/sentiae/platform-kit/tenant"
)

// Prometheus metrics (constitution §22). Package-level via promauto so they
// register once per process regardless of how many Drainers a service builds.
var (
	// outboxUnsentCount is the number of pending outbox rows, refreshed each
	// drain tick from CountByStatus.
	outboxUnsentCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "outbox_unsent_count",
		Help: "Number of pending (unsent) outbox messages.",
	})
	// outboxDeadTotal counts rows dead-lettered after exhausting max_retries.
	outboxDeadTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "outbox_dead_total",
		Help: "Total outbox messages dead-lettered after exhausting retries.",
	})
)

// DrainConfig configures the drain worker. Zero values fall back to defaults.
type DrainConfig struct {
	// Interval between drain ticks. Default 1s.
	Interval time.Duration
	// BatchSize caps rows fetched per tick. Default 100.
	BatchSize int
	// DefaultMaxRetries caps publish attempts for rows whose max_retries is
	// unset (0). Default 10.
	DefaultMaxRetries int
}

// Drainer polls the outbox, publishes due rows to Kafka, and drives the status
// ladder. At-least-once: a crash between publish and MarkSent re-publishes on
// the next tick, so consumers must be idempotent (the CloudEvent IdempotencyKey
// is the dedup hint).
type Drainer struct {
	repo              Repo
	publisher         pkkafka.Publisher
	logger            *slog.Logger
	interval          time.Duration
	batchSize         int
	defaultMaxRetries int
}

// NewDrainer builds the worker.
func NewDrainer(repo Repo, publisher pkkafka.Publisher, logger *slog.Logger, cfg DrainConfig) *Drainer {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.DefaultMaxRetries <= 0 {
		cfg.DefaultMaxRetries = defaultMaxRetries
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Drainer{
		repo:              repo,
		publisher:         publisher,
		logger:            logger.With("component", "outbox_drain"),
		interval:          cfg.Interval,
		batchSize:         cfg.BatchSize,
		defaultMaxRetries: cfg.DefaultMaxRetries,
	}
}

// Start runs the drain loop until ctx is cancelled. The worker context is
// marked as a system/cross-tenant path (tenant.WithSystemContext) so the
// RLS-exempt drain reads outbox_messages across all orgs — the foundry/ops
// omission that must NOT recur. A panic in a tick is recovered (§30.4) so the
// loop keeps the service alive.
func (d *Drainer) Start(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			d.logger.Error("outbox drain panic recovered", "panic", rec)
		}
	}()

	ctx = tenant.WithSystemContext(ctx)

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	d.logger.Info("outbox drain started",
		"interval", d.interval,
		"batch_size", d.batchSize,
		"default_max_retries", d.defaultMaxRetries,
	)

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("outbox drain stopped")
			return
		case <-ticker.C:
			if err := d.RunOnce(ctx); err != nil {
				d.logger.Warn("outbox drain tick failed", "error", err)
			}
		}
	}
}

// RunOnce fetches one due batch and drives each row through the state machine:
// MarkSending → publish → MarkSent on success, or MarkFailed (retry or dead) on
// failure. Exported so tests and one-shot callers can drive a single cycle.
func (d *Drainer) RunOnce(ctx context.Context) error {
	if counts, err := d.repo.CountByStatus(ctx); err == nil {
		outboxUnsentCount.Set(float64(counts[StatusPending]))
	} else {
		d.logger.Warn("outbox count-by-status failed", "error", err)
	}

	rows, err := d.repo.FetchDue(ctx, d.batchSize)
	if err != nil {
		return fmt.Errorf("fetch due: %w", err)
	}

	for _, row := range rows {
		if err := d.repo.MarkSending(ctx, row.ID); err != nil {
			d.logger.Warn("outbox mark sending failed", "id", row.ID, "error", err)
			continue
		}
		if err := d.publishOne(ctx, row); err != nil {
			d.handleFailure(ctx, row, err)
			continue
		}
		if err := d.repo.MarkSent(ctx, row.ID); err != nil {
			d.logger.Warn("outbox mark sent failed", "id", row.ID, "error", err)
		}
	}
	return nil
}

// handleFailure computes the next attempt and either schedules a backoff retry
// or dead-letters the row once its per-row max_retries is exhausted.
func (d *Drainer) handleFailure(ctx context.Context, row Row, cause error) {
	attempts := row.Attempts + 1
	maxRetries := row.MaxRetries
	if maxRetries <= 0 {
		maxRetries = d.defaultMaxRetries
	}

	if attempts >= maxRetries {
		if err := d.repo.MarkFailed(ctx, row.ID, attempts, time.Time{}, cause.Error(), true); err != nil {
			d.logger.Error("outbox mark dead failed", "id", row.ID, "error", err)
		}
		d.logger.Error("outbox message dead-lettered after attempts",
			"id", row.ID,
			"topic", row.Topic,
			"attempts", attempts,
			"last_error", cause.Error(),
		)
		outboxDeadTotal.Inc()
		return
	}

	nextRetryAt := time.Now().UTC().Add(backoff(attempts))
	if err := d.repo.MarkFailed(ctx, row.ID, attempts, nextRetryAt, cause.Error(), false); err != nil {
		d.logger.Error("outbox mark failed", "id", row.ID, "error", err)
	}
	d.logger.Warn("outbox publish failed, scheduled retry",
		"id", row.ID,
		"topic", row.Topic,
		"attempts", attempts,
		"next_retry_at", nextRetryAt,
		"error", cause,
	)
}

// publishOne re-hydrates the stored EventData and republishes via platform-kit,
// so the CloudEvent envelope (and the real Kafka topic) is regenerated cleanly
// each attempt — the wire bytes match a direct publish.
func (d *Drainer) publishOne(ctx context.Context, row Row) error {
	var data pkkafka.EventData
	if err := json.Unmarshal(row.Payload, &data); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	return d.publisher.Publish(ctx, row.Topic, data)
}
