package interceptor

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/sentiae/platform-kit/logger"
	"github.com/sentiae/platform-kit/middleware"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// fakeTokenValidator is a test TokenValidator implementation.
type fakeTokenValidator struct {
	claims middleware.Claims
	err    error
}

func (f *fakeTokenValidator) Validate(_ context.Context, _ string) (middleware.Claims, error) {
	return f.claims, f.err
}

// fakeAPIKeyValidator is a test APIKeyValidator implementation.
type fakeAPIKeyValidator struct {
	validKey string
}

func (f *fakeAPIKeyValidator) Validate(_ context.Context, key string) error {
	if key == f.validKey {
		return nil
	}
	return errors.New("invalid key")
}

func TestUnaryAuth_NoMetadata(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := UnaryAuth(AuthConfig{
		TokenValidator: &fakeTokenValidator{},
		Logger:         l,
	})

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	// Context without metadata.
	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test/Auth"}, handler)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", st.Code())
	}
}

func TestUnaryAuth_ValidBearerToken(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := UnaryAuth(AuthConfig{
		TokenValidator: &fakeTokenValidator{
			claims: middleware.Claims{
				Subject: "user-123",
				Email:   "test@example.com",
			},
		},
		Logger: l,
	})

	var capturedCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		capturedCtx = ctx
		return "ok", nil
	}

	md := metadata.New(map[string]string{"authorization": "Bearer valid-token"})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Auth"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected 'ok', got %v", resp)
	}

	// Verify claims are in context.
	claims, ok := GetClaims(capturedCtx)
	if !ok {
		t.Fatal("expected claims in context")
	}
	if claims.Subject != "user-123" {
		t.Fatalf("expected subject 'user-123', got %q", claims.Subject)
	}

	// Verify user ID is in context.
	userID, ok := capturedCtx.Value(logger.UserIDKey).(string)
	if !ok || userID != "user-123" {
		t.Fatalf("expected user ID 'user-123' in context, got %q", userID)
	}
}

func TestUnaryAuth_InvalidToken(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := UnaryAuth(AuthConfig{
		TokenValidator: &fakeTokenValidator{
			err: errors.New("token expired"),
		},
		Logger: l,
	})

	handler := func(ctx context.Context, req any) (any, error) {
		t.Fatal("handler should not be called")
		return nil, nil
	}

	md := metadata.New(map[string]string{"authorization": "Bearer expired-token"})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Auth"}, handler)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", st.Code())
	}
}

func TestUnaryAuth_InvalidHeaderFormat(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := UnaryAuth(AuthConfig{
		TokenValidator: &fakeTokenValidator{},
		Logger:         l,
	})

	handler := func(ctx context.Context, req any) (any, error) {
		t.Fatal("handler should not be called")
		return nil, nil
	}

	md := metadata.New(map[string]string{"authorization": "Basic abc123"})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Auth"}, handler)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", st.Code())
	}
}

func TestUnaryAuth_ValidAPIKey(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := UnaryAuth(AuthConfig{
		APIKeyValidator: &fakeAPIKeyValidator{validKey: "secret-key"},
		Logger:          l,
	})

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	md := metadata.New(map[string]string{"x-api-key": "secret-key"})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Auth"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected 'ok', got %v", resp)
	}
}

func TestUnaryAuth_InvalidAPIKey(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := UnaryAuth(AuthConfig{
		APIKeyValidator: &fakeAPIKeyValidator{validKey: "secret-key"},
		Logger:          l,
	})

	handler := func(ctx context.Context, req any) (any, error) {
		t.Fatal("handler should not be called")
		return nil, nil
	}

	md := metadata.New(map[string]string{"x-api-key": "wrong-key"})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Auth"}, handler)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", st.Code())
	}
}

func TestUnaryAuth_APIKeyPrecedence(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	// When both validators are present and both headers exist,
	// API key should be checked first.
	interceptor := UnaryAuth(AuthConfig{
		TokenValidator:  &fakeTokenValidator{err: errors.New("should not be called")},
		APIKeyValidator: &fakeAPIKeyValidator{validKey: "secret-key"},
		Logger:          l,
	})

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	md := metadata.New(map[string]string{
		"x-api-key":     "secret-key",
		"authorization": "Bearer some-token",
	})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Auth"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected 'ok', got %v", resp)
	}
}

func TestUnaryAuth_SkipMethod(t *testing.T) {
	interceptor := UnaryAuth(AuthConfig{
		TokenValidator: &fakeTokenValidator{err: errors.New("should not validate")},
		SkipMethods:    []string{"/grpc.health.v1.Health/Check"},
	})

	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	// No metadata at all, but method is skipped.
	resp, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected 'ok', got %v", resp)
	}
}

func TestUnaryAuth_NoCredentials(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := UnaryAuth(AuthConfig{
		TokenValidator:  &fakeTokenValidator{},
		APIKeyValidator: &fakeAPIKeyValidator{validKey: "key"},
		Logger:          l,
	})

	handler := func(ctx context.Context, req any) (any, error) {
		t.Fatal("handler should not be called")
		return nil, nil
	}

	// Metadata present but no auth headers.
	md := metadata.New(map[string]string{"some-key": "some-value"})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Auth"}, handler)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", st.Code())
	}
}

func TestStreamAuth_ValidToken(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := StreamAuth(AuthConfig{
		TokenValidator: &fakeTokenValidator{
			claims: middleware.Claims{Subject: "user-456"},
		},
		Logger: l,
	})

	var capturedCtx context.Context
	handler := func(srv any, stream grpc.ServerStream) error {
		capturedCtx = stream.Context()
		return nil
	}

	md := metadata.New(map[string]string{"authorization": "Bearer valid-token"})
	ctx := metadata.NewIncomingContext(context.Background(), md)
	ss := &fakeServerStream{ctx: ctx}

	err := interceptor(nil, ss, &grpc.StreamServerInfo{FullMethod: "/test/StreamAuth"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	claims, ok := GetClaims(capturedCtx)
	if !ok {
		t.Fatal("expected claims in context")
	}
	if claims.Subject != "user-456" {
		t.Fatalf("expected subject 'user-456', got %q", claims.Subject)
	}
}

func TestStreamAuth_SkipMethod(t *testing.T) {
	interceptor := StreamAuth(AuthConfig{
		TokenValidator: &fakeTokenValidator{err: errors.New("should not validate")},
		SkipMethods:    []string{"/grpc.health.v1.Health/Watch"},
	})

	handler := func(srv any, stream grpc.ServerStream) error {
		return nil
	}

	ss := &fakeServerStream{ctx: context.Background()}
	err := interceptor(nil, ss, &grpc.StreamServerInfo{FullMethod: "/grpc.health.v1.Health/Watch"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetClaims_NotSet(t *testing.T) {
	_, ok := GetClaims(context.Background())
	if ok {
		t.Fatal("expected no claims in empty context")
	}
}
