// Package logger provides structured logging built on [log/slog] with automatic
// injection of correlation fields (trace_id, span_id, request_id, user_id) from
// context. It supports JSON output for production and text output for development.
//
// Usage:
//
//	l := logger.New(logger.Config{Level: "info", Format: "json"})
//	ctx := logger.NewContext(ctx, l)
//	logger.FromContext(ctx).Info("request handled", "status", 200)
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Config controls logger creation.
type Config struct {
	// Level is the minimum log level: "debug", "info", "warn", "error".
	// Defaults to "info".
	Level string

	// Format is the output format: "json" or "text".
	// Use "text" for local development, "json" for production.
	// Defaults to "json".
	Format string

	// Writer is the output destination. Defaults to os.Stdout.
	Writer io.Writer
}

// ContextKey is a typed key for context values that are automatically
// extracted into log attributes by the contextHandler.
type ContextKey string

const (
	TraceIDKey   ContextKey = "trace_id"
	SpanIDKey    ContextKey = "span_id"
	RequestIDKey ContextKey = "request_id"
	UserIDKey    ContextKey = "user_id"
)

// contextKeys lists the keys extracted on every log call, in the order they
// appear in the log output.
var contextKeys = []ContextKey{TraceIDKey, SpanIDKey, RequestIDKey, UserIDKey}

// loggerCtxKey is the unexported key used to store/retrieve a *slog.Logger in context.
type loggerCtxKey struct{}

// New creates a [*slog.Logger] configured with the given options.
// The returned logger uses a handler that automatically injects correlation
// fields from the context on every log call.
func New(cfg Config) *slog.Logger {
	w := cfg.Writer
	if w == nil {
		w = os.Stdout
	}

	level := parseLevel(cfg.Level)
	opts := &slog.HandlerOptions{Level: level}

	var base slog.Handler
	if strings.EqualFold(cfg.Format, "text") {
		base = slog.NewTextHandler(w, opts)
	} else {
		base = slog.NewJSONHandler(w, opts)
	}

	return slog.New(&contextHandler{inner: base})
}

// FromContext returns the logger stored in ctx via [NewContext].
// If no logger is found, the slog default logger is returned.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerCtxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// NewContext returns a copy of ctx carrying the given logger.
func NewContext(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerCtxKey{}, l)
}

// parseLevel converts a string to an slog.Level. Unrecognised values default to Info.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// contextHandler wraps an [slog.Handler] and injects well-known correlation
// fields from the context into every log record.
type contextHandler struct {
	inner slog.Handler
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, key := range contextKeys {
		if v, ok := ctx.Value(key).(string); ok && v != "" {
			r.AddAttrs(slog.String(string(key), v))
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{inner: h.inner.WithGroup(name)}
}
