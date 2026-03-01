package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	platformerrors "github.com/sentiae/platform-kit/errors"
)

func TestRateLimiter_AllowsWithinLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rl := NewRateLimiter(ctx, RateLimitConfig{
		Rate:  10,
		Burst: 10,
	})

	handler := rl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestRateLimiter_BlocksExcessRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rl := NewRateLimiter(ctx, RateLimitConfig{
		Rate:  1,
		Burst: 1,
	})

	handler := rl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request should succeed (burst=1).
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", rr.Code)
	}

	// Second request should be rate-limited.
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: expected 429, got %d", rr.Code)
	}

	// Verify JSON error response.
	var resp platformerrors.ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Status != 429 {
		t.Fatalf("expected status 429 in body, got %d", resp.Status)
	}
}

func TestRateLimiter_PerIPIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rl := NewRateLimiter(ctx, RateLimitConfig{
		Rate:  1,
		Burst: 1,
	})

	handler := rl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First IP exhausts its limit.
	req1 := httptest.NewRequest("GET", "/", nil)
	req1.Header.Set("X-Forwarded-For", "1.1.1.1")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req1)
	if rr.Code != http.StatusOK {
		t.Fatalf("IP1 first: expected 200, got %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req1)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("IP1 second: expected 429, got %d", rr.Code)
	}

	// Second IP should still be allowed.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("X-Forwarded-For", "2.2.2.2")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req2)
	if rr.Code != http.StatusOK {
		t.Fatalf("IP2: expected 200, got %d", rr.Code)
	}
}

func TestRateLimiter_TTLEviction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rl := NewRateLimiter(ctx, RateLimitConfig{
		Rate:            1,
		Burst:           1,
		TTL:             50 * time.Millisecond,
		CleanupInterval: 25 * time.Millisecond,
	})

	// Create a visitor.
	rl.allow("1.1.1.1")

	rl.mu.Lock()
	if len(rl.visitors) != 1 {
		t.Fatalf("expected 1 visitor, got %d", len(rl.visitors))
	}
	rl.mu.Unlock()

	// Wait for TTL + cleanup interval.
	time.Sleep(100 * time.Millisecond)

	rl.mu.Lock()
	count := len(rl.visitors)
	rl.mu.Unlock()

	if count != 0 {
		t.Fatalf("expected 0 visitors after eviction, got %d", count)
	}
}

func TestRateLimiter_ShutdownStopsCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	rl := NewRateLimiter(ctx, RateLimitConfig{
		Rate:            1,
		Burst:           1,
		CleanupInterval: 10 * time.Millisecond,
	})
	_ = rl

	// Cancel should cause the cleanup goroutine to exit (no goroutine leak).
	cancel()
	// Brief sleep to let goroutine exit.
	time.Sleep(20 * time.Millisecond)
}

func TestRealIP(t *testing.T) {
	tests := []struct {
		name       string
		xff        string
		xri        string
		remoteAddr string
		want       string
	}{
		{
			name: "X-Forwarded-For single",
			xff:  "1.2.3.4",
			want: "1.2.3.4",
		},
		{
			name: "X-Forwarded-For chain",
			xff:  "1.2.3.4, 5.6.7.8, 9.10.11.12",
			want: "1.2.3.4",
		},
		{
			name: "X-Real-Ip",
			xri:  "10.0.0.1",
			want: "10.0.0.1",
		},
		{
			name:       "RemoteAddr with port",
			remoteAddr: "192.168.1.1:12345",
			want:       "192.168.1.1",
		},
		{
			name:       "RemoteAddr without port",
			remoteAddr: "192.168.1.1",
			want:       "192.168.1.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Del("X-Forwarded-For")
			req.Header.Del("X-Real-Ip")
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xri != "" {
				req.Header.Set("X-Real-Ip", tt.xri)
			}
			if tt.remoteAddr != "" {
				req.RemoteAddr = tt.remoteAddr
			}

			got := realIP(req)
			if got != tt.want {
				t.Errorf("realIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
