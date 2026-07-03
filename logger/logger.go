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

	// ExtraHandlers are additional slog handlers that every record is
	// teed to, after the context correlation fields are injected. This
	// is the seam platform-kit/otel uses to also emit each log line as an
	// OTLP log record (trace-correlated) without any service coupling to
	// a backend. Nil (the default) keeps the single-sink behaviour.
	ExtraHandlers []slog.Handler
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

	// When extra sinks are configured (e.g. the OTLP log handler), tee the
	// enriched record to all of them. contextHandler stays the single place
	// correlation fields are injected, so both stdout and OTLP see them.
	inner := base
	if len(cfg.ExtraHandlers) > 0 {
		inner = &teeHandler{handlers: append([]slog.Handler{base}, cfg.ExtraHandlers...)}
	}

	return slog.New(&contextHandler{inner: inner})
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

// teeHandler fans one record out to several handlers. It sits inside the
// contextHandler, so it receives records already enriched with correlation
// fields. A handler that reports it is not enabled for a level is skipped;
// a handler error is collected but never blocks the others.
type teeHandler struct {
	handlers []slog.Handler
}

func (t *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range t.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (t *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range t.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (t *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(t.handlers))
	for i, h := range t.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &teeHandler{handlers: next}
}

func (t *teeHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(t.handlers))
	for i, h := range t.handlers {
		next[i] = h.WithGroup(name)
	}
	return &teeHandler{handlers: next}
}
