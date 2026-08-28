package spiffe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sentiae/platform-kit/spiffe/spiffetest"
)

func TestTrustDomainID(t *testing.T) {
	td := TrustDomainID()
	if td.IsZero() {
		t.Fatal("trust domain should parse to a non-zero value")
	}
	if got := td.Name(); got != "sentiae.io" {
		t.Fatalf("trust domain name = %q, want sentiae.io", got)
	}
}

func TestServiceID(t *testing.T) {
	id := ServiceID("foundry")
	if want := "spiffe://sentiae.io/svc/foundry"; id.String() != want {
		t.Fatalf("ServiceID = %q, want %q", id.String(), want)
	}
	if id.Path() != "/svc/foundry" {
		t.Fatalf("ServiceID path = %q, want /svc/foundry", id.Path())
	}
	if !id.MemberOf(TrustDomainID()) {
		t.Fatal("ServiceID should be a member of the trust domain")
	}
}

// TestNewSource_WaitsForSocketWithinBound pins the behaviour the
// sourceStartupTimeout doc promises: the bound is spent WAITING, not printed
// after one instant failure. On 2026-08-06 vigil booted 4.5 minutes before the
// SPIRE agent; the one-shot probe returned Unavailable in 0.5 ms while
// reporting "within 1m0s", and the service ran without a TLS half for weeks.
func TestNewSource_WaitsForSocketWithinBound(t *testing.T) {
	restore := sourceStartupTimeout
	sourceStartupTimeout = 5 * time.Second
	t.Cleanup(func() { sourceStartupTimeout = restore })

	t.Run("socket appears inside the bound", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "wl")
		if err != nil {
			t.Fatalf("temp dir: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })

		sock := filepath.Join(dir, "api.sock")
		t.Setenv("SPIFFE_ENDPOINT_SOCKET", "unix://"+sock)

		ca := spiffetest.NewCA(t)
		go func() {
			time.Sleep(1500 * time.Millisecond)
			ca.StartWorkloadAPI(t, sock, "vigil")
		}()

		start := time.Now()
		src, err := NewSource(context.Background())
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("NewSource gave up on a socket that appeared after %s: %v", elapsed, err)
		}
		t.Cleanup(func() { _ = src.Close() })

		if elapsed < 1500*time.Millisecond {
			t.Fatalf("NewSource returned after %s, before the socket existed at 1.5s", elapsed)
		}
		svid, err := src.GetX509SVID()
		if err != nil {
			t.Fatalf("source holds no SVID: %v", err)
		}
		if want := "spiffe://sentiae.io/svc/vigil"; svid.ID.String() != want {
			t.Fatalf("SVID = %q, want %q", svid.ID, want)
		}
	})

	t.Run("socket never appears", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "wl")
		if err != nil {
			t.Fatalf("temp dir: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })

		sock := filepath.Join(dir, "api.sock")
		t.Setenv("SPIFFE_ENDPOINT_SOCKET", "unix://"+sock)

		start := time.Now()
		_, err = NewSource(context.Background())
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("NewSource returned a source with no Workload API at all")
		}
		// The whole point: the bound is CONSUMED. An instant error is the
		// defect, not the guard.
		if elapsed < sourceStartupTimeout {
			t.Fatalf("NewSource gave up after %s, want at least the %s bound it reports", elapsed, sourceStartupTimeout)
		}
		for _, want := range []string{"within 5s", "attempts", sock} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q must contain %q for operator diagnosis", err, want)
			}
		}
	})
}
