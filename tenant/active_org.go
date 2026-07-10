package tenant

import (
	"context"

	"github.com/google/uuid"
)

// activeOrgCtxKey carries the REQUESTED active org for a request — the org a
// handler/interceptor has authorized the caller to act in for this call. It is
// distinct from the Principal's set of authorized orgs (a caller may be a member
// of many orgs but acts in exactly one per request). Typed key per root §18.
type activeOrgCtxKey struct{}

// systemCtxKey marks a context as a sanctioned cross-tenant/system path (the
// outbox drainer, an admin scan, a migration) that runs under a BYPASSRLS role
// and must NOT be stamped with a per-tenant GUC. Typed key per root §18.
type systemCtxKey struct{}

// systemOrgCtxKey carries the org a PROCESS-INTERNAL system actor is scoped to
// for exactly one unit of work: a kafka consumer stamping the org named in the
// CloudEvents envelope it is processing, or one iteration of a per-org
// background-job loop. Distinct from activeOrgCtxKey (a request's authorized
// org, re-verified against a Principal) and from systemCtxKey (a BYPASSRLS
// cross-tenant path that skips stamping entirely). Typed key per root §18.
type systemOrgCtxKey struct{}

// WithActiveOrg stores the requested active org on ctx. Set by handlers or
// interceptors AFTER authorizing the caller for org.
func WithActiveOrg(ctx context.Context, org uuid.UUID) context.Context {
	return context.WithValue(ctx, activeOrgCtxKey{}, org)
}

// ActiveOrgFromContext reads the requested active org set by WithActiveOrg.
func ActiveOrgFromContext(ctx context.Context) (uuid.UUID, bool) {
	v, ok := ctx.Value(activeOrgCtxKey{}).(uuid.UUID)
	return v, ok
}

// WithSystemOrg scopes ctx to a single org for a process-internal system actor
// (a kafka consumer stamping the event's org, or one pass of a per-org
// background-job loop). It is stamped WITHOUT a principal re-verify: there is no
// caller/Principal on a consumer or job ctx, so the trust anchor is the
// process's own subscription/job — NOT a caller assertion.
//
// Only set org from data the service itself resolved: the CloudEvents envelope's
// org, or the org the job loop is currently iterating. NEVER set it from
// caller-supplied request input — that would let a caller pick the tenant to act
// in with no authorization check. For a request path use WithActiveOrg (which is
// re-verified against a Principal); for a BYPASSRLS cross-tenant path use
// WithSystemContext (which skips stamping entirely).
func WithSystemOrg(ctx context.Context, org uuid.UUID) context.Context {
	return context.WithValue(ctx, systemOrgCtxKey{}, org)
}

// SystemOrgFromContext reads the org set by WithSystemOrg.
func SystemOrgFromContext(ctx context.Context) (uuid.UUID, bool) {
	v, ok := ctx.Value(systemOrgCtxKey{}).(uuid.UUID)
	return v, ok
}

// WithSystemContext marks ctx as a system/cross-tenant path. Callers on this
// path run under a BYPASSRLS role, so tenant stamping is skipped.
func WithSystemContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, systemCtxKey{}, true)
}

// IsSystemContext reports whether ctx was marked by WithSystemContext.
func IsSystemContext(ctx context.Context) bool {
	v, ok := ctx.Value(systemCtxKey{}).(bool)
	return ok && v
}
