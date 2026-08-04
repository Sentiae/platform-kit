package tenant

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/sentiae/platform-kit/middleware"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// TestCanActInOrg_NoSVIDUnchanged proves the safety guarantee: with no peer
// SVID present (ServiceSVID == ""), CanActInOrg is byte-identical to the
// pre-mTLS behavior — an x-api-key service acts in any org, a user principal
// acts only in its orgs / as platform admin, a bare principal is denied.
// This mirrors the pre-change TestPrincipal_CanActInOrg cases exactly.
func TestCanActInOrg_NoSVIDUnchanged(t *testing.T) {
	tests := []struct {
		name string
		p    Principal
		org  uuid.UUID
		want bool
	}{
		{"user member", Principal{Claims: &middleware.Claims{Scopes: []string{"org:" + orgA.String()}}}, orgA, true},
		{"user non-member", Principal{Claims: &middleware.Claims{Scopes: []string{"org:" + orgA.String()}}}, orgB, false},
		{"platform admin anywhere", Principal{Claims: &middleware.Claims{PlatformAdmin: true}}, orgB, true},
		{"service-only trusted (any org)", Principal{ServiceAuthed: true}, orgA, true},
		{"empty principal fail-closed", Principal{}, orgA, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.p.ServiceSVID != "" {
				t.Fatalf("test fixture must have empty ServiceSVID, got %q", tt.p.ServiceSVID)
			}
			if got := tt.p.CanActInOrg(tt.org); got != tt.want {
				t.Fatalf("CanActInOrg() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCanActInOrg_SVIDGrants proves the SVID path: an SVID principal is denied
// unless its SPIFFE ID is in the configured cross-org grant set, and the
// user-Claims path is unchanged even when an SVID is also present.
func TestCanActInOrg_SVIDGrants(t *testing.T) {
	const grantedSVID = "spiffe://sentiae.io/svc/foundry"
	const ungrantedSVID = "spiffe://sentiae.io/svc/portal"

	// Configure the package-level grants for this test, restore after.
	prev := defaultServiceGrants
	SetServiceGrants(NewServiceGrants(map[string]ServiceGrant{
		grantedSVID: {CrossOrg: true},
	}))
	t.Cleanup(func() { SetServiceGrants(prev) })

	// Grant enforcement on the SVID path is active only under strict SVID-authz
	// (the lenient rollout step lets any peer-SVID service act in any org).
	prevStrict := meshSVIDAuthzStrict
	SetMeshSVIDAuthzStrict(true)
	t.Cleanup(func() { SetMeshSVIDAuthzStrict(prevStrict) })

	tests := []struct {
		name string
		p    Principal
		org  uuid.UUID
		want bool
	}{
		{"granted SVID allowed", Principal{ServiceSVID: grantedSVID}, orgA, true},
		{"granted SVID allowed in any org", Principal{ServiceSVID: grantedSVID}, orgB, true},
		{"ungranted SVID denied", Principal{ServiceSVID: ungrantedSVID}, orgA, false},
		// SVID present but user Claims win — Claims path unchanged.
		{"user claims path unchanged despite SVID", Principal{
			ServiceSVID: ungrantedSVID,
			Claims:      &middleware.Claims{Scopes: []string{"org:" + orgA.String()}},
		}, orgA, true},
		{"user non-member despite granted SVID string", Principal{
			ServiceSVID: grantedSVID,
			Claims:      &middleware.Claims{Scopes: []string{"org:" + orgA.String()}},
		}, orgB, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.CanActInOrg(tt.org); got != tt.want {
				t.Fatalf("CanActInOrg() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCanActInOrg_MethodScopedGrant proves the D-223 seam: for a restricted
// (non-empty Methods) grant the served full-method must be an exact member —
// a non-member and a missing method are both denied — while a blanket grant and
// the user-claims path (D-073 precedence) are untouched.
func TestCanActInOrg_MethodScopedGrant(t *testing.T) {
	const (
		restrictedSVID = "spiffe://sentiae.io/svc/canvas"
		blanketSVID    = "spiffe://sentiae.io/svc/foundry"
		grantedMethod  = "/catalog.v1.ComponentCatalogService/GetComponent"
		otherMethod    = "/catalog.v1.ComponentCatalogService/CreateComponent"
	)

	prev := defaultServiceGrants
	SetServiceGrants(NewServiceGrants(map[string]ServiceGrant{
		restrictedSVID: {CrossOrg: true, Methods: map[string]struct{}{grantedMethod: {}}},
		blanketSVID:    {CrossOrg: true},
		"spiffe://sentiae.io/svc/nocrossorg": {
			CrossOrg: false,
			Methods:  map[string]struct{}{grantedMethod: {}},
		},
	}))
	t.Cleanup(func() { SetServiceGrants(prev) })

	prevStrict := meshSVIDAuthzStrict
	SetMeshSVIDAuthzStrict(true)
	t.Cleanup(func() { SetMeshSVIDAuthzStrict(prevStrict) })

	tests := []struct {
		name string
		p    Principal
		want bool
	}{
		{"granted method allowed", Principal{ServiceSVID: restrictedSVID, Method: grantedMethod}, true},
		{"non-member method denied", Principal{ServiceSVID: restrictedSVID, Method: otherMethod}, false},
		{"empty method fails closed", Principal{ServiceSVID: restrictedSVID}, false},
		{"unknown SVID denied", Principal{ServiceSVID: "spiffe://sentiae.io/svc/portal", Method: grantedMethod}, false},
		{"no cross-org denied even for a granted method", Principal{
			ServiceSVID: "spiffe://sentiae.io/svc/nocrossorg", Method: grantedMethod,
		}, false},
		{"blanket grant unrestricted by method", Principal{ServiceSVID: blanketSVID, Method: otherMethod}, true},
		{"blanket grant unaffected by an empty method", Principal{ServiceSVID: blanketSVID}, true},
		// D-073: claims are the sole org authority; the method never enters it.
		{"user claims path unaffected by a non-member method", Principal{
			ServiceSVID: restrictedSVID,
			Method:      otherMethod,
			Claims:      &middleware.Claims{Scopes: []string{"org:" + orgA.String()}},
		}, true},
		{"user claims path unaffected by an empty method", Principal{
			ServiceSVID: restrictedSVID,
			Claims:      &middleware.Claims{Scopes: []string{"org:" + orgA.String()}},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.CanActInOrg(orgA); got != tt.want {
				t.Fatalf("CanActInOrg() = %v, want %v", got, tt.want)
			}
		})
	}
}

// stubServerTransportStream carries a full-method the way the gRPC server's own
// transport stream does, so grpc.Method(ctx) resolves it.
type stubServerTransportStream struct{ method string }

func (s *stubServerTransportStream) Method() string               { return s.method }
func (s *stubServerTransportStream) SetHeader(metadata.MD) error  { return nil }
func (s *stubServerTransportStream) SendHeader(metadata.MD) error { return nil }
func (s *stubServerTransportStream) SetTrailer(metadata.MD) error { return nil }

// TestFromContext_Method proves Principal.Method is filled from the server-owned
// gRPC method and stays empty off the gRPC path (where the restricted-grant
// branch then fails closed).
func TestFromContext_Method(t *testing.T) {
	const method = "/catalog.v1.ComponentCatalogService/GetComponent"
	claims := middleware.Claims{Scopes: []string{"org:" + orgA.String()}}

	t.Run("gRPC server context", func(t *testing.T) {
		ctx := grpc.NewContextWithServerTransportStream(
			middleware.InjectClaimsForTest(context.Background(), claims),
			&stubServerTransportStream{method: method},
		)
		p, ok := FromContext(ctx)
		if !ok {
			t.Fatal("FromContext() ok = false, want true")
		}
		if p.Method != method {
			t.Fatalf("Method = %q, want %q", p.Method, method)
		}
	})

	t.Run("off the gRPC path", func(t *testing.T) {
		p, ok := FromContext(middleware.InjectClaimsForTest(context.Background(), claims))
		if !ok {
			t.Fatal("FromContext() ok = false, want true")
		}
		if p.Method != "" {
			t.Fatalf("Method = %q, want empty", p.Method)
		}
	})
}

// TestServiceGrants_ZeroValueDenies confirms the conservative default: an
// unconfigured (zero-value) grant set denies every SVID.
func TestServiceGrants_ZeroValueDenies(t *testing.T) {
	var g ServiceGrants
	if g.AllowsOrg("spiffe://sentiae.io/svc/foundry", orgA) {
		t.Fatal("zero-value ServiceGrants must deny all SVIDs")
	}
	if g.AllowsOrg("", orgA) {
		t.Fatal("empty SVID must be denied")
	}
}
