package webhook

import "time"

// BackoffDuration returns the minimum delay before the given
// retry-attempt should be dispatched. Cadence: 1min → 5min → 25min →
// 2h → 10h (caps at 10h). Kept deterministic (no jitter) so callers
// can test delivery windows without flakiness; add jitter at the
// worker if needed.
func BackoffDuration(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 1 * time.Minute
	case attempt == 2:
		return 5 * time.Minute
	case attempt == 3:
		return 25 * time.Minute
	case attempt == 4:
		return 2 * time.Hour
	default:
		return 10 * time.Hour
	}
}

// IsRetryable reports whether a delivery failure at the given HTTP
// status code is worth retrying. 2xx never retry, 4xx never retry
// (except 408/429), 5xx always retry.
func IsRetryable(statusCode int) bool {
	switch {
	case statusCode >= 200 && statusCode < 300:
		return false
	case statusCode == 408, statusCode == 429:
		return true
	case statusCode >= 400 && statusCode < 500:
		return false
	case statusCode >= 500:
		return true
	default:
		// 1xx/3xx, treat as non-success but retryable
		return true
	}
}
