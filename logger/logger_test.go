package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/sentiae/platform-kit/logger"
)

func TestNew_DefaultsToJSONInfo(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(logger.Config{Writer: &buf})

	l.Info("hello")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("expected JSON output, got: %s", buf.String())
	}
	if entry["msg"] != "hello" {
		t.Errorf("msg = %v, want hello", entry["msg"])
	}
	if entry["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", entry["level"])
	}
}

func TestNew_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(logger.Config{Format: "text", Writer: &buf})

	l.Info("text log")

	out := buf.String()
	if json.Valid(buf.Bytes()) {
		t.Errorf("expected non-JSON text output, got: %s", out)
	}
	if !strings.Contains(out, "text log") {
		t.Errorf("output does not contain message: %s", out)
	}
}

func TestNew_LevelFiltering(t *testing.T) {
	tests := []struct {
		level   string
		logFunc func(*slog.Logger, string)
		want    bool // true if the message should appear
	}{
		{"error", func(l *slog.Logger, m string) { l.Warn(m) }, false},
		{"error", func(l *slog.Logger, m string) { l.Error(m) }, true},
		{"warn", func(l *slog.Logger, m string) { l.Info(m) }, false},
		{"warn", func(l *slog.Logger, m string) { l.Warn(m) }, true},
		{"debug", func(l *slog.Logger, m string) { l.Debug(m) }, true},
		{"info", func(l *slog.Logger, m string) { l.Debug(m) }, false},
		{"info", func(l *slog.Logger, m string) { l.Info(m) }, true},
	}

	for _, tt := range tests {
		var buf bytes.Buffer
		l := logger.New(logger.Config{Level: tt.level, Writer: &buf})
		tt.logFunc(l, "test")

		got := buf.Len() > 0
		if got != tt.want {
			t.Errorf("level=%s: output present=%v, want %v", tt.level, got, tt.want)
		}
	}
}

func TestNew_UnknownLevelDefaultsToInfo(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(logger.Config{Level: "bogus", Writer: &buf})

	l.Debug("should not appear")
	if buf.Len() > 0 {
		t.Error("debug message appeared with default info level")
	}

	l.Info("should appear")
	if buf.Len() == 0 {
		t.Error("info message did not appear with default info level")
	}
}

func TestContextHandler_InjectsCorrelationFields(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(logger.Config{Writer: &buf})

	ctx := context.Background()
	ctx = context.WithValue(ctx, logger.TraceIDKey, "abc-trace")
	ctx = context.WithValue(ctx, logger.SpanIDKey, "def-span")
	ctx = context.WithValue(ctx, logger.RequestIDKey, "req-123")
	ctx = context.WithValue(ctx, logger.UserIDKey, "user-456")

	l.InfoContext(ctx, "correlated")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON: %s", buf.String())
	}

	wantFields := map[string]string{
		"trace_id":   "abc-trace",
		"span_id":    "def-span",
		"request_id": "req-123",
		"user_id":    "user-456",
	}
	for key, want := range wantFields {
		got, ok := entry[key].(string)
		if !ok || got != want {
			t.Errorf("%s = %v, want %q", key, entry[key], want)
		}
	}
}

func TestContextHandler_OmitsEmptyFields(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(logger.Config{Writer: &buf})

	// Only set request_id; others should be omitted.
	ctx := context.WithValue(context.Background(), logger.RequestIDKey, "req-only")
	l.InfoContext(ctx, "partial")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON: %s", buf.String())
	}

	if entry["request_id"] != "req-only" {
		t.Errorf("request_id = %v, want req-only", entry["request_id"])
	}
	for _, key := range []string{"trace_id", "span_id", "user_id"} {
		if _, exists := entry[key]; exists {
			t.Errorf("unexpected key %q in output", key)
		}
	}
}

func TestFromContext_ReturnsStoredLogger(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(logger.Config{Writer: &buf})

	ctx := logger.NewContext(context.Background(), l)
	got := logger.FromContext(ctx)

	got.Info("from context")

	if buf.Len() == 0 {
		t.Error("expected logger from context to write to buffer")
	}
}

func TestFromContext_FallsBackToDefault(t *testing.T) {
	got := logger.FromContext(context.Background())
	if got == nil {
		t.Fatal("FromContext returned nil for empty context")
	}
	// Should be the slog default logger.
	if got != slog.Default() {
		t.Error("expected slog.Default() as fallback")
	}
}

func TestWithAttrs_PreservesContextInjection(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(logger.Config{Writer: &buf})
	l = l.With("service", "test-svc")

	ctx := context.WithValue(context.Background(), logger.RequestIDKey, "req-with-attrs")
	l.InfoContext(ctx, "with attrs")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON: %s", buf.String())
	}

	if entry["service"] != "test-svc" {
		t.Errorf("service = %v, want test-svc", entry["service"])
	}
	if entry["request_id"] != "req-with-attrs" {
		t.Errorf("request_id = %v, want req-with-attrs", entry["request_id"])
	}
}

func TestWithGroup_PreservesContextInjection(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(logger.Config{Writer: &buf})
	l = l.WithGroup("http")

	ctx := context.WithValue(context.Background(), logger.UserIDKey, "user-grouped")
	l.InfoContext(ctx, "grouped", "method", "GET")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON: %s", buf.String())
	}

	// Both context-injected and explicit attrs are nested under the group.
	group, ok := entry["http"].(map[string]any)
	if !ok {
		t.Fatalf("expected http group in output, got: %v", entry)
	}
	if group["user_id"] != "user-grouped" {
		t.Errorf("http.user_id = %v, want user-grouped", group["user_id"])
	}
	if group["method"] != "GET" {
		t.Errorf("http.method = %v, want GET", group["method"])
	}
}
