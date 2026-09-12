package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newCORSHandler(cfg CORSConfig) http.Handler {
	return CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func TestCORS_PreflightAllowed(t *testing.T) {
	handler := newCORSHandler(CORSConfig{
		AllowedOrigins:   []string{"https://app.example.com"},
		AllowCredentials: true,
		MaxAge:           300,
	})

	req := httptest.NewRequest("OPTIONS", "/api/data", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("expected origin header, got %q", rr.Header().Get("Access-Control-Allow-Origin"))
	}
	if rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("expected credentials header")
	}
	if rr.Header().Get("Access-Control-Max-Age") != "300" {
		t.Fatalf("expected max-age 300, got %q", rr.Header().Get("Access-Control-Max-Age"))
	}
	if rr.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatal("expected allow-methods header")
	}
}

func TestCORS_PreflightRejected(t *testing.T) {
	handler := newCORSHandler(CORSConfig{
		AllowedOrigins: []string{"https://allowed.com"},
	})

	req := httptest.NewRequest("OPTIONS", "/api/data", nil)
	req.Header.Set("Origin", "https://evil.com")
	req.Header.Set("Access-Control-Request-Method", "POST")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Non-matching origin: request passes through without CORS headers.
	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("expected no CORS headers for disallowed origin, got %q", rr.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORS_ActualRequest(t *testing.T) {
	handler := newCORSHandler(CORSConfig{
		AllowedOrigins: []string{"https://app.example.com"},
		ExposedHeaders: []string{"X-Request-Id"},
	})

	req := httptest.NewRequest("GET", "/api/data", nil)
	req.Header.Set("Origin", "https://app.example.com")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("expected origin header on actual request")
	}
	if rr.Header().Get("Access-Control-Expose-Headers") != "X-Request-Id" {
		t.Fatalf("expected exposed headers, got %q", rr.Header().Get("Access-Control-Expose-Headers"))
	}
}

func TestCORS_NoOriginPassthrough(t *testing.T) {
	handler := newCORSHandler(CORSConfig{
		AllowedOrigins: []string{"https://app.example.com"},
	})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("expected no CORS headers for same-origin request")
	}
}

func TestCORS_WildcardOrigin(t *testing.T) {
	handler := newCORSHandler(CORSConfig{
		AllowedOrigins: []string{"https://*.sentiae.com"},
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://app.sentiae.com")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Access-Control-Allow-Origin") != "https://app.sentiae.com" {
		t.Fatalf("expected wildcard match, got %q", rr.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORS_WildcardOriginNoMatch(t *testing.T) {
	handler := newCORSHandler(CORSConfig{
		AllowedOrigins: []string{"https://*.sentiae.com"},
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://evil.com")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("expected no match for non-matching wildcard origin")
	}
}

func TestMatchOrigin(t *testing.T) {
	tests := []struct {
		pattern string
		origin  string
		want    bool
	}{
		{"https://example.com", "https://example.com", true},
		{"https://example.com", "https://other.com", false},
		{"https://*.example.com", "https://app.example.com", true},
		{"https://*.example.com", "https://a.b.example.com", true},
		{"https://*.example.com", "https://example.com", false},
		{"https://*.example.com", "https://.example.com", false},
	}

	for _, tt := range tests {
		got := matchOrigin(tt.pattern, tt.origin)
		if got != tt.want {
			t.Errorf("matchOrigin(%q, %q) = %v, want %v", tt.pattern, tt.origin, got, tt.want)
		}
	}
}

// TestCORS_CredentialedWildcardNeverReflects locks the credentialed-wildcard
// rule: with AllowCredentials, a pattern containing '*' (bare "*" or a pattern
// such as "https://*.example.com") never matches, so no origin is reflected
// with credentials through it. Exact origins still reflect with credentials,
// and a non-credentialed "*" still reflects as before.
//
// CONTROL: drop the credentialed-wildcard filter in CORS and the "credentialed"
// wildcard rows fail with an ACAO/ACAC present.
func TestCORS_CredentialedWildcardNeverReflects(t *testing.T) {
	tests := []struct {
		name          string
		cfg           CORSConfig
		origin        string
		preflight     bool
		wantOrigin    string
		wantCreds     string
		wantPreflight bool
	}{
		{
			name:   "credentialed bare * does not reflect evil origin",
			cfg:    CORSConfig{AllowedOrigins: []string{"*"}, AllowCredentials: true},
			origin: "https://evil.example",
		},
		{
			name:      "credentialed bare * does not answer evil preflight",
			cfg:       CORSConfig{AllowedOrigins: []string{"*"}, AllowCredentials: true},
			origin:    "https://evil.example",
			preflight: true,
		},
		{
			name:   "credentialed bare * does not match a literal * origin",
			cfg:    CORSConfig{AllowedOrigins: []string{"*"}, AllowCredentials: true},
			origin: "*",
		},
		{
			name:   "credentialed subdomain pattern does not reflect",
			cfg:    CORSConfig{AllowedOrigins: []string{"https://*.example.com"}, AllowCredentials: true},
			origin: "https://app.example.com",
		},
		{
			name:       "credentialed exact origin reflects with credentials",
			cfg:        CORSConfig{AllowedOrigins: []string{"https://app.example.com"}, AllowCredentials: true},
			origin:     "https://app.example.com",
			wantOrigin: "https://app.example.com",
			wantCreds:  "true",
		},
		{
			name:          "credentialed exact origin beside a * still reflects on preflight",
			cfg:           CORSConfig{AllowedOrigins: []string{"*", "https://app.example.com"}, AllowCredentials: true},
			origin:        "https://app.example.com",
			preflight:     true,
			wantOrigin:    "https://app.example.com",
			wantCreds:     "true",
			wantPreflight: true,
		},
		{
			name:       "non-credentialed bare * still reflects",
			cfg:        CORSConfig{AllowedOrigins: []string{"*"}},
			origin:     "https://evil.example",
			wantOrigin: "https://evil.example",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method := http.MethodGet
			if tt.preflight {
				method = http.MethodOptions
			}
			req := httptest.NewRequest(method, "/api/data", nil)
			req.Header.Set("Origin", tt.origin)
			if tt.preflight {
				req.Header.Set("Access-Control-Request-Method", "POST")
			}

			rr := httptest.NewRecorder()
			newCORSHandler(tt.cfg).ServeHTTP(rr, req)

			if got := rr.Header().Get("Access-Control-Allow-Origin"); got != tt.wantOrigin {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.wantOrigin)
			}
			if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != tt.wantCreds {
				t.Errorf("Access-Control-Allow-Credentials = %q, want %q", got, tt.wantCreds)
			}
			wantCode := http.StatusOK
			if tt.wantPreflight {
				wantCode = http.StatusNoContent
			}
			if rr.Code != wantCode {
				t.Errorf("status = %d, want %d", rr.Code, wantCode)
			}
		})
	}
}
