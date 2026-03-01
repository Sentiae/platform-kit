package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"

	platformerrors "github.com/sentiae/platform-kit/errors"
)

// Recovery returns middleware that catches panics and returns a 500 JSON error
// response using the platform's RFC 7807 error format.
func Recovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					stack := string(debug.Stack())
					logger.ErrorContext(r.Context(), "panic recovered",
						"panic", rec,
						"stack", stack,
						"method", r.Method,
						"path", r.URL.Path,
					)

					w.Header().Set("Content-Type", "application/problem+json")
					w.WriteHeader(http.StatusInternalServerError)
					resp := platformerrors.ErrorResponse{
						Type:   "about:blank",
						Title:  http.StatusText(http.StatusInternalServerError),
						Status: http.StatusInternalServerError,
						Detail: "internal server error",
					}
					json.NewEncoder(w).Encode(resp)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
