package embedding

import "time"

// RepoTier classifies a repository by recency for embedding policy.
// (Paradigm Shift §C.3)
//
// Hot   — touched in the last 30 days. Embed eagerly on every change.
// Warm  — touched in the last year. Embed eagerly but lower priority.
// Cold  — older. Skip eager embed; embed lazily on first search hit.
//
// These windows are tuned for our cost model — ~70% of embed cost
// is spent on cold repos that never get searched. Adjust via
// TierClassifier.HotDays / WarmDays at boot.
type RepoTier string

const (
	TierHot  RepoTier = "hot"
	TierWarm RepoTier = "warm"
	TierCold RepoTier = "cold"
)

// TierClassifier returns a RepoTier given the last-commit
// timestamp. Zero values default to 30 days hot / 365 days warm.
type TierClassifier struct {
	HotDays  int
	WarmDays int
	Now      func() time.Time
}

// NewTierClassifier builds a classifier with sane defaults.
func NewTierClassifier() *TierClassifier {
	return &TierClassifier{
		HotDays:  30,
		WarmDays: 365,
		Now:      func() time.Time { return time.Now().UTC() },
	}
}

// Classify maps lastActivity → RepoTier.
//
// A zero or future timestamp resolves to TierCold so empty seed
// data doesn't get treated as hot.
func (c *TierClassifier) Classify(lastActivity time.Time) RepoTier {
	if c == nil {
		return TierCold
	}
	if lastActivity.IsZero() {
		return TierCold
	}
	now := c.Now()
	if now.Before(lastActivity) {
		return TierCold
	}
	hotDays := c.HotDays
	if hotDays <= 0 {
		hotDays = 30
	}
	warmDays := c.WarmDays
	if warmDays <= 0 {
		warmDays = 365
	}
	age := now.Sub(lastActivity)
	switch {
	case age <= time.Duration(hotDays)*24*time.Hour:
		return TierHot
	case age <= time.Duration(warmDays)*24*time.Hour:
		return TierWarm
	default:
		return TierCold
	}
}

// EagerOnChange reports whether a tier should re-embed on every
// commit. Hot + warm yes; cold no.
func EagerOnChange(t RepoTier) bool {
	switch t {
	case TierHot, TierWarm:
		return true
	default:
		return false
	}
}

// PriorityForTier maps tiers to a queue priority. Higher = run sooner.
func PriorityForTier(t RepoTier) int {
	switch t {
	case TierHot:
		return 100
	case TierWarm:
		return 50
	default:
		return 10
	}
}
