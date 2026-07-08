package tenant

import (
	"testing"

	"github.com/google/uuid"
	"github.com/sentiae/platform-kit/middleware"
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
	SetServiceGrants(NewServiceGrants(grantedSVID))
	t.Cleanup(func() { SetServiceGrants(prev) })

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

// TestServiceGrants_ZeroValueDenies confirms the conservative default: an
// unconfigured (zero-value) grant set denies every SVID.
func TestServiceGrants_ZeroValueDenies(t *testing.T) {
	var g ServiceGrants
	if g.Allows("spiffe://sentiae.io/svc/foundry", orgA) {
		t.Fatal("zero-value ServiceGrants must deny all SVIDs")
	}
	if g.Allows("", orgA) {
		t.Fatal("empty SVID must be denied")
	}
}
