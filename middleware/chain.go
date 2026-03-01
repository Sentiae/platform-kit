// Package middleware provides Chi-compatible HTTP middleware for the Sentiae platform.
//
// Use [NewChain] to build an ordered middleware stack from a [Config]:
//
//	chain := middleware.NewChain(middleware.Config{
//	    Logger: myLogger,
//	    CORS:   &middleware.CORSConfig{AllowedOrigins: []string{"https://app.example.com"}},
//	})
//	router.Use(chain...)
//
// The chain order is: Recovery → RequestID → Logging → Metrics → Tracing → CORS → RateLimit → Auth.
// Optional middleware (Metrics, Tracing, CORS, RateLimit, Auth) are only included
// when their corresponding config is non-nil.
package middleware

import (
	"log/slog"
	"net/http"
)

// Config configures the middleware chain returned by [NewChain].
type Config struct {
	// Logger used by the logging and recovery middleware.
	// Defaults to [slog.Default] if nil.
	Logger *slog.Logger

	// CORS configures CORS handling. If nil, CORS middleware is not included.
	CORS *CORSConfig

	// RateLimiter is a pre-created rate limiter. Create with [NewRateLimiter].
	// If nil, rate limiting middleware is not included.
	RateLimiter *RateLimiter

	// Auth validates JWT tokens. If nil, auth middleware is not included.
	Auth TokenValidator

	// Metrics is an optional custom middleware slot for metrics collection.
	Metrics func(http.Handler) http.Handler

	// Tracing is an optional custom middleware slot for distributed tracing.
	Tracing func(http.Handler) http.Handler
}

// NewChain returns an ordered slice of Chi-compatible middleware.
// The order is: Recovery → RequestID → Logging → Metrics → Tracing → CORS → RateLimit → Auth.
func NewChain(cfg Config) []func(http.Handler) http.Handler {
	l := cfg.Logger
	if l == nil {
		l = slog.Default()
	}

	var chain []func(http.Handler) http.Handler

	chain = append(chain, Recovery(l))
	chain = append(chain, RequestID)
	chain = append(chain, Logging(l))

	if cfg.Metrics != nil {
		chain = append(chain, cfg.Metrics)
	}
	if cfg.Tracing != nil {
		chain = append(chain, cfg.Tracing)
	}
	if cfg.CORS != nil {
		chain = append(chain, CORS(*cfg.CORS))
	}
	if cfg.RateLimiter != nil {
		chain = append(chain, cfg.RateLimiter.Middleware())
	}
	if cfg.Auth != nil {
		chain = append(chain, Auth(cfg.Auth))
	}

	return chain
}

// responseWriter wraps [http.ResponseWriter] to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *responseWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap returns the underlying [http.ResponseWriter] for Go 1.20+
// [http.ResponseController] support.
func (w *responseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
