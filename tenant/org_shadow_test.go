package tenant

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/sentiae/platform-kit/logger"
	"github.com/sentiae/platform-kit/middleware"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// memberCtx builds a context carrying a user principal whose JWT proves
// membership of exactly org (via ContextWithPrincipal, mirroring the fixtures
// in principal_test.go / tenant_test.go).
func memberCtx(org uuid.UUID) context.Context {
	return ContextWithPrincipal(context.Background(), Principal{
		Claims: &middleware.Claims{Scopes: []string{"org:" + org.String()}},
	})
}

// TestAuthorizeOrgShadow drives the shadow/enforce matrix. In shadow a
// would-deny returns nil (no behavior change); under enforce it returns the
// real AuthorizeOrg code (PermissionDenied for an unpermitted principal,
// Unauthenticated for none).
func TestAuthorizeOrgShadow(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		org      uuid.UUID
		enforce  bool
		wantCode codes.Code // codes.OK => expect nil
	}{
		{"allowed shadow", memberCtx(orgA), orgA, false, codes.OK},
		{"allowed enforce", memberCtx(orgA), orgA, true, codes.OK},
		{"foreign shadow -> nil", memberCtx(orgB), orgA, false, codes.OK},
		{"foreign enforce -> denied", memberCtx(orgB), orgA, true, codes.PermissionDenied},
		{"no principal enforce -> unauth", context.Background(), orgA, true, codes.Unauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AuthorizeOrgShadow(tt.ctx, tt.org, tt.enforce)
			if got := status.Code(err); got != tt.wantCode {
				t.Fatalf("AuthorizeOrgShadow code = %v, want %v (err=%v)", got, tt.wantCode, err)
			}
		})
	}
}

// TestAssertOrgShadow mirrors TestAuthorizeOrgShadow but the enforce-branch
// error for an unpermitted principal is NotFound (no existence leak).
func TestAssertOrgShadow(t *testing.T) {
	const msg = "widget not found"
	tests := []struct {
		name     string
		ctx      context.Context
		org      uuid.UUID
		enforce  bool
		wantCode codes.Code
	}{
		{"allowed shadow", memberCtx(orgA), orgA, false, codes.OK},
		{"allowed enforce", memberCtx(orgA), orgA, true, codes.OK},
		{"foreign shadow -> nil", memberCtx(orgB), orgA, false, codes.OK},
		{"foreign enforce -> notfound", memberCtx(orgB), orgA, true, codes.NotFound},
		{"no principal enforce -> unauth", context.Background(), orgA, true, codes.Unauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AssertOrgShadow(tt.ctx, tt.org, msg, tt.enforce)
			if got := status.Code(err); got != tt.wantCode {
				t.Fatalf("AssertOrgShadow code = %v, want %v (err=%v)", got, tt.wantCode, err)
			}
		})
	}
}

// TestOrgShadowLogsDivergence proves a shadow-mode would-deny emits the
// "org authz divergence" line (with the derived_org key) while still returning
// nil. Captured via a buffer-backed slog logger injected with logger.NewContext.
func TestOrgShadowLogsDivergence(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, nil))
	ctx := logger.NewContext(memberCtx(orgB), l)

	if err := AuthorizeOrgShadow(ctx, orgA, false); err != nil {
		t.Fatalf("shadow would-deny must return nil, got %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "org authz divergence") {
		t.Fatalf("expected divergence log line, got: %q", out)
	}
	if !strings.Contains(out, "derived_org") || !strings.Contains(out, orgA.String()) {
		t.Fatalf("expected derived_org=%s in log, got: %q", orgA, out)
	}

	// An allowed call must NOT log.
	buf.Reset()
	if err := AuthorizeOrgShadow(logger.NewContext(memberCtx(orgA), l), orgA, false); err != nil {
		t.Fatalf("allowed shadow must return nil, got %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("allowed call must not log, got: %q", buf.String())
	}
}
