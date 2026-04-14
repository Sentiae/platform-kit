// Package dbmetrics — Phase 8 shared slow-query logging helper.
//
// Wraps a query execution in a timer and emits a slog warning when
// the elapsed time exceeds the threshold. Drop-in for any service
// that wants to flag slow DB calls without reaching for a full
// tracing stack.
//
//	start := dbmetrics.Start()
//	rows, err := db.Query(ctx, sql, args...)
//	dbmetrics.Observe(ctx, start, 200*time.Millisecond, "list_specs")
//
// The threshold is per-call so hot paths can enforce stricter SLOs
// than batch jobs. Pass 0 to opt out without changing the call site.
package dbmetrics

import (
	"context"
	"log/slog"
	"time"
)

// Start captures a start time; keep it opaque so callers don't depend
// on the time.Time representation.
func Start() time.Time { return time.Now() }

// Observe logs a slow-query warning if the elapsed time exceeds the
// threshold. Label is a short, low-cardinality name ("list_specs",
// "upsert_finding") suitable for log aggregation.
//
// Returns the measured duration so callers can attach it to telemetry
// spans, Prometheus histograms, or test assertions.
func Observe(ctx context.Context, start time.Time, threshold time.Duration, label string) time.Duration {
	d := time.Since(start)
	if threshold > 0 && d >= threshold {
		slog.WarnContext(ctx, "slow_query",
			"label", label,
			"duration_ms", d.Milliseconds(),
			"threshold_ms", threshold.Milliseconds(),
		)
	}
	return d
}
