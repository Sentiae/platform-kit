// Package interceptor provides gRPC server interceptors for the Sentiae platform.
//
// Use [NewUnaryChain] and [NewStreamChain] to build ordered interceptor stacks:
//
//	unary, stream := interceptor.NewChain(interceptor.Config{
//	    Logger: myLogger,
//	    Auth:   &interceptor.AuthConfig{TokenValidator: myValidator},
//	})
//	srv := grpc.NewServer(
//	    grpc.ChainUnaryInterceptor(unary...),
//	    grpc.ChainStreamInterceptor(stream...),
//	)
//
// The chain order is: Recovery → Logging → Metrics → Auth.
// Optional interceptors (Metrics, Auth) are only included when their config is non-nil.
package interceptor

import (
	"log/slog"

	"google.golang.org/grpc"
)

// Config configures the interceptor chains returned by [NewChain].
type Config struct {
	// Logger used by the logging and recovery interceptors.
	// Defaults to [slog.Default] if nil.
	Logger *slog.Logger

	// Metrics is an optional metrics collector. If nil, metrics interceptors are not included.
	Metrics MetricsCollector

	// Auth configures authentication. If nil, auth interceptors are not included.
	Auth *AuthConfig
}

// NewChain returns ordered slices of unary and stream server interceptors.
// The order is: Recovery → Logging → Metrics → Auth.
func NewChain(cfg Config) ([]grpc.UnaryServerInterceptor, []grpc.StreamServerInterceptor) {
	l := cfg.Logger
	if l == nil {
		l = slog.Default()
	}

	var unary []grpc.UnaryServerInterceptor
	var stream []grpc.StreamServerInterceptor

	// Recovery first (outermost).
	unary = append(unary, UnaryRecovery(l))
	stream = append(stream, StreamRecovery(l))

	// Logging.
	unary = append(unary, UnaryLogging(l))
	stream = append(stream, StreamLogging(l))

	// Metrics (optional).
	if cfg.Metrics != nil {
		unary = append(unary, UnaryMetrics(cfg.Metrics))
		stream = append(stream, StreamMetrics(cfg.Metrics))
	}

	// Auth (optional, innermost).
	if cfg.Auth != nil {
		authCfg := *cfg.Auth
		if authCfg.Logger == nil {
			authCfg.Logger = l
		}
		unary = append(unary, UnaryAuth(authCfg))
		stream = append(stream, StreamAuth(authCfg))
	}

	return unary, stream
}
