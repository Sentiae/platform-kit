package outbox

import (
	"testing"
	"time"
)

func TestBackoffMonotonicAndCapped(t *testing.T) {
	tests := []struct {
		name    string
		attempt int
		want    time.Duration
	}{
		{"attempt 0 clamps to 1", 0, time.Second},
		{"attempt 1", 1, time.Second},
		{"attempt 2", 2, 2 * time.Second},
		{"attempt 3", 3, 4 * time.Second},
		{"attempt 4", 4, 8 * time.Second},
		{"large attempt capped", 40, backoffCap},
		{"negative clamps to 1", -5, time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backoff(tt.attempt); got != tt.want {
				t.Fatalf("backoff(%d) = %v, want %v", tt.attempt, got, tt.want)
			}
		})
	}
}

func TestBackoffIsMonotonicNonDecreasingAndBounded(t *testing.T) {
	var prev time.Duration
	for attempt := 1; attempt <= 50; attempt++ {
		d := backoff(attempt)
		if d < prev {
			t.Fatalf("backoff(%d) = %v decreased from %v", attempt, d, prev)
		}
		if d > backoffCap {
			t.Fatalf("backoff(%d) = %v exceeds cap %v", attempt, d, backoffCap)
		}
		if d <= 0 {
			t.Fatalf("backoff(%d) = %v is non-positive", attempt, d)
		}
		prev = d
	}
}
