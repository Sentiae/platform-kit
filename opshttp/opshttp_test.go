package opshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sentiae/platform-kit/health"
	"github.com/sentiae/platform-kit/posture"
)

func get(t *testing.T, h http.Handler, path string) (int, postureResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var pr postureResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &pr)
	return rec.Code, pr
}

func TestMount_RegistersUniformSurface(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, Config{
		Service: "test",
		Health:  health.Config{NoChecksReason: "no deps in this test"},
	})

	// liveness always up
	if code, _ := get(t, mux, "/health"); code != http.StatusOK {
		t.Errorf("/health = %d, want 200", code)
	}
	// readiness: explicit no-checks opt-out → 200
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/ready (explicit no-checks) = %d, want 200", rec.Code)
	}
}

func TestReady_FailsClosed_WhenNoChecksAndNoOptOut(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, Config{Service: "test", Health: health.Config{}}) // zero checkers, no opt-out
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ready with zero checkers = %d, want 503 (fail-closed)", rec.Code)
	}
}

func TestPosture_NilSet_FailsClosed(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, Config{Service: "test", Health: health.Config{NoChecksReason: "n/a"}}) // Posture nil
	code, pr := get(t, mux, "/posture")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("/posture with no declared posture = %d, want 503", code)
	}
	if pr.Declared {
		t.Fatalf("expected declared=false")
	}
}

func TestPosture_ReportsControls(t *testing.T) {
	// A passing control and a failing one; after Hold, /posture must reflect both
	// and fail closed (503) because one control failed.
	set, err := posture.Declare(
		posture.Control{Name: "good", Assert: func(context.Context) error { return nil }},
		posture.Control{Name: "bad", Assert: func(context.Context) error { return errors.New("boom") }},
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = set.Hold(context.Background()) // will error; we only need the recorded results

	mux := http.NewServeMux()
	Mount(mux, Config{Service: "test", Health: health.Config{NoChecksReason: "n/a"}, Posture: set})
	code, pr := get(t, mux, "/posture")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("/posture with a failed control = %d, want 503", code)
	}
	if !pr.Declared || pr.Ok {
		t.Fatalf("expected declared=true ok=false, got declared=%v ok=%v", pr.Declared, pr.Ok)
	}
	if len(pr.Controls) != 2 {
		t.Fatalf("expected 2 controls, got %d", len(pr.Controls))
	}
	var sawBadErr bool
	for _, c := range pr.Controls {
		if c.Name == "bad" && c.Err == "boom" {
			sawBadErr = true
		}
	}
	if !sawBadErr {
		t.Fatalf("expected the failed control's error surfaced, got %+v", pr.Controls)
	}
}

func TestMount_ConsumersHandler(t *testing.T) {
	called := false
	mux := http.NewServeMux()
	Mount(mux, Config{
		Service: "test",
		Health:  health.Config{NoChecksReason: "n/a"},
		ConsumersHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}),
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz/consumers", nil))
	if !called {
		t.Fatalf("/healthz/consumers handler was not invoked")
	}
}
