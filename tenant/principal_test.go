package tenant

import (
	"testing"

	"github.com/google/uuid"
	"github.com/sentiae/platform-kit/middleware"
)

// TestCanActInOrg_D073Precedence locks the D-073 user-org precedence contract:
// when a user JWT is present (Claims != nil) the user's memberships are the
// SOLE org authority — the peer-SVID grant table is never consulted, so a
// CrossOrg service SVID can NEVER widen a user-mediated call beyond the user's
// orgs. Per-SVID grants apply exclusively to headless (claims-less) calls.
//
// It drives the full precedence matrix against the CURRENT code; every case is
// existing behavior being pinned, not new behavior. A failing quadrant is a
// real finding, not a test to relax.
func TestCanActInOrg_D073Precedence(t *testing.T) {
	const grantedSVID = "spiffe://sentiae.io/svc/foundry"
	const ungrantedSVID = "spiffe://sentiae.io/svc/portal"

	// Configure the package-level grants + strict authz for the headless cases;
	// restore both after so the shared globals aren't leaked to other tests.
	prevGrants := defaultServiceGrants
	SetServiceGrants(NewServiceGrants(map[string]ServiceGrant{
		grantedSVID: {CrossOrg: true},
	}))
	t.Cleanup(func() { SetServiceGrants(prevGrants) })

	prevStrict := meshSVIDAuthzStrict
	SetMeshSVIDAuthzStrict(true)
	t.Cleanup(func() { SetMeshSVIDAuthzStrict(prevStrict) })

	// member is a principal whose user JWT proves membership of orgA (only).
	member := func(svid string) Principal {
		return Principal{
			ServiceSVID: svid,
			Claims:      &middleware.Claims{Scopes: []string{"org:" + orgA.String()}},
		}
	}
	// notMember carries a user JWT proving membership of orgB only, then is
	// asked about orgA — i.e. the user is NOT a member of the target org.
	notMember := func(svid string) Principal {
		return Principal{
			ServiceSVID: svid,
			Claims:      &middleware.Claims{Scopes: []string{"org:" + orgB.String()}},
		}
	}

	tests := []struct {
		name string
		p    Principal
		org  uuid.UUID
		want bool
	}{
		// Quadrant 1: Claims present + member + SVID CrossOrg → true (member;
		// the grant is irrelevant).
		{"claims member + crossorg SVID", member(grantedSVID), orgA, true},
		// Quadrant 2 (the critical lock): Claims present + NOT-member + SVID
		// CrossOrg → FALSE. The CrossOrg SVID must NOT rescue a non-member user.
		{"claims non-member + crossorg SVID (leak lock)", notMember(grantedSVID), orgA, false},
		// Quadrant 3: Claims present + member + SVID ungranted → true (user
		// membership alone suffices; no grant needed).
		{"claims member + ungranted SVID", member(ungrantedSVID), orgA, true},
		// Quadrant 4: Claims present + NOT-member + SVID ungranted → false.
		{"claims non-member + ungranted SVID", notMember(ungrantedSVID), orgA, false},

		// Headless (Claims nil) grant path — the ONLY place the grant table is
		// consulted. Under strict authz a CrossOrg SVID acts in any org.
		{"headless crossorg SVID any org (orgA)", Principal{ServiceSVID: grantedSVID}, orgA, true},
		{"headless crossorg SVID any org (orgB)", Principal{ServiceSVID: grantedSVID}, orgB, true},
		// Headless ungranted SVID under strict authz → false.
		{"headless ungranted SVID strict", Principal{ServiceSVID: ungrantedSVID}, orgA, false},
		// Headless with no SVID and no api-key → false (bare principal).
		{"headless bare principal", Principal{}, orgA, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.CanActInOrg(tt.org); got != tt.want {
				t.Fatalf("CanActInOrg(%s) = %v, want %v", tt.org, got, tt.want)
			}
		})
	}
}
