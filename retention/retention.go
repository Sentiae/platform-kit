// Package retention encodes how long each data kind survives in
// each storage tier. Plans pick a RetentionPolicy per DataKind;
// retention workers in each service read the policy and delete /
// archive accordingly. (Paradigm Shift §H.1)
package retention

import (
	"errors"
	"time"
)

// DataKind identifies a category of data subject to retention.
// New kinds get added here as we wire workers in their services.
type DataKind string

const (
	// DataKindSignals — telemetry/event signals (ops-service).
	DataKindSignals DataKind = "signals"
	// DataKindLogs — application logs (ops-service).
	DataKindLogs DataKind = "logs"
	// DataKindArtifacts — build artifacts (work-service / git-service).
	DataKindArtifacts DataKind = "artifacts"
	// DataKindAudit — audit log entries.
	DataKindAudit DataKind = "audit"
	// DataKindEmbeddingsCold — cold-tier embedding vectors.
	DataKindEmbeddingsCold DataKind = "embeddings.cold"
)

// RetentionPolicy is the tier ladder for one data kind. Rows live
// in the hot tier for HotDays, then move to warm, then cold (S3),
// then are deleted. Zero values mean "skip that step".
type RetentionPolicy struct {
	DataKind    DataKind
	HotDays     int
	WarmDays    int
	ColdAfter   int // days from creation when row moves to S3 cold
	DeleteAfter int // days from creation when row is hard-deleted; 0 = never
}

// Validate checks the policy is internally consistent.
func (p RetentionPolicy) Validate() error {
	if p.DataKind == "" {
		return ErrInvalidDataKind
	}
	if p.HotDays < 0 || p.WarmDays < 0 || p.ColdAfter < 0 || p.DeleteAfter < 0 {
		return ErrNegativeRetention
	}
	if p.ColdAfter > 0 && p.DeleteAfter > 0 && p.ColdAfter > p.DeleteAfter {
		return ErrColdAfterDelete
	}
	return nil
}

// ShouldArchive returns true when a row created at createdAt is past
// its hot+warm window and should move to S3 cold.
func (p RetentionPolicy) ShouldArchive(createdAt, now time.Time) bool {
	if p.ColdAfter == 0 {
		return false
	}
	return now.Sub(createdAt) >= time.Duration(p.ColdAfter)*24*time.Hour
}

// ShouldDelete returns true when a row created at createdAt has
// passed DeleteAfter and should be hard-deleted.
func (p RetentionPolicy) ShouldDelete(createdAt, now time.Time) bool {
	if p.DeleteAfter == 0 {
		return false
	}
	return now.Sub(createdAt) >= time.Duration(p.DeleteAfter)*24*time.Hour
}

// PlanRetention is a per-plan map of DataKind -> RetentionPolicy.
// Lookups for a missing kind return the zero policy + false so
// callers can fall back to a global default.
type PlanRetention map[DataKind]RetentionPolicy

// Get returns the policy for the given kind.
func (p PlanRetention) Get(kind DataKind) (RetentionPolicy, bool) {
	pol, ok := p[kind]
	return pol, ok
}

// Sentinel errors.
var (
	ErrInvalidDataKind   = errors.New("retention: data kind required")
	ErrNegativeRetention = errors.New("retention: days must be >= 0")
	ErrColdAfterDelete   = errors.New("retention: cold_after must be <= delete_after")
)
