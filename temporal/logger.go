package temporal

import (
	"log/slog"

	"go.temporal.io/sdk/log"
)

// sdkLogger adapts *slog.Logger to Temporal SDK's log.Logger interface.
type sdkLogger struct {
	l *slog.Logger
}

func newSDKLogger(l *slog.Logger) log.Logger {
	return &sdkLogger{l: l.With("component", "temporal_sdk")}
}

func (s *sdkLogger) Debug(msg string, keyvals ...any) { s.l.Debug(msg, keyvals...) }
func (s *sdkLogger) Info(msg string, keyvals ...any)  { s.l.Info(msg, keyvals...) }
func (s *sdkLogger) Warn(msg string, keyvals ...any)  { s.l.Warn(msg, keyvals...) }
func (s *sdkLogger) Error(msg string, keyvals ...any) { s.l.Error(msg, keyvals...) }
