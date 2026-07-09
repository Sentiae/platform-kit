package tenant

import "github.com/google/uuid"

// ServiceGrant is the capability set granted to a single peer-mTLS service SVID.
//
//   - CrossOrg lets the SVID act in an organization not proven by a user JWT
//     (the successor of the x-api-key "trusted platform code" any-org caller).
//   - Methods, when non-empty, restricts the SVID to the listed gRPC
//     full-methods for the per-method (Tier-2) guard. An empty Methods imposes
//     no per-method restriction.
type ServiceGrant struct {
	CrossOrg bool                // may act in an org not proven by a user JWT
	Methods  map[string]struct{} // allowed gRPC full-methods; empty = no per-method restriction
}

// ServiceGrants is the allow-set that scopes what peer-mTLS service SVIDs may
// do, keyed by full SPIFFE ID (e.g. "spiffe://sentiae.io/svc/foundry"). It is
// consulted by [Principal.CanActInOrg] (cross-org) and [interceptor.UnaryServiceAuthz]
// (per-method) only when a peer SVID is present.
//
// The zero value denies everything (fail-closed). The real grant list is wired
// at process start via [SetServiceGrants].
type ServiceGrants struct {
	byID map[string]ServiceGrant
}

// NewServiceGrants builds a grant set from a map of full SPIFFE ID -> grant.
// It copies the input and drops empty ("") keys.
func NewServiceGrants(m map[string]ServiceGrant) ServiceGrants {
	set := make(map[string]ServiceGrant, len(m))
	for svid, gr := range m {
		if svid == "" {
			continue
		}
		set[svid] = gr
	}
	return ServiceGrants{byID: set}
}

// AllowsOrg reports whether the given peer SVID may act across organizations.
// The org argument is currently ignored: a cross-org grant is coarse — it
// authorizes any org (the successor of the x-api-key any-org caller). An
// unknown SVID (or the zero-value grants) is denied.
func (g ServiceGrants) AllowsOrg(svid string, _ uuid.UUID) bool {
	if g.byID == nil || svid == "" {
		return false
	}
	gr, ok := g.byID[svid]
	if !ok {
		return false
	}
	return gr.CrossOrg
}

// AllowsMethod reports whether the given peer SVID may call fullMethod. An
// unknown SVID is denied. A known SVID with an empty Methods set has no
// per-method restriction (any method allowed); otherwise the method must be a
// member of its Methods set.
func (g ServiceGrants) AllowsMethod(svid, fullMethod string) bool {
	if g.byID == nil || svid == "" {
		return false
	}
	gr, ok := g.byID[svid]
	if !ok {
		return false
	}
	if len(gr.Methods) == 0 {
		return true
	}
	_, ok = gr.Methods[fullMethod]
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

// meshSVIDAuthzStrict, when true, fails closed for a legacy api-key service
// principal that presents no peer SVID (a plaintext island mid-rollout). It is
// consulted by [Principal.CanActInOrg]. Default false (back-compat).
var meshSVIDAuthzStrict bool

// SetMeshSVIDAuthzStrict toggles strict SVID authz. Once strict, a service
// caller with no peer SVID is denied even if it presented a valid api-key.
func SetMeshSVIDAuthzStrict(v bool) { meshSVIDAuthzStrict = v }
