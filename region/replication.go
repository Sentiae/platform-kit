// Package region — cross-region replication scaffolding.
//
// This file declares the policy types every service honors when it
// writes data scoped to a specific region. Today's implementation
// covers Kafka topic mirroring conventions; future storage-level
// replication (Postgres logical replication, S3 cross-region sync)
// plugs into the same policy object.
package region

import (
	"fmt"
	"strings"
	"time"
)

// ReplicationMode controls how data leaving its home region is treated.
type ReplicationMode string

const (
	// ReplicationModeIsolated: data never leaves the home region.
	// Violations are a hard error. Default for orgs with strict
	// data-residency (GDPR, HIPAA).
	ReplicationModeIsolated ReplicationMode = "isolated"

	// ReplicationModeBestEffort: data replicates to peer regions
	// when feasible, but loss on failover is acceptable. Default
	// for orgs with no explicit residency contract.
	ReplicationModeBestEffort ReplicationMode = "best_effort"

	// ReplicationModeStrictMirror: synchronous replication to at
	// least one peer region before the write is acked. Opt-in for
	// high-availability orgs; costs latency.
	ReplicationModeStrictMirror ReplicationMode = "strict_mirror"
)

// IsValid reports whether the mode is one of the known tiers.
func (m ReplicationMode) IsValid() bool {
	switch m {
	case ReplicationModeIsolated, ReplicationModeBestEffort, ReplicationModeStrictMirror:
		return true
	}
	return false
}

// ReplicationPolicy captures the per-org replication contract. Every
// mutation path consults this before a write:
//
//   - Kafka: policy.KafkaMirror(eventType) returns the set of peer
//     regions to mirror to (via Kafka MirrorMaker 2 topic prefixes).
//   - Storage: policy.StorageMirror(resourceType) returns the set of
//     S3 buckets / Postgres follower DSNs to write through.
//
// The policy is created at org creation time and revisited whenever
// the org changes its DataResidency setting. Services MUST refuse to
// write cross-region when Mode == ReplicationModeIsolated.
type ReplicationPolicy struct {
	OrgID       string
	HomeRegion  Region
	PeerRegions []Region
	Mode        ReplicationMode
	// DataResidency is the compliance-labeled region group (e.g.
	// "eu", "us", "strict-gdpr"). Services use this to block
	// deployments that would land data outside the allowed set.
	DataResidency string
	UpdatedAt     time.Time
}

// KafkaMirrorTopicPrefix is the convention Kafka MirrorMaker uses to
// republish events from another region: "<sourceRegion>.<origTopic>".
// Callers subscribing to cross-region events consume both the local
// topic and the mirrored one.
func KafkaMirrorTopicPrefix(source Region) string {
	if source == "" {
		return ""
	}
	// Kafka topic names can't contain dashes in every setup, so we
	// normalize to underscores. "us-east-1" → "us_east_1".
	return strings.ReplaceAll(string(source), "-", "_") + "."
}

// AllowsRegion reports whether a write scoped to `target` is allowed
// under the policy. Returns an error explaining the violation when
// denied.
func (p *ReplicationPolicy) AllowsRegion(target Region) error {
	if p == nil {
		// No policy = treat as isolated to home region. Safest default.
		return fmt.Errorf("replication: no policy; target %q denied by default", target)
	}
	if target == p.HomeRegion {
		return nil
	}
	switch p.Mode {
	case ReplicationModeIsolated:
		return fmt.Errorf("replication: org %s is isolated to %s; target %q denied", p.OrgID, p.HomeRegion, target)
	case ReplicationModeBestEffort, ReplicationModeStrictMirror:
		for _, peer := range p.PeerRegions {
			if peer == target {
				return nil
			}
		}
		return fmt.Errorf("replication: target %q not in peer list for %s", target, p.HomeRegion)
	}
	return fmt.Errorf("replication: unknown mode %q", p.Mode)
}
