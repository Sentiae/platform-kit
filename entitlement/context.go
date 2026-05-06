package entitlement

import "context"

// ctxKey is the unexported context key for the resolved Set.
type ctxKey struct{}

// hashCtxKey carries the raw ents_hash claim before resolution. It
// lets middleware that runs before resolution stash the hash so the
// resolver can pick it up.
type hashCtxKey struct{}

// WithSet returns a new context carrying the resolved entitlement set.
func WithSet(ctx context.Context, s *Set) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// FromContext returns the Set on ctx, or an empty Set when none is
// present. Returning an empty Set rather than nil means callers can
// always call Has() without a nil check.
func FromContext(ctx context.Context) *Set {
	if ctx == nil {
		return &Set{idx: map[Entitlement]struct{}{}}
	}
	s, ok := ctx.Value(ctxKey{}).(*Set)
	if !ok || s == nil {
		return &Set{idx: map[Entitlement]struct{}{}}
	}
	return s
}

// HasEntitlement is the canonical check helper used by handlers and
// middleware. Returns false on nil context or missing set.
func HasEntitlement(ctx context.Context, e Entitlement) bool {
	return FromContext(ctx).Has(e)
}

// WithHash stashes the raw ents_hash claim on context for resolver
// middleware to pick up. Used by JWT auth middleware that runs before
// the entitlement resolver.
func WithHash(ctx context.Context, hash string) context.Context {
	if hash == "" {
		return ctx
	}
	return context.WithValue(ctx, hashCtxKey{}, hash)
}

// HashFromContext returns the raw ents_hash from ctx if present.
func HashFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	h, _ := ctx.Value(hashCtxKey{}).(string)
	return h
}
