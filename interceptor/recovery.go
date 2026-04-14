package interceptor

import (
	"context"
	"log/slog"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UnaryRecovery returns a unary server interceptor that catches panics and
// converts them to gRPC Internal errors instead of crashing the process.
func UnaryRecovery(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (_ any, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := string(debug.Stack())
				logger.ErrorContext(ctx, "panic recovered",
					"panic", rec,
					"stack", stack,
					"method", info.FullMethod,
				)
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}

// StreamRecovery returns a stream server interceptor that catches panics and
// converts them to gRPC Internal errors instead of crashing the process.
func StreamRecovery(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := string(debug.Stack())
				logger.ErrorContext(ss.Context(), "panic recovered",
					"panic", rec,
					"stack", stack,
					"method", info.FullMethod,
				)
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()
		return handler(srv, ss)
	}
}
