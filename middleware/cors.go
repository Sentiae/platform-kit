package middleware

import (
	"net/http"
	"strconv"
	"strings"
)

// CORSConfig configures the CORS middleware.
type CORSConfig struct {
	// AllowedOrigins is a list of origins allowed to make cross-origin requests.
	// Supports exact matches and wildcard patterns (e.g. "https://*.example.com").
	// An empty list rejects all cross-origin requests.
	AllowedOrigins []string

	// AllowedMethods is the list of HTTP methods allowed for cross-origin requests.
	// Defaults to GET, POST, PUT, PATCH, DELETE, OPTIONS.
	AllowedMethods []string

	// AllowedHeaders is the list of request headers allowed for cross-origin requests.
	// Defaults to Accept, Authorization, Content-Type, X-Request-Id.
	AllowedHeaders []string

	// ExposedHeaders are response headers that browsers are allowed to access.
	ExposedHeaders []string

	// AllowCredentials indicates whether cookies and auth headers are included.
	AllowCredentials bool

	// MaxAge is the preflight cache duration in seconds.
	MaxAge int
}

// CORS returns middleware that handles Cross-Origin Resource Sharing.
// Origins are read from config; the middleware never uses a hardcoded "*".
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	methods := cfg.AllowedMethods
	if len(methods) == 0 {
		methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
	}
	headers := cfg.AllowedHeaders
	if len(headers) == 0 {
		headers = []string{"Accept", "Authorization", "Content-Type", "X-Request-Id"}
	}

	methodsStr := strings.Join(methods, ", ")
	headersStr := strings.Join(headers, ", ")
	exposedStr := strings.Join(cfg.ExposedHeaders, ", ")
	maxAgeStr := strconv.Itoa(cfg.MaxAge)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			if !matchAnyOrigin(cfg.AllowedOrigins, origin) {
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("Access-Control-Allow-Origin", origin)
			if cfg.AllowCredentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			if exposedStr != "" {
				w.Header().Set("Access-Control-Expose-Headers", exposedStr)
			}
			w.Header().Add("Vary", "Origin")

			// Preflight request.
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.Header().Set("Access-Control-Allow-Methods", methodsStr)
				w.Header().Set("Access-Control-Allow-Headers", headersStr)
				if cfg.MaxAge > 0 {
					w.Header().Set("Access-Control-Max-Age", maxAgeStr)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func matchAnyOrigin(patterns []string, origin string) bool {
	for _, p := range patterns {
		if matchOrigin(p, origin) {
			return true
		}
	}
	return false
}

func matchOrigin(pattern, origin string) bool {
	if pattern == origin {
		return true
	}
	i := strings.IndexByte(pattern, '*')
	if i < 0 {
		return false
	}
	prefix := pattern[:i]
	suffix := pattern[i+1:]
	// The origin must be longer than prefix+suffix (wildcard matches at least 1 char).
	if len(origin) <= len(prefix)+len(suffix) {
		return false
	}
	return strings.HasPrefix(origin, prefix) && strings.HasSuffix(origin, suffix)
}
