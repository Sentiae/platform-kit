package interceptor

import (
	"context"
	"log/slog"
	"strings"

	"github.com/sentiae/platform-kit/logger"
	"github.com/sentiae/platform-kit/middleware"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// APIKeyValidator validates service-to-service API keys.
type APIKeyValidator interface {
	Validate(ctx context.Context, key string) error
}

// AuthConfig configures the auth interceptor.
type AuthConfig struct {
	// TokenValidator validates JWT Bearer tokens. If nil, JWT auth is disabled.
	TokenValidator middleware.TokenValidator

	// APIKeyValidator validates service-to-service API keys. If nil, API key auth is disabled.
	APIKeyValidator APIKeyValidator

	// SkipMethods is a list of fully-qualified gRPC methods to skip auth for
	// (e.g., health checks, reflection).
	SkipMethods []string

	// Logger is used for auth-related logging. Defaults to slog.Default().
	Logger *slog.Logger
}

// UnaryAuth returns a unary server interceptor that authenticates requests
// using either JWT Bearer tokens or service-to-service API keys from gRPC metadata.
func UnaryAuth(cfg AuthConfig) grpc.UnaryServerInterceptor {
	skipSet := buildSkipSet(cfg.SkipMethods)
	l := cfg.Logger
	if l == nil {
		l = slog.Default()
	}

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if _, ok := skipSet[info.FullMethod]; ok {
			return handler(ctx, req)
		}

		newCtx, err := authenticate(ctx, cfg, l)
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}

// StreamAuth returns a stream server interceptor that authenticates requests
// using either JWT Bearer tokens or service-to-service API keys from gRPC metadata.
func StreamAuth(cfg AuthConfig) grpc.StreamServerInterceptor {
	skipSet := buildSkipSet(cfg.SkipMethods)
	l := cfg.Logger
	if l == nil {
		l = slog.Default()
	}

	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if _, ok := skipSet[info.FullMethod]; ok {
			return handler(srv, ss)
		}

		newCtx, err := authenticate(ss.Context(), cfg, l)
		if err != nil {
			return err
		}
		return handler(srv, &wrappedServerStream{ServerStream: ss, ctx: newCtx})
	}
}

func authenticate(ctx context.Context, cfg AuthConfig, l *slog.Logger) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "missing metadata")
	}

	// Try API key first (service-to-service).
	if cfg.APIKeyValidator != nil {
		if keys := md.Get("x-api-key"); len(keys) > 0 {
			if err := cfg.APIKeyValidator.Validate(ctx, keys[0]); err != nil {
				l.WarnContext(ctx, "api key validation failed", "error", err)
				return ctx, status.Error(codes.Unauthenticated, "invalid api key")
			}
			return ctx, nil
		}
	}

	// Try JWT Bearer token.
	if cfg.TokenValidator != nil {
		if auths := md.Get("authorization"); len(auths) > 0 {
			parts := strings.SplitN(auths[0], " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
				return ctx, status.Error(codes.Unauthenticated, "invalid authorization header format")
			}

			claims, err := cfg.TokenValidator.Validate(ctx, parts[1])
			if err != nil {
				l.WarnContext(ctx, "token validation failed", "error", err)
				return ctx, status.Error(codes.Unauthenticated, "invalid or expired token")
			}

			ctx = context.WithValue(ctx, logger.UserIDKey, claims.Subject)
			ctx = context.WithValue(ctx, claimsCtxKey{}, claims)
			return ctx, nil
		}
	}

	return ctx, status.Error(codes.Unauthenticated, "no credentials provided")
}

// claimsCtxKey is the unexported context key for storing parsed JWT claims in gRPC context.
type claimsCtxKey struct{}

// GetClaims returns the JWT claims from the gRPC context, or zero-value Claims if not set.
func GetClaims(ctx context.Context) (middleware.Claims, bool) {
	c, ok := ctx.Value(claimsCtxKey{}).(middleware.Claims)
	return c, ok
}

func buildSkipSet(methods []string) map[string]struct{} {
	m := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		m[method] = struct{}{}
	}
	return m
}

// wrappedServerStream wraps a grpc.ServerStream with a modified context.
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}
