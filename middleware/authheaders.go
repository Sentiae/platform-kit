// Phase 8: shared auth-header extraction.
//
// Every Sentiae service has its own flavor of "read the caller's
// org/user/tenant id out of headers" middleware. The exact header
// names drifted during Phase 7 — work-service uses X-Organization-ID,
// code-analysis-service originally used X-Tenant-ID, and foundry
// checks both. This package centralizes the dual-read so new services
// don't reinvent it with yet another spelling.
package middleware

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// OrganizationHeaderNames lists the headers callers may use to supply
// the current organization/tenant id. First non-empty parse wins.
// Extend this list conservatively — every entry is forever.
var OrganizationHeaderNames = []string{
	"X-Organization-ID",
	"X-Tenant-ID",
}

// UserHeaderName is the single canonical header for the caller's user
// id. We don't duplicate this one — all services converged on the name.
const UserHeaderName = "X-User-ID"

// AuthorizationHeader is the standard RFC 7235 header. Included here
// so callers have a single import for the canonical names.
const AuthorizationHeader = "Authorization"

// ExtractOrganizationID reads and parses the first available
// organization/tenant header. Returns uuid.Nil when no header is set
// or all values fail to parse.
func ExtractOrganizationID(r *http.Request) uuid.UUID {
	for _, name := range OrganizationHeaderNames {
		if v := r.Header.Get(name); v != "" {
			if id, err := uuid.Parse(v); err == nil {
				return id
			}
		}
	}
	return uuid.Nil
}

// ExtractUserID reads and parses the canonical user header.
func ExtractUserID(r *http.Request) uuid.UUID {
	v := r.Header.Get(UserHeaderName)
	if v == "" {
		return uuid.Nil
	}
	if id, err := uuid.Parse(v); err == nil {
		return id
	}
	return uuid.Nil
}

// ExtractBearerToken strips the "Bearer " prefix from the Authorization
// header. Returns an empty string when the header is missing or
// malformed; callers decide whether that's a 401.
func ExtractBearerToken(r *http.Request) string {
	h := r.Header.Get(AuthorizationHeader)
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix))
}
