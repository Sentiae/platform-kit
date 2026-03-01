// Package health provides standardized health and readiness endpoints for Sentiae services.
//
// Use [NewHandler] to create an HTTP handler that exposes /health (liveness)
// and /ready (readiness) endpoints:
//
//	h := health.NewHandler(health.Config{
//	    Checks: []health.Checker{
//	        health.DatabaseChecker(db),
//	        health.RedisChecker(redisClient),
//	    },
//	})
//	mux.Handle("/health", h)
//	mux.Handle("/ready", h)
//
// Liveness (/health) always returns 200 OK with basic status.
// Readiness (/ready) runs all registered [Checker] functions and returns
// a JSON response with individual component statuses.
package health

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Status represents the health state of a component.
type Status string

const (
	StatusUp   Status = "up"
	StatusDown Status = "down"
)

// ComponentStatus holds the health result for a single component.
type ComponentStatus struct {
	Name    string `json:"name"`
	Status  Status `json:"status"`
	Message string `json:"message,omitempty"`
}

// Response is the JSON body returned by health endpoints.
type Response struct {
	Status     Status            `json:"status"`
	Components []ComponentStatus `json:"components,omitempty"`
}

// Checker performs a health check for a single component.
// It returns the component name, status, and an optional error message.
type Checker interface {
	Check(ctx context.Context) ComponentStatus
}

// CheckerFunc adapts a plain function to the [Checker] interface.
type CheckerFunc func(ctx context.Context) ComponentStatus

func (f CheckerFunc) Check(ctx context.Context) ComponentStatus { return f(ctx) }

// SQLPinger is satisfied by [*sql.DB] and [*gorm.DB] (via its SqlDB() method result).
type SQLPinger interface {
	PingContext(ctx context.Context) error
}

// DatabaseChecker returns a [Checker] that pings a SQL database.
func DatabaseChecker(db SQLPinger) Checker {
	return CheckerFunc(func(ctx context.Context) ComponentStatus {
		cs := ComponentStatus{Name: "database", Status: StatusUp}
		if err := db.PingContext(ctx); err != nil {
			cs.Status = StatusDown
			cs.Message = err.Error()
		}
		return cs
	})
}

// RedisChecker returns a [Checker] that pings a Redis client.
// The client must implement a Ping method (e.g., go-redis Client).
func RedisChecker(client RedisPinger) Checker {
	return CheckerFunc(func(ctx context.Context) ComponentStatus {
		cs := ComponentStatus{Name: "redis", Status: StatusUp}
		if err := client.Ping(ctx); err != nil {
			cs.Status = StatusDown
			cs.Message = err.Error()
		}
		return cs
	})
}

// RedisPinger is satisfied by Redis clients that support Ping.
type RedisPinger interface {
	Ping(ctx context.Context) error
}

// KafkaChecker returns a [Checker] that verifies Kafka broker connectivity
// by establishing a TCP connection to one of the provided broker addresses.
func KafkaChecker(dialer KafkaDialer) Checker {
	return CheckerFunc(func(ctx context.Context) ComponentStatus {
		cs := ComponentStatus{Name: "kafka", Status: StatusUp}
		if err := dialer.Dial(ctx); err != nil {
			cs.Status = StatusDown
			cs.Message = err.Error()
		}
		return cs
	})
}

// KafkaDialer verifies connectivity to a Kafka broker.
type KafkaDialer interface {
	Dial(ctx context.Context) error
}

// Config configures the health handler.
type Config struct {
	// Checks are the readiness checkers to run. Optional.
	Checks []Checker

	// Timeout for each individual check. Defaults to 5s.
	Timeout time.Duration
}

// Handler serves /health and /ready endpoints.
type Handler struct {
	checks  []Checker
	timeout time.Duration
}

// NewHandler creates a health [Handler] from the given [Config].
func NewHandler(cfg Config) *Handler {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &Handler{
		checks:  cfg.Checks,
		timeout: timeout,
	}
}

// ServeHTTP dispatches to liveness or readiness based on the request path.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/ready":
		h.handleReady(w, r)
	default:
		h.handleHealth(w, r)
	}
}

// LivenessHandler returns an [http.HandlerFunc] for the /health (liveness) endpoint.
func (h *Handler) LivenessHandler() http.HandlerFunc {
	return h.handleHealth
}

// ReadinessHandler returns an [http.HandlerFunc] for the /ready (readiness) endpoint.
func (h *Handler) ReadinessHandler() http.HandlerFunc {
	return h.handleReady
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Response{Status: StatusUp})
}

func (h *Handler) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	components := h.runChecks(ctx)
	overall := StatusUp
	for _, c := range components {
		if c.Status == StatusDown {
			overall = StatusDown
			break
		}
	}

	status := http.StatusOK
	if overall == StatusDown {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, Response{Status: overall, Components: components})
}

func (h *Handler) runChecks(ctx context.Context) []ComponentStatus {
	if len(h.checks) == 0 {
		return nil
	}

	results := make([]ComponentStatus, len(h.checks))
	var wg sync.WaitGroup
	wg.Add(len(h.checks))
	for i, check := range h.checks {
		go func(idx int, c Checker) {
			defer wg.Done()
			results[idx] = c.Check(ctx)
		}(i, check)
	}
	wg.Wait()
	return results
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// Ensure *sql.DB satisfies SQLPinger at compile time.
var _ SQLPinger = (*sql.DB)(nil)
