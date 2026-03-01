package interceptor

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// UnaryLogging returns a unary server interceptor that logs the gRPC method,
// response status, and duration of every RPC call.
func UnaryLogging(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logRPC(ctx, logger, info.FullMethod, start, err)
		return resp, err
	}
}

// StreamLogging returns a stream server interceptor that logs the gRPC method,
// response status, and duration of every streaming RPC.
func StreamLogging(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		logRPC(ss.Context(), logger, info.FullMethod, start, err)
		return err
	}
}

func logRPC(ctx context.Context, logger *slog.Logger, method string, start time.Time, err error) {
	code := status.Code(err)
	logger.InfoContext(ctx, "rpc completed",
		slog.String("method", method),
		slog.String("code", code.String()),
		slog.Duration("duration", time.Since(start)),
	)
}
