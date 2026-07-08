package interceptor

import (
	"bytes"
	"testing"

	"github.com/sentiae/platform-kit/middleware"
)

func TestNewChain_MinimalConfig(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	unary, stream := NewChain(Config{Logger: l})

	// Minimal chain: Recovery + Logging + SVID = 3 each.
	if len(unary) != 3 {
		t.Fatalf("expected 3 unary interceptors, got %d", len(unary))
	}
	if len(stream) != 3 {
		t.Fatalf("expected 3 stream interceptors, got %d", len(stream))
	}
}

func TestNewChain_WithMetrics(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	m := &InMemoryMetrics{}
	unary, stream := NewChain(Config{Logger: l, Metrics: m})

	// Recovery + Logging + Metrics + SVID = 4 each.
	if len(unary) != 4 {
		t.Fatalf("expected 4 unary interceptors, got %d", len(unary))
	}
	if len(stream) != 4 {
		t.Fatalf("expected 4 stream interceptors, got %d", len(stream))
	}
}

func TestNewChain_WithAuth(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	unary, stream := NewChain(Config{
		Logger: l,
		Auth: &AuthConfig{
			TokenValidator: &fakeTokenValidator{
				claims: middleware.Claims{Subject: "user-1"},
			},
		},
	})

	// Recovery + Logging + SVID + Auth = 4 each.
	if len(unary) != 4 {
		t.Fatalf("expected 4 unary interceptors, got %d", len(unary))
	}
	if len(stream) != 4 {
		t.Fatalf("expected 4 stream interceptors, got %d", len(stream))
	}
}

func TestNewChain_FullConfig(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	m := &InMemoryMetrics{}
	unary, stream := NewChain(Config{
		Logger:  l,
		Metrics: m,
		Auth: &AuthConfig{
			TokenValidator: &fakeTokenValidator{
				claims: middleware.Claims{Subject: "user-1"},
			},
		},
	})

	// Recovery + Logging + Metrics + SVID + Auth = 5 each.
	if len(unary) != 5 {
		t.Fatalf("expected 5 unary interceptors, got %d", len(unary))
	}
	if len(stream) != 5 {
		t.Fatalf("expected 5 stream interceptors, got %d", len(stream))
	}
}

func TestNewChain_NilLoggerUsesDefault(t *testing.T) {
	// Should not panic with nil logger. Recovery + Logging + SVID = 3 each.
	unary, stream := NewChain(Config{})
	if len(unary) != 3 {
		t.Fatalf("expected 3 unary interceptors, got %d", len(unary))
	}
	if len(stream) != 3 {
		t.Fatalf("expected 3 stream interceptors, got %d", len(stream))
	}
}
