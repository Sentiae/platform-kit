package tenant

import "testing"

const (
	grantedSVID   = "spiffe://sentiae.io/svc/foundry"
	ungrantedSVID = "spiffe://sentiae.io/svc/portal"
)

// TestAllowsOrg confirms cross-org grants: only a known SVID with CrossOrg set
// is allowed; unknown SVIDs and the empty SVID are denied.
func TestAllowsOrg(t *testing.T) {
	g := NewServiceGrants(map[string]ServiceGrant{
		grantedSVID:                           {CrossOrg: true},
		"spiffe://sentiae.io/svc/no-crossorg": {CrossOrg: false},
	})

	tests := []struct {
		name string
		svid string
		want bool
	}{
		{"granted cross-org", grantedSVID, true},
		{"known but not cross-org", "spiffe://sentiae.io/svc/no-crossorg", false},
		{"ungranted", ungrantedSVID, false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := g.AllowsOrg(tt.svid, orgA); got != tt.want {
				t.Fatalf("AllowsOrg(%q) = %v, want %v", tt.svid, got, tt.want)
			}
		})
	}
}

// TestAllowsMethod confirms per-method least privilege: an empty Methods set
// imposes no restriction (any method allowed for the known SVID); a non-empty
// set allows only listed methods; unknown SVIDs are denied.
func TestAllowsMethod(t *testing.T) {
	const method = "/pkg.Svc/Do"
	const other = "/pkg.Svc/Other"
	g := NewServiceGrants(map[string]ServiceGrant{
		"spiffe://sentiae.io/svc/open":       {CrossOrg: true}, // empty Methods
		"spiffe://sentiae.io/svc/restricted": {Methods: map[string]struct{}{method: {}}},
	})

	tests := []struct {
		name       string
		svid       string
		fullMethod string
		want       bool
	}{
		{"empty methods allows any", "spiffe://sentiae.io/svc/open", method, true},
		{"empty methods allows other", "spiffe://sentiae.io/svc/open", other, true},
		{"restricted allows listed", "spiffe://sentiae.io/svc/restricted", method, true},
		{"restricted denies unlisted", "spiffe://sentiae.io/svc/restricted", other, false},
		{"unknown svid denied", ungrantedSVID, method, false},
		{"empty svid denied", "", method, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := g.AllowsMethod(tt.svid, tt.fullMethod); got != tt.want {
				t.Fatalf("AllowsMethod(%q, %q) = %v, want %v", tt.svid, tt.fullMethod, got, tt.want)
			}
		})
	}
}

// TestCanActInOrg_StrictSVIDAuthz covers the strict-vs-lenient service behavior.
// Under strict: a granted SVID is allowed, an ungranted SVID is denied. Under
// lenient (the behavior-neutral rollout step): ANY peer-SVID service acts in any
// org (mirrors legacy api-key any-org) so the grant policy ships neutrally. An
// api-key service with no SVID is allowed only when NOT strict, denied under
// strict (fail closed).
func TestCanActInOrg_StrictSVIDAuthz(t *testing.T) {
	prevGrants := defaultServiceGrants
	SetServiceGrants(NewServiceGrants(map[string]ServiceGrant{grantedSVID: {CrossOrg: true}}))
	t.Cleanup(func() { SetServiceGrants(prevGrants) })

	prevStrict := meshSVIDAuthzStrict
	t.Cleanup(func() { SetMeshSVIDAuthzStrict(prevStrict) })

	cases := []struct {
		name   string
		strict bool
		p      Principal
		want   bool
	}{
		{"strict: granted SVID allowed", true, Principal{ServiceSVID: grantedSVID}, true},
		{"strict: ungranted SVID denied", true, Principal{ServiceSVID: ungrantedSVID}, false},
		{"strict: api-key no SVID denied", true, Principal{ServiceAuthed: true}, false},
		{"lenient: granted SVID allowed", false, Principal{ServiceSVID: grantedSVID}, true},
		{"lenient: ungranted SVID allowed (neutral step)", false, Principal{ServiceSVID: ungrantedSVID}, true},
		{"lenient: api-key no SVID allowed (back-compat)", false, Principal{ServiceAuthed: true}, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			SetMeshSVIDAuthzStrict(tt.strict)
			if got := tt.p.CanActInOrg(orgA); got != tt.want {
				t.Fatalf("CanActInOrg() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestLoadMeshPolicy_Default confirms the embedded cross-org TCB is granted and
// a non-mesh service is deny-by-default when no override is set.
func TestLoadMeshPolicy_Default(t *testing.T) {
	t.Setenv("APP_MESH_SERVICE_GRANTS", "")
	g := LoadMeshPolicy()

	for _, svid := range []string{
		"spiffe://sentiae.io/svc/foundry",
		"spiffe://sentiae.io/svc/vigil",
	} {
		if !g.AllowsOrg(svid, orgA) {
			t.Fatalf("expected default mesh policy to grant cross-org to %q", svid)
		}
	}
	if g.AllowsOrg("spiffe://sentiae.io/svc/portal", orgA) {
		t.Fatal("non-mesh service must be deny-by-default")
	}
	// D-073: the BFF is NO LONGER a blanket CrossOrg service — it is purely
	// user-mediated and forwards the user JWT, so backends enforce the user's org.
	// A headless (claims-less) BFF SVID must be deny-by-default under strict authz.
	if g.AllowsOrg("spiffe://sentiae.io/svc/bff", orgA) {
		t.Fatal("D-073: svc/bff must be deny-by-default (removed from crossOrgMeshServices)")
	}
}

// TestLoadMeshPolicy_Override confirms an env override merges over the default
// (adds a new SVID) and that malformed JSON falls back to the default without
// panicking.
func TestLoadMeshPolicy_Override(t *testing.T) {
	t.Setenv("APP_MESH_SERVICE_GRANTS",
		`{"spiffe://sentiae.io/svc/custom":{"cross_org":true,"methods":["/pkg.Svc/M"]}}`)
	g := LoadMeshPolicy()
	if !g.AllowsOrg("spiffe://sentiae.io/svc/custom", orgA) {
		t.Fatal("override SVID must be granted cross-org")
	}
	if !g.AllowsMethod("spiffe://sentiae.io/svc/custom", "/pkg.Svc/M") {
		t.Fatal("override SVID must allow its listed method")
	}
	if g.AllowsMethod("spiffe://sentiae.io/svc/custom", "/pkg.Svc/Other") {
		t.Fatal("override SVID must deny an unlisted method")
	}
	// Default entries survive the merge.
	if !g.AllowsOrg("spiffe://sentiae.io/svc/foundry", orgA) {
		t.Fatal("default mesh entry must survive the override merge")
	}

	t.Setenv("APP_MESH_SERVICE_GRANTS", "{not json")
	g2 := LoadMeshPolicy()
	if !g2.AllowsOrg("spiffe://sentiae.io/svc/foundry", orgA) {
		t.Fatal("malformed override must fall back to the default policy")
	}
}
