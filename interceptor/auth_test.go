package interceptor

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/sentiae/platform-kit/logger"
	"github.com/sentiae/platform-kit/middleware"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
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
		AcceptAPIKey:    true,
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
		AcceptAPIKey:    true,
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

// TestUnaryAuth_LayeredCredentials exercises the layered-credential model:
// every presented principal is established, a present-but-invalid credential
// fails closed, and a request with no credential is rejected. The both-headers
// scenarios (valid + invalid Bearer alongside a valid api-key) replace the
// removed api-key short-circuit ("precedence") behavior.
func TestUnaryAuth_LayeredCredentials(t *testing.T) {
	validClaims := middleware.Claims{Subject: "user-123", Email: "u@example.com"}

	tests := []struct {
		name              string
		tokenValidator    middleware.TokenValidator
		apiKeyValidator   APIKeyValidator
		md                map[string]string
		wantErr           bool
		wantCode          codes.Code
		wantServiceCaller string // "" means expect none
		wantClaims        bool
	}{
		{
			name:              "api-key only, no bearer, succeeds",
			apiKeyValidator:   &fakeAPIKeyValidator{validKey: "secret-key"},
			md:                map[string]string{"x-api-key": "secret-key", "x-service-name": "codegen"},
			wantServiceCaller: "codegen",
		},
		{
			name:              "api-key valid and bearer valid, both established",
			tokenValidator:    &fakeTokenValidator{claims: validClaims},
			apiKeyValidator:   &fakeAPIKeyValidator{validKey: "secret-key"},
			md:                map[string]string{"x-api-key": "secret-key", "x-service-name": "eve", "authorization": "Bearer good"},
			wantServiceCaller: "eve",
			wantClaims:        true,
		},
		{
			name:            "api-key valid but bearer invalid, rejected",
			tokenValidator:  &fakeTokenValidator{err: errors.New("expired")},
			apiKeyValidator: &fakeAPIKeyValidator{validKey: "secret-key"},
			md:              map[string]string{"x-api-key": "secret-key", "authorization": "Bearer bad"},
			wantErr:         true,
			wantCode:        codes.Unauthenticated,
		},
		{
			name:              "token validator nil, present bearer ignored, api-key succeeds",
			tokenValidator:    nil,
			apiKeyValidator:   &fakeAPIKeyValidator{validKey: "secret-key"},
			md:                map[string]string{"x-api-key": "secret-key", "authorization": "Bearer ignored"},
			wantServiceCaller: "unknown",
		},
		{
			name:            "no credentials, unauthenticated",
			tokenValidator:  &fakeTokenValidator{claims: validClaims},
			apiKeyValidator: &fakeAPIKeyValidator{validKey: "secret-key"},
			md:              map[string]string{"unrelated": "x"},
			wantErr:         true,
			wantCode:        codes.Unauthenticated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			l := testLogger(&buf)
			ic := UnaryAuth(AuthConfig{
				AcceptAPIKey:    true,
				TokenValidator:  tt.tokenValidator,
				APIKeyValidator: tt.apiKeyValidator,
				Logger:          l,
			})

			var capturedCtx context.Context
			handler := func(ctx context.Context, req any) (any, error) {
				capturedCtx = ctx
				return "ok", nil
			}

			md := metadata.New(tt.md)
			ctx := metadata.NewIncomingContext(context.Background(), md)

			resp, err := ic(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Auth"}, handler)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				st, _ := status.FromError(err)
				if st.Code() != tt.wantCode {
					t.Fatalf("expected code %v, got %v", tt.wantCode, st.Code())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp != "ok" {
				t.Fatalf("expected 'ok', got %v", resp)
			}

			svc, ok := GetServiceCaller(capturedCtx)
			if tt.wantServiceCaller == "" {
				if ok {
					t.Fatalf("expected no service caller, got %q", svc)
				}
			} else if !ok || svc != tt.wantServiceCaller {
				t.Fatalf("expected service caller %q, got %q (ok=%v)", tt.wantServiceCaller, svc, ok)
			}

			if _, hasClaims := GetClaims(capturedCtx); hasClaims != tt.wantClaims {
				t.Fatalf("expected claims present=%v, got %v", tt.wantClaims, hasClaims)
			}
		})
	}
}

// TestUnaryAuth_PeerSVID exercises the SVID credential path: a call carrying a
// peer SPIFFE ID (stashed by the SVID interceptor) establishes a service
// principal with no api-key and no shared secret, and the service caller label
// is the short name after /svc/.
func TestUnaryAuth_PeerSVID(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	ic := UnaryAuth(AuthConfig{Logger: l}) // no validators, no api-key

	var capturedCtx context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		capturedCtx = ctx
		return "ok", nil
	}

	id, err := spiffeid.FromString("spiffe://sentiae.io/svc/foundry")
	if err != nil {
		t.Fatalf("build spiffe id: %v", err)
	}
	// No metadata beyond an empty set; the SVID is on the context (as the SVID
	// interceptor would place it before Auth runs).
	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(nil))
	ctx = context.WithValue(ctx, svidCtxKey{}, id)

	resp, err := ic(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Auth"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected 'ok', got %v", resp)
	}
	svc, ok := GetServiceCaller(capturedCtx)
	if !ok || svc != "foundry" {
		t.Fatalf("expected service caller 'foundry', got %q (ok=%v)", svc, ok)
	}
}

// TestUnaryAuth_RequirePeerSVID confirms rollout step 3: with RequirePeerSVID
// set, a non-skipped call carrying no peer SVID is rejected Unauthenticated.
func TestUnaryAuth_RequirePeerSVID(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	ic := UnaryAuth(AuthConfig{
		RequirePeerSVID: true,
		TokenValidator:  &fakeTokenValidator{claims: middleware.Claims{Subject: "u"}},
		Logger:          l,
	})

	handler := func(ctx context.Context, req any) (any, error) {
		t.Fatal("handler should not be called")
		return nil, nil
	}

	// A valid bearer is present but there is no peer SVID.
	md := metadata.New(map[string]string{"authorization": "Bearer good"})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := ic(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/test/Auth"}, handler)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", st.Code())
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
