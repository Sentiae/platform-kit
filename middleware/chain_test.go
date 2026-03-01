package middleware

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewChain_MinimalConfig(t *testing.T) {
	chain := NewChain(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	// Minimal chain: Recovery, RequestID, Logging = 3 middleware.
	if len(chain) != 3 {
		t.Fatalf("expected 3 middleware, got %d", len(chain))
	}
}

func TestNewChain_FullConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chain := NewChain(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics: func(next http.Handler) http.Handler {
			return next
		},
		Tracing: func(next http.Handler) http.Handler {
			return next
		},
		CORS: &CORSConfig{
			AllowedOrigins: []string{"https://example.com"},
		},
		RateLimiter: NewRateLimiter(ctx, RateLimitConfig{Rate: 100, Burst: 100}),
		Auth: &testValidator{
			claims: Claims{Subject: "user"},
		},
	})

	// Full chain: Recovery, RequestID, Logging, Metrics, Tracing, CORS, RateLimit, Auth = 8.
	if len(chain) != 8 {
		t.Fatalf("expected 8 middleware, got %d", len(chain))
	}
}

func TestNewChain_EndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	v := &testValidator{
		claims: Claims{Subject: "user-e2e", Email: "e2e@test.com"},
	}

	chain := NewChain(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		CORS: &CORSConfig{
			AllowedOrigins: []string{"https://example.com"},
		},
		RateLimiter: NewRateLimiter(ctx, RateLimitConfig{Rate: 100, Burst: 100}),
		Auth:        v,
	})

	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := GetClaims(r.Context())
		if !ok {
			t.Error("expected claims in context")
		}
		if claims.Subject != "user-e2e" {
			t.Errorf("expected user-e2e, got %q", claims.Subject)
		}

		id := GetRequestID(r.Context())
		if id == "" {
			t.Error("expected request ID in context")
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Apply middleware in reverse order (innermost first) since Chi applies left-to-right.
	for i := len(chain) - 1; i >= 0; i-- {
		handler = chain[i](handler)
	}

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("Authorization", "Bearer valid")
	req.Header.Set("Origin", "https://example.com")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Header().Get("X-Request-Id") == "" {
		t.Fatal("expected X-Request-Id in response")
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
		t.Fatal("expected CORS origin header")
	}
}

func TestNewChain_DefaultLogger(t *testing.T) {
	// Should not panic with nil Logger.
	chain := NewChain(Config{})
	if len(chain) != 3 {
		t.Fatalf("expected 3, got %d", len(chain))
	}
}
