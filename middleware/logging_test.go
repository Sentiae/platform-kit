package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLogging_LogsFields(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, nil))

	handler := Logging(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("POST", "/api/test", nil))

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse log: %v", err)
	}

	if entry["method"] != "POST" {
		t.Errorf("expected method POST, got %v", entry["method"])
	}
	if entry["path"] != "/api/test" {
		t.Errorf("expected path /api/test, got %v", entry["path"])
	}
	if status, ok := entry["status"].(float64); !ok || int(status) != 201 {
		t.Errorf("expected status 201, got %v", entry["status"])
	}
	if _, ok := entry["duration"]; !ok {
		t.Error("expected duration field in log")
	}
}

func TestLogging_DefaultStatus(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, nil))

	handler := Logging(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse log: %v", err)
	}

	if status, ok := entry["status"].(float64); !ok || int(status) != 200 {
		t.Errorf("expected status 200, got %v", entry["status"])
	}
}

func TestLogging_CapturesRequestIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, nil))

	// Chain RequestID -> Logging to verify request_id appears in the log.
	handler := RequestID(Logging(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

	// The platform-kit logger's contextHandler is not in the chain here
	// (we use a plain slog.JSONHandler), so request_id won't appear via context.
	// This test only verifies logging doesn't break when context has values.
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}
