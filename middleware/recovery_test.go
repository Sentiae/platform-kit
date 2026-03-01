package middleware

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	platformerrors "github.com/sentiae/platform-kit/errors"
)

func TestRecovery_NoPanic(t *testing.T) {
	handler := Recovery(slog.New(slog.NewTextHandler(io.Discard, nil)))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		}),
	)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "ok" {
		t.Fatalf("expected body 'ok', got %q", rr.Body.String())
	}
}

func TestRecovery_CatchesPanic(t *testing.T) {
	handler := Recovery(slog.New(slog.NewTextHandler(io.Discard, nil)))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("something went wrong")
		}),
	)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/test", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "application/problem+json" {
		t.Fatalf("expected Content-Type application/problem+json, got %q", ct)
	}

	var resp platformerrors.ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != 500 {
		t.Fatalf("expected status 500 in body, got %d", resp.Status)
	}
	if resp.Detail != "internal server error" {
		t.Fatalf("expected detail 'internal server error', got %q", resp.Detail)
	}
}

func TestRecovery_CatchesNilPanic(t *testing.T) {
	handler := Recovery(slog.New(slog.NewTextHandler(io.Discard, nil)))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic(nil)
		}),
	)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

	// Go 1.21+ panic(nil) becomes a *runtime.PanicNilError, so recovery catches it.
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}
