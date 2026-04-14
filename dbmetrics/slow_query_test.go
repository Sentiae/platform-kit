package dbmetrics

import (
	"context"
	"testing"
	"time"
)

func TestObserve_ReturnsMeasuredDuration(t *testing.T) {
	start := Start()
	time.Sleep(2 * time.Millisecond)
	got := Observe(context.Background(), start, 0, "test")
	if got < 2*time.Millisecond {
		t.Fatalf("want >=2ms, got %v", got)
	}
}

func TestObserve_ZeroThresholdIsOptOut(t *testing.T) {
	// No panic, no log (can't easily assert no log without a custom
	// handler; just ensure it returns cleanly).
	Observe(context.Background(), Start(), 0, "test")
}

func TestObserve_BelowThresholdNoWarn(t *testing.T) {
	// Threshold well above the measured duration — the function must
	// still return the elapsed time.
	got := Observe(context.Background(), Start(), time.Hour, "test")
	if got < 0 {
		t.Fatalf("negative duration: %v", got)
	}
}
