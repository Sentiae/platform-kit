// Package tenant resolves the caller identity behind a request and provides
// the tenant-authorization primitives services share when isolating data by
// organization. It is a thin consumer of the auth layers: it reads the
// principals stashed by the gRPC interceptor (service caller + user claims)
// and the HTTP middleware (user claims), or an explicitly-set Principal for
// transports that run neither (e.g. the git OCI HTTP path).
//
// Dependency direction is one-way: tenant imports interceptor, middleware
// and authjwt; none of them import tenant.
package tenant

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/sentiae/platform-kit/interceptor"
	"github.com/sentiae/platform-kit/middleware"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Principal is the resolved caller identity for tenant-authorization decisions.
type Principal struct {
	ServiceAuthed bool               // a valid x-api-key was presented
	Service       string             // x-service-name label (attribution only; empty if none)
	Claims        *middleware.Claims // non-nil when a valid user JWT was presented
}

// principalCtxKey is the unexported key for an explicitly-set Principal.
type principalCtxKey struct{}

// ContextWithPrincipal stashes an explicit Principal on the context, used by
// HTTP services (e.g. git OCI) that don't run the gRPC auth interceptor.
func ContextWithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// FromContext resolves the caller. Order: an explicitly-set Principal (HTTP
// path) → else synthesize from the gRPC interceptor's stashed claims + service
// marker → else from middleware (HTTP Auth) claims. Returns ok=false only when
// nothing is present.
func FromContext(ctx context.Context) (Principal, bool) {
	if p, ok := ctx.Value(principalCtxKey{}).(Principal); ok {
		return p, true
	}
	var p Principal
	if svc, ok := interceptor.GetServiceCaller(ctx); ok {
		p.ServiceAuthed = true
		p.Service = svc
	}
	if c, ok := interceptor.GetClaims(ctx); ok {
		cc := c
		p.Claims = &cc
	} else if c, ok := middleware.GetClaims(ctx); ok {
		cc := c
		p.Claims = &cc
	}
	if !p.ServiceAuthed && p.Claims == nil {
		return Principal{}, false
	}
	return p, true
}

// OrgIDs is the union of the org:<uuid> scopes and the organization_id claim
// (parsed UUIDs, deduped). Empty for a service-only principal.
func (p Principal) OrgIDs() []uuid.UUID {
	if p.Claims == nil {
		return nil
	}
	seen := map[uuid.UUID]struct{}{}
	var out []uuid.UUID
	add := func(s string) {
		id, err := uuid.Parse(s)
		if err != nil {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, s := range p.Claims.Scopes {
		if rest, ok := strings.CutPrefix(s, "org:"); ok {
			add(rest)
		}
	}
	if p.Claims.OrganizationID != "" {
		add(p.Claims.OrganizationID)
	}
	return out
}

// CanActInOrg reports whether the principal may act in org. A user principal
// may act when org is among its OrgIDs() or it is a platform admin. A
// service-only principal is trusted platform code and may always act; a
// principal with neither service auth nor user claims is denied (fail-closed).
func (p Principal) CanActInOrg(org uuid.UUID) bool {
	if p.Claims == nil {
		return p.ServiceAuthed
	}
	if p.Claims.PlatformAdmin {
		return true
	}
	for _, id := range p.OrgIDs() {
		if id == org {
			return true
		}
	}
	return false
}

// ActorID returns the user subject (sub) as a UUID for a user principal.
// ok is false for a service-only principal or an unparseable subject.
func (p Principal) ActorID() (uuid.UUID, bool) {
	if p.Claims == nil {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(p.Claims.Subject)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// AuthorizeOrg returns nil if the caller may act in org; else a gRPC status
// error. No principal → codes.Unauthenticated; principal but not permitted →
// codes.PermissionDenied.
func AuthorizeOrg(ctx context.Context, org uuid.UUID) error {
	p, ok := FromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "no principal")
	}
	if !p.CanActInOrg(org) {
		return status.Error(codes.PermissionDenied, "not a member of the target organization")
	}
	return nil
}

// AssertOrgOrNotFound is for by-id reads/writes where a cross-tenant hit must
// not leak existence: returns codes.NotFound (NOT PermissionDenied) when a
// user principal may not act in org. A service principal always passes. No
// principal → codes.Unauthenticated.
func AssertOrgOrNotFound(ctx context.Context, org uuid.UUID, notFoundMsg string) error {
	p, ok := FromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "no principal")
	}
	if !p.CanActInOrg(org) {
		return status.Error(codes.NotFound, notFoundMsg)
	}
	return nil
}

// ActorIDOrRequested resolves the trusted actor for audit attribution: a user
// principal uses its own subject (request value ignored); a service principal
// uses the requested value (parsed). Returns uuid.Nil,false when neither
// yields a usable id.
func ActorIDOrRequested(ctx context.Context, requested string) (uuid.UUID, bool) {
	p, ok := FromContext(ctx)
	if !ok {
		return uuid.Nil, false
	}
	if p.Claims != nil {
		return p.ActorID()
	}
	id, err := uuid.Parse(requested)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}
