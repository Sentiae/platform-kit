// Activity-aware tier classification. (Paradigm Shift §C.8 promotion path)
//
// Static commit-time-based TierClassifier (tier.go) misses overnight
// flips: cold repo gets a 2am push, classifier still says cold until
// next nightly. ActivityTierClassifier layers fast signals on top:
//
//   - Commit signal — receive-pack hook calls RecordCommit(repoID).
//     Synchronously promotes to TierHot regardless of last-activity
//     time stored in the DB. Persisted on the repos.tier column lazily.
//   - Search-hit signal — search-service calls RecordSearchHit(repoID).
//     Increments an in-memory counter; ≥ HitThreshold within HitWindow
//     promotes Cold → Warm, Warm → Hot.
//   - Demotion is slow — only happens via the nightly cron walking
//     last-activity timestamps. Promotions are immediate; demotions
//     respect a recent-hit cooldown so a momentarily idle repo with
//     real users doesn't churn back to S3.
//
// In-memory state (hit counts) is intentionally non-persistent:
// promotions are bursty signals, the persistent tier column is the
// source of truth for cold-mover decisions. Restart drops in-flight
// counters; that's fine — search-time fallback still serves cold
// queries and the hits resume from zero.

package embedding

import (
	"context"
	"sync"
	"time"
)

// TierWriter persists tier changes. Implemented by the repo-side
// adapter that owns the `repos.tier` column. The classifier never
// touches the DB directly.
type TierWriter interface {
	SetTier(ctx context.Context, repoID string, tier RepoTier) error
}

// TierReader returns the persisted tier. Used as the base layer
// before activity overrides apply.
type TierReader interface {
	GetTier(ctx context.Context, repoID string) (RepoTier, time.Time, error)
}

// ActivityTierClassifier composes a TierClassifier (commit-time
// based) with two fast signals: explicit commit RecordCommit calls
// and bucketed search-hit counts. Thread-safe.
type ActivityTierClassifier struct {
	base         *TierClassifier
	reader       TierReader
	writer       TierWriter
	now          func() time.Time
	hitWindow    time.Duration
	hitThreshold int

	mu   sync.Mutex
	hits map[string]*hitBucket // repoID → bucket
}

// hitBucket counts hits within the rolling window.
type hitBucket struct {
	count   int
	resetAt time.Time
}

// ActivityConfig wires the classifier.
type ActivityConfig struct {
	// Base is the underlying commit-time classifier. Required.
	Base *TierClassifier
	// Reader returns the persisted tier. Required.
	Reader TierReader
	// Writer persists promotions. Required.
	Writer TierWriter
	// HitWindow is the rolling window for search-hit counting.
	// Defaults to 1 hour.
	HitWindow time.Duration
	// HitThreshold is the count that triggers promotion. Defaults to 5.
	HitThreshold int
	// Now overrides the clock for tests.
	Now func() time.Time
}

// NewActivityTierClassifier builds the classifier.
func NewActivityTierClassifier(cfg ActivityConfig) *ActivityTierClassifier {
	if cfg.Base == nil {
		cfg.Base = NewTierClassifier()
	}
	if cfg.HitWindow <= 0 {
		cfg.HitWindow = time.Hour
	}
	if cfg.HitThreshold <= 0 {
		cfg.HitThreshold = 5
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	return &ActivityTierClassifier{
		base:         cfg.Base,
		reader:       cfg.Reader,
		writer:       cfg.Writer,
		now:          cfg.Now,
		hitWindow:    cfg.HitWindow,
		hitThreshold: cfg.HitThreshold,
		hits:         map[string]*hitBucket{},
	}
}

// RecordCommit immediately promotes the repo to TierHot. Called by
// receive-pack on every push (HTTP + SSH paths) so a 2am commit on a
// cold repo flips before the next search lands.
func (c *ActivityTierClassifier) RecordCommit(ctx context.Context, repoID string) error {
	if c == nil || c.writer == nil {
		return nil
	}
	prev := TierCold
	if c.reader != nil {
		if t, _, err := c.reader.GetTier(ctx, repoID); err == nil {
			prev = t
		}
	}
	if err := c.writer.SetTier(ctx, repoID, TierHot); err != nil {
		return err
	}
	if prev != TierHot {
		RecordTierPromotion("commit", prev, TierHot)
	}
	return nil
}

// RecordSearchHit increments the rolling-window counter and promotes
// the repo when the threshold is crossed. Cold → Warm, Warm → Hot.
// Returns the post-call tier so callers can short-circuit S3 fetches
// when promotion lands inline with the request.
func (c *ActivityTierClassifier) RecordSearchHit(ctx context.Context, repoID string) (RepoTier, error) {
	if c == nil || c.reader == nil {
		return TierCold, nil
	}
	current, _, err := c.reader.GetTier(ctx, repoID)
	if err != nil {
		return TierCold, err
	}
	c.mu.Lock()
	now := c.now()
	bucket, ok := c.hits[repoID]
	if !ok || now.After(bucket.resetAt) {
		bucket = &hitBucket{count: 0, resetAt: now.Add(c.hitWindow)}
		c.hits[repoID] = bucket
	}
	bucket.count++
	crossed := bucket.count >= c.hitThreshold
	c.mu.Unlock()

	if !crossed {
		return current, nil
	}
	// Threshold crossed — promote one tier toward Hot. Skip if already
	// Hot. Reset the bucket to avoid double-promotion within window.
	c.mu.Lock()
	c.hits[repoID] = &hitBucket{count: 0, resetAt: now.Add(c.hitWindow)}
	c.mu.Unlock()

	switch current {
	case TierCold:
		if c.writer != nil {
			if err := c.writer.SetTier(ctx, repoID, TierWarm); err != nil {
				return current, err
			}
		}
		RecordTierPromotion("search_hit", TierCold, TierWarm)
		return TierWarm, nil
	case TierWarm:
		if c.writer != nil {
			if err := c.writer.SetTier(ctx, repoID, TierHot); err != nil {
				return current, err
			}
		}
		RecordTierPromotion("search_hit", TierWarm, TierHot)
		return TierHot, nil
	default:
		return current, nil
	}
}

// EffectiveTier returns the tier to use for routing decisions. Reads
// the persisted tier from the writer + applies any in-memory hot-flip
// from a same-process commit that hasn't yet round-tripped to the DB.
func (c *ActivityTierClassifier) EffectiveTier(ctx context.Context, repoID string, lastActivity time.Time) (RepoTier, error) {
	if c == nil || c.reader == nil {
		return c.base.Classify(lastActivity), nil
	}
	tier, _, err := c.reader.GetTier(ctx, repoID)
	if err != nil {
		// Fall back to commit-time classification on read failure —
		// don't deny service when the metadata table hiccups.
		return c.base.Classify(lastActivity), nil
	}
	return tier, nil
}

// HitCount returns the current bucket count for tests + observability.
func (c *ActivityTierClassifier) HitCount(repoID string) int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if b, ok := c.hits[repoID]; ok {
		return b.count
	}
	return 0
}
