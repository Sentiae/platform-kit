// Package middleware — RequirePermission HTTP middleware (shared).
//
// This file lives in platform-kit so every service enforces authorization
// through the same code path instead of each one wiring its own call to
// permission-service. It's a thin router adapter:
//   - the caller provides a PermissionChecker (gRPC or in-memory fake),
//   - the caller provides a ResourceExtractor that plucks the resource
//     type/id out of the request (usually a URL path param),
//   - the middleware reads the authenticated subject from context (via
//     GetClaims — the Auth middleware must run first),
//   - on a denied check, it writes an RFC-7807 problem+json response with
//     HTTP 403.
//
// Services that currently call permissionClient.Check directly inside
// handlers (§0.6 of the gap-closure plan: git-service, conversation-service,
// notification-service, foundry-service) should migrate to this wrapper
// so behavior is uniform — same error shape, same logging hooks, same
// fail-closed semantics.

package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	platformerrors "github.com/sentiae/platform-kit/errors"
)

// PermissionChecker is the minimum surface the middleware needs from a
// permission-service client. Implementers:
//   - conversation-service/internal/adapter/gateway/permission_client.go
//   - notification-service/internal/infrastructure/grpc/permission_client.go
//   - any in-process mock for tests.
//
// Returning (false, nil) means "subject is not authorised" — write 403.
// Returning a non-nil error means the check itself failed (permission
// service unreachable, malformed IDs, etc.) — the middleware writes 503.
type PermissionChecker interface {
	CheckPermission(ctx context.Context, subjectID, permission, resourceType, resourceID string) (bool, error)
}

// PermissionResource names the resource being checked. ResourceType is a
// static string (e.g. "repository", "spec", "feature"); ResourceID comes
// out of the request.
type PermissionResource struct {
	Type string
	ID   string
}

// ResourceExtractor plucks the resource identity out of a request.
// Typically a thin wrapper around chi.URLParam. Return the empty resource
// (both fields zero) with a nil error to fail-closed.
type ResourceExtractor func(r *http.Request) (PermissionResource, error)

// FixedResourceType returns an extractor that reuses a static resource
// type and reads the ID from the given request-parameter function. This
// is the common case — each route handler knows its resource type at
// compile time.
//
//	mw := middleware.RequirePermission(pc, "write",
//	    middleware.FixedResourceType("repository", chi.URLParam))
func FixedResourceType(resourceType string, idFromRequest func(*http.Request, string) string, paramName ...string) ResourceExtractor {
	param := "id"
	if len(paramName) > 0 && paramName[0] != "" {
		param = paramName[0]
	}
	return func(r *http.Request) (PermissionResource, error) {
		id := idFromRequest(r, param)
		if id == "" {
			return PermissionResource{}, errors.New("missing resource id")
		}
		return PermissionResource{Type: resourceType, ID: id}, nil
	}
}

// ErrPermissionDenied is returned (via the response body detail) when the
// checker reports the subject is not authorised. Exposed so tests can
// assert against a stable sentinel.
var ErrPermissionDenied = errors.New("permission denied")

// RequirePermission returns HTTP middleware that authorises the caller
// for `permission` on the resource described by `extract`. The Auth
// middleware (or an equivalent that populates Claims) MUST run first —
// without a subject in context the middleware fails closed with 401.
//
// Drop-in usage pattern (copy into any service's server.go):
//
//	import "github.com/sentiae/platform-kit/middleware"
//	import "github.com/go-chi/chi/v5"
//
//	writer := middleware.RequirePermission(
//	    permissionClient,
//	    "write",
//	    middleware.FixedResourceType("repository", chi.URLParam, "repo_id"),
//	)
//	r.With(writer).Post("/repositories/{repo_id}/commits", commitHandler)
//
// Services that already have a custom permission check in each handler
// should migrate to this factory so error-shape, logging, and
// fail-closed semantics stay uniform across the platform. §A5.
func RequirePermission(checker PermissionChecker, permission string, extract ResourceExtractor) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if checker == nil || extract == nil {
				writePermissionError(w, http.StatusInternalServerError, "permission middleware not configured")
				return
			}

			claims, ok := GetClaims(r.Context())
			if !ok || claims.Subject == "" {
				writePermissionError(w, http.StatusUnauthorized, "authenticated subject required")
				return
			}

			resource, err := extract(r)
			if err != nil {
				writePermissionError(w, http.StatusBadRequest, fmt.Sprintf("resource extraction: %v", err))
				return
			}
			if resource.Type == "" || resource.ID == "" {
				writePermissionError(w, http.StatusBadRequest, "missing resource type or id")
				return
			}

			allowed, err := checker.CheckPermission(r.Context(), claims.Subject, permission, resource.Type, resource.ID)
			if err != nil {
				writePermissionError(w, http.StatusServiceUnavailable, fmt.Sprintf("permission check failed: %v", err))
				return
			}
			if !allowed {
				writePermissionError(w, http.StatusForbidden, fmt.Sprintf("permission %q denied on %s %s", permission, resource.Type, resource.ID))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func writePermissionError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(platformerrors.ErrorResponse{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	})
}
