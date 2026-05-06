package entitlement

import (
	"context"
	"encoding/json"
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RequireHTTP returns HTTP middleware that ensures the request's
// ents_hash resolves to a Set containing all of `required`. On
// missing entitlement returns 403 with a structured "upgrade plan"
// payload pointing at the billing UI.
//
// The middleware uses HashFromContext so a prior auth middleware
// must stash the JWT ents_hash via WithHash. If no hash is present
// the request is rejected as unauthorized.
func RequireHTTP(resolver Resolver, required ...Entitlement) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			set, err := loadSet(ctx, resolver)
			if err != nil {
				writeUpgradeJSON(w, http.StatusInternalServerError, "entitlement_resolver_error", required, err.Error())
				return
			}
			for _, ent := range required {
				if !set.Has(ent) {
					writeUpgradeJSON(w, http.StatusForbidden, "entitlement_required", required, "")
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(WithSet(ctx, set)))
		})
	}
}

// RequireUnary returns a gRPC unary server interceptor enforcing the
// same set of required entitlements. Returns codes.PermissionDenied
// with a machine-readable detail when the check fails.
func RequireUnary(resolver Resolver, required ...Entitlement) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		set, err := loadSet(ctx, resolver)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "entitlement resolver: %v", err)
		}
		for _, ent := range required {
			if !set.Has(ent) {
				return nil, status.Errorf(codes.PermissionDenied, "entitlement required: %s", ent)
			}
		}
		return handler(WithSet(ctx, set), req)
	}
}

// loadSet loads a resolved set onto context, reusing one if already
// resolved by an outer middleware. Returns an empty Set when no hash
// is on the context — callers (RequireHTTP / RequireUnary) decide
// whether that means deny.
func loadSet(ctx context.Context, resolver Resolver) (*Set, error) {
	if existing := FromContext(ctx); existing != nil && !existing.Empty() {
		return existing, nil
	}
	hash := HashFromContext(ctx)
	if hash == "" || resolver == nil {
		return NewSet(nil), nil
	}
	return resolver.Resolve(ctx, hash)
}

// LoadSetMiddleware returns HTTP middleware that resolves the set
// for the request once and stashes it on context. Use it when many
// downstream handlers will inspect entitlements (cheaper than each
// RequireHTTP call resolving individually).
func LoadSetMiddleware(resolver Resolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			set, err := loadSet(ctx, resolver)
			if err != nil {
				writeUpgradeJSON(w, http.StatusInternalServerError, "entitlement_resolver_error", nil, err.Error())
				return
			}
			next.ServeHTTP(w, r.WithContext(WithSet(ctx, set)))
		})
	}
}

// writeUpgradeJSON emits the canonical "you need to upgrade" payload.
// Frontends key on `code` to render the upgrade prompt.
func writeUpgradeJSON(w http.ResponseWriter, statusCode int, code string, required []Entitlement, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	missing := make([]string, len(required))
	for i, e := range required {
		missing[i] = string(e)
	}
	body := map[string]any{
		"error": map[string]any{
			"code":    code,
			"missing": missing,
			"detail":  detail,
			"upgrade": map[string]any{
				"reason":     "Your current plan does not include this entitlement.",
				"action_url": "/settings/billing",
			},
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}
