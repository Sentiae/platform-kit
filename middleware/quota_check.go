// Package middleware — NewCheckMiddleware wraps identity-service's quota
// check endpoint as HTTP middleware so any service can gate an action (add
// spec, deploy, test run, etc.) with a single router line.
//
// Semantics:
//   - If the quota client is not configured (nil or empty BaseURL) the
//     middleware fails open — the same behavior as quota.Client.Check, so
//     local dev and tests work without identity-service running.
//   - If the check reports allowed=false, the middleware writes HTTP 402
//     Payment Required with a JSON body {"error":"quota_exceeded",
//     "action":"<action>"}. 402 is the right status for a soft-cap
//     violation: the account is known, authenticated, but has exceeded a
//     paid-plan limit.
//   - If the check itself errors (transport failure, non-2xx from
//     identity-service), the middleware fails open. This is a deliberate
//     availability-over-consistency tradeoff: a momentarily unreachable
//     identity-service should not 500 every write across the platform.
//     The underlying Check call still logs/returns the error to any
//     interested caller through client instrumentation.
package middleware

import (
	"encoding/json"
	"net/http"

	"github.com/sentiae/platform-kit/quota"
)

// NewCheckMiddleware builds middleware that enforces a single quota
// action. extractOrgID reads the organization id out of the request (URL
// param, header, JWT claim, etc.) — return "" to skip the check for that
// request.
func NewCheckMiddleware(c *quota.Client, action quota.Action, extractOrgID func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c == nil {
				next.ServeHTTP(w, r)
				return
			}
			orgID := ""
			if extractOrgID != nil {
				orgID = extractOrgID(r)
			}
			if orgID == "" {
				next.ServeHTTP(w, r)
				return
			}
			allowed, err := c.Check(r.Context(), orgID, action)
			if err != nil {
				// fail open on transport/server errors — availability over strictness.
				next.ServeHTTP(w, r)
				return
			}
			if !allowed {
				writeQuotaExceeded(w, action)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeQuotaExceeded(w http.ResponseWriter, action quota.Action) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":  "quota_exceeded",
		"action": string(action),
	})
}
