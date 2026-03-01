package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/sentiae/platform-kit/logger"
)

const requestIDHeader = "X-Request-Id"

// RequestID is middleware that sets a unique request ID on each request.
// If the incoming request already has an X-Request-Id header, that value is used.
// Otherwise, a new 16-byte hex ID is generated.
// The ID is stored in the context (accessible to the platform logger) and set
// on the response header.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = generateID()
		}

		ctx := context.WithValue(r.Context(), logger.RequestIDKey, id)
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetRequestID returns the request ID from the context, or an empty string if not set.
func GetRequestID(ctx context.Context) string {
	id, _ := ctx.Value(logger.RequestIDKey).(string)
	return id
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
