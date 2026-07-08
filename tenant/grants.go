package tenant

import "github.com/google/uuid"

// ServiceGrants is the allow-set that scopes what a peer-mTLS service SVID may
// do. Today it carries the coarsest grant that matters for the mesh: a set of
// service SVIDs trusted to act across ANY organization (the successors of the
// current x-api-key "trusted platform code" caller). It is consulted by
// [Principal.CanActInOrg] only when a peer SVID is present.
//
// The zero value denies everything (fail-closed). The real grant list is
// wired later via [SetServiceGrants]; until then no SVID has any grant, which
// is safe because no SVID is presented anywhere yet.
type ServiceGrants struct {
	crossOrg map[string]struct{} // svid string -> allowed in any org
}

// NewServiceGrants builds a grant set from the SVIDs trusted to act across any
// organization (e.g. "spiffe://sentiae.io/svc/foundry").
func NewServiceGrants(crossOrgSVIDs ...string) ServiceGrants {
	set := make(map[string]struct{}, len(crossOrgSVIDs))
	for _, s := range crossOrgSVIDs {
		if s != "" {
			set[s] = struct{}{}
		}
	}
	return ServiceGrants{crossOrg: set}
}

// Allows reports whether the given peer SVID may act in org. Currently a
// granted SVID may act in any org; an ungranted SVID (or the zero-value
// grants) is denied.
func (g ServiceGrants) Allows(svid string, _ uuid.UUID) bool {
	if g.crossOrg == nil || svid == "" {
		return false
	}
	_, ok := g.crossOrg[svid]
	return ok
}

// defaultServiceGrants is the package-level grant set consulted by
// [Principal.CanActInOrg] for SVID principals. Zero value = deny-all.
var defaultServiceGrants ServiceGrants

// SetServiceGrants configures the package-level service SVID grant set. Call
// once at process start after resolving the trusted-service list.
func SetServiceGrants(g ServiceGrants) {
	defaultServiceGrants = g
}
