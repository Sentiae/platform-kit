package grpcclient

import (
	"context"
	"testing"
	"time"

	"github.com/sentiae/platform-kit/config"
)

// TestNewMeshSource_OffNeedsNoSource proves off/empty short-circuits to
// (nil, nil) without touching the workload API — no source is needed and none
// is built.
func TestNewMeshSource_OffNeedsNoSource(t *testing.T) {
	for _, mode := range []string{"", config.MTLSModeOff} {
		src, err := NewMeshSource(context.Background(), mode)
		if err != nil {
			t.Fatalf("mode %q: unexpected error: %v", mode, err)
		}
		if src != nil {
			src.Close()
			t.Fatalf("mode %q: expected nil source, got %v", mode, src)
		}
	}
}

// TestNewMeshSource_FailClosed proves the fail-closed posture when the workload
// API is unreachable: strict RETURNS AN ERROR (refuse to boot), permissive
// returns (nil, nil) (boot, degrade to plaintext-with-warn). A bogus
// SPIFFE_ENDPOINT_SOCKET plus a short-deadline ctx forces spiffe.NewSource to
// fail fast without a real SPIRE agent.
func TestNewMeshSource_FailClosed(t *testing.T) {
	t.Setenv("SPIFFE_ENDPOINT_SOCKET", "unix:///nonexistent/spire-agent.sock")

	t.Run("strict refuses to boot", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		src, err := NewMeshSource(ctx, config.MTLSModeStrict)
		if err == nil {
			if src != nil {
				src.Close()
			}
			t.Fatal("expected strict + unreachable workload API to error (refuse to boot), got nil")
		}
		if src != nil {
			src.Close()
			t.Fatalf("expected nil source on strict failure, got %v", src)
		}
	})

	t.Run("permissive degrades to nil", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		src, err := NewMeshSource(ctx, config.MTLSModePermissive)
		if err != nil {
			t.Fatalf("expected permissive to degrade (nil, nil), got error: %v", err)
		}
		if src != nil {
			src.Close()
			t.Fatalf("expected nil source on permissive degrade, got %v", src)
		}
	})
}
