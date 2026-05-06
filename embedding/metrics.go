// Prometheus metrics for the §C.8 cold-tier path.
//
// Two surfaces:
//   - PromColdMoverMetrics: implements ColdMoverMetrics for the
//     nightly cold-mover cron. Counts moved/skipped/failed rows +
//     records batch durations.
//   - RecordTierPromotion: emits one-shot promotion counters from
//     ActivityTierClassifier when search-hit threshold flips a repo
//     Cold→Warm or Warm→Hot.
//
// Metrics are registered against the default registry via promauto
// so any service importing platform-kit/embedding picks them up
// automatically — wire them once at process start by constructing
// PromColdMoverMetrics{} and passing it to ColdMover.WithMetrics.

package embedding

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Cold-mover counters. Labels stay narrow on purpose — high-cardinality
// labels (org_id, repo_id) would explode the time series count.
var (
	coldMovedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sentiae_embeddings_cold_moved_total",
		Help: "Vectors successfully migrated from hot pgvector to S3 cold tier.",
	})

	coldSkippedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sentiae_embeddings_cold_skipped_total",
		Help: "Vectors skipped during cold-mover run, by reason.",
	}, []string{"reason"})

	coldFailedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sentiae_embeddings_cold_failed_total",
		Help: "Vector migrations that hit an unrecoverable error.",
	})

	coldBatchDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "sentiae_embeddings_cold_batch_duration_seconds",
		Help:    "Wall-clock time per cold-mover batch (per repo).",
		Buckets: prometheus.ExponentialBuckets(0.1, 2, 12),
	})

	coldBatchSize = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "sentiae_embeddings_cold_batch_size",
		Help:    "Number of vectors moved per cold-mover batch.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 14),
	})

	tierPromotionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sentiae_embeddings_tier_promotions_total",
		Help: "Activity-driven tier promotions, labelled by signal + edge.",
	}, []string{"signal", "from_tier", "to_tier"})

	coldStorageGB = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "sentiae_embeddings_cold_storage_gb",
		Help: "Cold-tier vector storage footprint in GiB, set by an external sweeper.",
	}, []string{"backend"})
)

// PromColdMoverMetrics wires ColdMoverMetrics to the prometheus client.
type PromColdMoverMetrics struct{}

// IncMoved records one successfully-moved vector.
func (PromColdMoverMetrics) IncMoved() { coldMovedTotal.Inc() }

// IncSkipped records one skipped vector with a free-form reason
// label. The set of reasons should stay small + predictable
// (typical values: "not_cold_at_run", "already_moved").
func (PromColdMoverMetrics) IncSkipped(reason string) {
	if reason == "" {
		reason = "unknown"
	}
	coldSkippedTotal.WithLabelValues(reason).Inc()
}

// IncFailed records one move failure.
func (PromColdMoverMetrics) IncFailed() { coldFailedTotal.Inc() }

// ObserveBatch records a completed batch.
func (PromColdMoverMetrics) ObserveBatch(count int, elapsed time.Duration) {
	coldBatchDuration.Observe(elapsed.Seconds())
	coldBatchSize.Observe(float64(count))
}

// RecordTierPromotion emits one promotion event. signal is "commit"
// or "search_hit". from + to are RepoTier values. Used by callers
// of ActivityTierClassifier.RecordCommit/RecordSearchHit so the
// classifier itself stays storage-port-only.
func RecordTierPromotion(signal string, from, to RepoTier) {
	tierPromotionsTotal.WithLabelValues(signal, string(from), string(to)).Inc()
}

// SetColdStorageGB sets the gauge for the named backend
// (e.g. "s3", "minio"). Run from a periodic sweeper that reports
// the bucket size; kept here so the gauge name stays canonical.
func SetColdStorageGB(backend string, gb float64) {
	coldStorageGB.WithLabelValues(backend).Set(gb)
}

// Compile-time guarantee that PromColdMoverMetrics satisfies the port.
var _ ColdMoverMetrics = PromColdMoverMetrics{}
