package middleware

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	platformerrors "github.com/sentiae/platform-kit/errors"
	"golang.org/x/time/rate"
)

// RateLimitConfig configures a [RateLimiter].
type RateLimitConfig struct {
	// Rate is the number of allowed requests per second.
	Rate float64
	// Burst is the maximum burst size.
	Burst int
	// TTL is how long an idle client's limiter is kept before eviction.
	// Defaults to 5 minutes.
	TTL time.Duration
	// CleanupInterval controls how often expired entries are evicted.
	// Defaults to 1 minute.
	CleanupInterval time.Duration
}

// RateLimiter tracks per-client request rates with TTL-based eviction.
// Create one with [NewRateLimiter] and call [RateLimiter.Middleware] to get
// the Chi-compatible middleware.
type RateLimiter struct {
	visitors map[string]*visitor
	mu       sync.Mutex
	rate     rate.Limit
	burst    int
	ttl      time.Duration
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter creates a rate limiter and starts a background cleanup goroutine
// that evicts idle clients. The goroutine stops when ctx is cancelled.
func NewRateLimiter(ctx context.Context, cfg RateLimitConfig) *RateLimiter {
	ttl := cfg.TTL
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	interval := cfg.CleanupInterval
	if interval == 0 {
		interval = time.Minute
	}

	rl := &RateLimiter{
		visitors: make(map[string]*visitor),
		rate:     rate.Limit(cfg.Rate),
		burst:    cfg.Burst,
		ttl:      ttl,
	}

	go rl.cleanup(ctx, interval)
	return rl
}

// Middleware returns Chi-compatible rate-limiting middleware.
// Client IP is extracted from X-Forwarded-For, X-Real-Ip, or RemoteAddr (without port).
func (rl *RateLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := realIP(r)
			if !rl.allow(ip) {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusTooManyRequests)
				resp := platformerrors.ErrorResponse{
					Type:   "about:blank",
					Title:  http.StatusText(http.StatusTooManyRequests),
					Status: http.StatusTooManyRequests,
					Detail: "rate limit exceeded",
				}
				json.NewEncoder(w).Encode(resp)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (rl *RateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, ok := rl.visitors[ip]
	if !ok {
		v = &visitor{limiter: rate.NewLimiter(rl.rate, rl.burst)}
		rl.visitors[ip] = v
	}
	v.lastSeen = time.Now()
	return v.limiter.Allow()
}

func (rl *RateLimiter) cleanup(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rl.evict()
		}
	}
}

func (rl *RateLimiter) evict() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	for ip, v := range rl.visitors {
		if now.Sub(v.lastSeen) > rl.ttl {
			delete(rl.visitors, ip)
		}
	}
}

// realIP extracts the client IP from the request, preferring
// X-Forwarded-For, then X-Real-Ip, then RemoteAddr (without port).
func realIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
