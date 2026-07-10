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
