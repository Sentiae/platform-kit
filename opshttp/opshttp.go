// Package opshttp mounts a service's uniform operational HTTP surface in one
// call, so every Sentiae service exposes the SAME liveness, readiness, security
// posture, and consumer-lag endpoints — the Wave-8 "boot-surface" contract.
//
// A service wires it once, in its Wave-8 pass:
//
//	opshttp.Mount(router, opshttp.Config{
//	    Service: "ops",
//	    Health:  health.Config{Checks: []health.Checker{health.DatabaseChecker(db), ...}},
//	    Posture: postureSet,                                    // from posture.Declare(...)
//	    ConsumersHandler: kafka.ConsumersHealthzHandler("ops", consumer),
//	})
//
// The point is uniformity + fail-closed defaults: /ready refuses to report ready
// with zero checkers (health), /posture makes "is anything fail-open?" a query,
// and /healthz/consumers surfaces DLQ activity. A missing piece is VISIBLE, never
// a silent 200.
package opshttp

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/sentiae/platform-kit/health"
	"github.com/sentiae/platform-kit/posture"
)

// Muxer is the minimal routing surface Mount needs. Satisfied by *http.ServeMux
// and chi.Router alike (both expose Handle(pattern, http.Handler)).
type Muxer interface {
	Handle(pattern string, handler http.Handler)
}

// Config declares the ops surface for one service.
type Config struct {
	// Service names the service in the /posture payload.
	Service string

	// Health drives /health (liveness) and /ready (readiness). With zero checkers
	// and no NoChecksReason, /ready fails closed (health package contract).
	Health health.Config

	// Posture is the declared security-control set (posture.Declare(...)). /posture
	// reports each control's boot result. Nil is legal but reported as "not
	// declared" — a service with no declared posture is itself a signal.
	Posture *posture.Set

	// ConsumersHandler, when set (kafka.ConsumersHealthzHandler(...)), is mounted
	// at /healthz/consumers so Kafka lag + DLQ activity is observable.
	ConsumersHandler http.Handler
}

// Mount registers the standard ops endpoints on mux:
//
//	/health, /healthz   — liveness (always 200 while the process serves)
//	/ready,  /readyz     — readiness (fail-closed on zero checkers)
//	/posture             — declared security controls + their boot result
//	/healthz/consumers   — Kafka consumer lag + DLQ (only if ConsumersHandler set)
func Mount(mux Muxer, cfg Config) {
	h := health.NewHandler(cfg.Health)
	mux.Handle("/health", h.LivenessHandler())
	mux.Handle("/healthz", h.LivenessHandler())
	mux.Handle("/ready", h.ReadinessHandler())
	mux.Handle("/readyz", h.ReadinessHandler())
	mux.Handle("/posture", PostureHandler(cfg.Service, cfg.Posture))
	if cfg.ConsumersHandler != nil {
		mux.Handle("/healthz/consumers", cfg.ConsumersHandler)
	}
}

// postureStatus is the JSON-friendly projection of a posture.Status (its Kind,
// Result and Err are not directly marshalable to readable JSON).
type postureStatus struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
	Err    string `json:"error,omitempty"`
}

type postureResponse struct {
	Service     string          `json:"service"`
	Ok          bool            `json:"ok"`
	Declared    bool            `json:"declared"`
	Controls    []postureStatus `json:"controls,omitempty"`
	GeneratedAt time.Time       `json:"generated_at"`
}

// PostureHandler serves the declared security posture as JSON. It returns 503
// when any control's last result is a failure (a service that boots with Hold
// enforced never reaches this, but a report drift is surfaced, not hidden), and
// when no posture is declared at all (a missing security declaration is a
// fail-open, not a pass).
func PostureHandler(service string, set *posture.Set) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := postureResponse{Service: service, GeneratedAt: time.Now().UTC()}
		if set == nil {
			resp.Ok = false
			resp.Declared = false
			writeJSON(w, http.StatusServiceUnavailable, resp)
			return
		}
		resp.Declared = true
		resp.Ok = true
		for _, s := range set.Report() {
			ps := postureStatus{
				Name:   s.Name,
				Kind:   s.Kind.String(),
				Result: s.Result.String(),
				Reason: s.Reason,
			}
			if s.Err != nil {
				ps.Err = s.Err.Error()
			}
			if s.Result == posture.ResultFailed {
				resp.Ok = false
			}
			resp.Controls = append(resp.Controls, ps)
		}
		status := http.StatusOK
		if !resp.Ok {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, resp)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
