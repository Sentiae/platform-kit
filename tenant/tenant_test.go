package tenant

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/sentiae/platform-kit/middleware"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	orgA  = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	orgB  = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	userU = uuid.MustParse("33333333-3333-3333-3333-333333333333")
)

func sameSet(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[uuid.UUID]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func TestPrincipal_OrgIDs(t *testing.T) {
	tests := []struct {
		name   string
		claims *middleware.Claims
		want   []uuid.UUID
	}{
		{
			name:   "service-only (nil claims) has no orgs",
			claims: nil,
			want:   nil,
		},
		{
			name: "scopes and organization_id union deduped",
			claims: &middleware.Claims{
				Scopes:         []string{"org:" + orgA.String(), "org:" + orgB.String(), "read"},
				OrganizationID: orgA.String(),
			},
			want: []uuid.UUID{orgA, orgB},
		},
		{
			name: "unparseable scope skipped",
			claims: &middleware.Claims{
				Scopes: []string{"org:not-a-uuid", "org:" + orgA.String()},
			},
			want: []uuid.UUID{orgA},
		},
		{
			name: "organization_id only",
			claims: &middleware.Claims{
				OrganizationID: orgB.String(),
			},
			want: []uuid.UUID{orgB},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Principal{Claims: tt.claims}
			if got := p.OrgIDs(); !sameSet(got, tt.want) {
				t.Fatalf("OrgIDs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrincipal_CanActInOrg(t *testing.T) {
	tests := []struct {
		name string
		p    Principal
		org  uuid.UUID
		want bool
	}{
		{
			name: "member",
			p:    Principal{Claims: &middleware.Claims{Scopes: []string{"org:" + orgA.String()}}},
			org:  orgA,
			want: true,
		},
		{
			name: "non-member",
			p:    Principal{Claims: &middleware.Claims{Scopes: []string{"org:" + orgA.String()}}},
			org:  orgB,
			want: false,
		},
		{
			name: "platform admin acts anywhere",
			p:    Principal{Claims: &middleware.Claims{PlatformAdmin: true}},
			org:  orgB,
			want: true,
		},
		{
			name: "service-only trusted",
			p:    Principal{ServiceAuthed: true},
			org:  orgA,
			want: true,
		},
		{
			name: "empty principal denied (fail-closed)",
			p:    Principal{},
			org:  orgA,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.CanActInOrg(tt.org); got != tt.want {
				t.Fatalf("CanActInOrg() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAssertOrgOrNotFound(t *testing.T) {
	tests := []struct {
		name     string
		setPrin  bool
		p        Principal
		org      uuid.UUID
		wantErr  bool
		wantCode codes.Code
	}{
		{
			name:    "member passes",
			setPrin: true,
			p:       Principal{Claims: &middleware.Claims{Scopes: []string{"org:" + orgA.String()}}},
			org:     orgA,
		},
		{
			name:     "non-member yields NotFound",
			setPrin:  true,
			p:        Principal{Claims: &middleware.Claims{Scopes: []string{"org:" + orgA.String()}}},
			org:      orgB,
			wantErr:  true,
			wantCode: codes.NotFound,
		},
		{
			name:     "no principal yields Unauthenticated",
			setPrin:  false,
			org:      orgA,
			wantErr:  true,
			wantCode: codes.Unauthenticated,
		},
		{
			name:    "service principal passes",
			setPrin: true,
			p:       Principal{ServiceAuthed: true},
			org:     orgA,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.setPrin {
				ctx = ContextWithPrincipal(ctx, tt.p)
			}
			err := AssertOrgOrNotFound(ctx, tt.org, "widget not found")
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if status.Code(err) != tt.wantCode {
				t.Fatalf("expected code %v, got %v", tt.wantCode, status.Code(err))
			}
		})
	}
}

func TestActorIDOrRequested(t *testing.T) {
	tests := []struct {
		name      string
		setPrin   bool
		p         Principal
		requested string
		wantID    uuid.UUID
		wantOK    bool
	}{
		{
			name:      "user principal overrides requested",
			setPrin:   true,
			p:         Principal{Claims: &middleware.Claims{Subject: userU.String()}},
			requested: orgB.String(), // ignored
			wantID:    userU,
			wantOK:    true,
		},
		{
			name:      "service principal uses requested",
			setPrin:   true,
			p:         Principal{ServiceAuthed: true},
			requested: userU.String(),
			wantID:    userU,
			wantOK:    true,
		},
		{
			name:      "service principal with unparseable requested",
			setPrin:   true,
			p:         Principal{ServiceAuthed: true},
			requested: "not-a-uuid",
			wantOK:    false,
		},
		{
			name:      "no principal",
			setPrin:   false,
			requested: userU.String(),
			wantOK:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.setPrin {
				ctx = ContextWithPrincipal(ctx, tt.p)
			}
			id, ok := ActorIDOrRequested(ctx, tt.requested)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && id != tt.wantID {
				t.Fatalf("id = %v, want %v", id, tt.wantID)
			}
		})
	}
}

// TestFromContext resolves the caller from an empty context (none), from an
// explicitly-set Principal, and from middleware-stashed user claims (the HTTP
// Auth fallback branch).
func TestFromContext(t *testing.T) {
	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("expected no principal on bare context")
	}

	explicit := ContextWithPrincipal(context.Background(), Principal{ServiceAuthed: true, Service: "codegen"})
	if p, ok := FromContext(explicit); !ok || !p.ServiceAuthed || p.Service != "codegen" {
		t.Fatalf("expected explicit service principal, got %+v (ok=%v)", p, ok)
	}

	ctx := middleware.InjectClaimsForTest(context.Background(), middleware.Claims{Subject: userU.String()})
	p, ok := FromContext(ctx)
	if !ok {
		t.Fatal("expected principal synthesized from middleware claims")
	}
	if p.Claims == nil || p.Claims.Subject != userU.String() {
		t.Fatalf("expected user claims, got %+v", p.Claims)
	}
}
