// Package authjwt — Phase 8 shared JWT validator.
//
// The dev-mode "trust X-User-ID and X-Organization-ID headers"
// middleware was acceptable for early phases but leaks obvious
// production holes. This package replaces it with real RS256
// verification against identity-service's JWKS endpoint.
//
// Usage:
//
//	v, _ := authjwt.New(authjwt.Config{
//	    JWKSURL:   "http://identity-service:8080/.well-known/jwks.json",
//	    Issuer:    "https://identity.sentiae.io",
//	    Audiences: []string{"sentiae-api"},
//	})
//	claims, err := v.Validate(ctx, bearerToken)
//
// The validator keeps the JWKS cached for Config.CacheTTL (default
// 10 minutes) with single-flight fetches on expiry.
package authjwt

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Config configures a Validator.
type Config struct {
	// JWKSURL is the fully-qualified URL of the identity-service JWKS
	// endpoint (e.g., http://identity:8080/.well-known/jwks.json).
	JWKSURL string
	// Issuer is the expected `iss` claim. Leave empty to skip.
	Issuer string
	// Audiences lists acceptable `aud` values. Empty = skip.
	Audiences []string
	// CacheTTL controls how long a fetched JWKS is reused. Default 10m.
	CacheTTL time.Duration
	// HTTPClient overrides the default 10s-timeout client.
	HTTPClient *http.Client
}

// Claims is the subset of JWT claims Sentiae services read from a
// validated token.
type Claims struct {
	UserID         uuid.UUID
	OrganizationID uuid.UUID
	Email          string
	Scopes         []string
	// PlatformAdmin is populated from the "platform_admin" boolean
	// claim when identity-service issues tokens for users holding
	// the platform-admin role. §18.2 — downstream services use this
	// to gate cross-tenant endpoints.
	PlatformAdmin bool
	// EntsHash is the canonical sha256 of the org's effective
	// entitlement set (Paradigm Shift §A.7). Downstream services
	// resolve this hash through Redis to obtain the full entitlement
	// list for entitlement-gated endpoints.
	EntsHash string
	Raw      jwt.MapClaims
}

// Validator verifies JWTs against a remote JWKS.
type Validator struct {
	cfg    Config
	client *http.Client

	mu       sync.RWMutex
	keys     map[string]*rsa.PublicKey
	fetched  time.Time
	fetchErr error

	singleflight sync.Mutex
}

// New builds a Validator. Returns error when Config is unusable.
func New(cfg Config) (*Validator, error) {
	if cfg.JWKSURL == "" {
		return nil, errors.New("authjwt: JWKSURL is required")
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 10 * time.Minute
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Validator{cfg: cfg, client: client, keys: map[string]*rsa.PublicKey{}}, nil
}

// Validate parses and verifies token. Returns the extracted claims on
// success or a descriptive error otherwise. A stale cache is refreshed
// once per call if the token references a key id the validator hasn't
// seen yet — handles key-rotation without a redeploy.
func (v *Validator) Validate(ctx context.Context, token string) (*Claims, error) {
	if token == "" {
		return nil, errors.New("authjwt: empty token")
	}
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, _ := t.Header["kid"].(string)
		key, err := v.keyForKid(ctx, kid)
		if err != nil {
			return nil, err
		}
		return key, nil
	}, jwt.WithValidMethods([]string{"RS256", "RS384", "RS512"}))
	if err != nil {
		return nil, fmt.Errorf("authjwt: parse: %w", err)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok || !parsed.Valid {
		return nil, errors.New("authjwt: token not valid")
	}
	if v.cfg.Issuer != "" {
		if iss, _ := claims["iss"].(string); iss != v.cfg.Issuer {
			return nil, fmt.Errorf("authjwt: wrong issuer %q", iss)
		}
	}
	if len(v.cfg.Audiences) > 0 && !audienceMatch(claims["aud"], v.cfg.Audiences) {
		return nil, errors.New("authjwt: audience mismatch")
	}

	out := &Claims{Raw: claims}
	if sub, _ := claims["sub"].(string); sub != "" {
		if id, err := uuid.Parse(sub); err == nil {
			out.UserID = id
		}
	}
	if org, _ := claims["organization_id"].(string); org != "" {
		if id, err := uuid.Parse(org); err == nil {
			out.OrganizationID = id
		}
	}
	out.Email, _ = claims["email"].(string)
	if raw, ok := claims["scopes"].([]any); ok {
		for _, s := range raw {
			if str, ok := s.(string); ok {
				out.Scopes = append(out.Scopes, str)
			}
		}
	}
	if v, ok := claims["platform_admin"].(bool); ok {
		out.PlatformAdmin = v
	}
	if v, ok := claims["ents_hash"].(string); ok {
		out.EntsHash = v
	}
	return out, nil
}

// keyForKid returns the cached key, refreshing JWKS if needed.
func (v *Validator) keyForKid(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	k, ok := v.keys[kid]
	fresh := time.Since(v.fetched) < v.cfg.CacheTTL
	v.mu.RUnlock()
	if ok && fresh {
		return k, nil
	}
	if err := v.refresh(ctx); err != nil {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	k, ok = v.keys[kid]
	if !ok {
		if kid == "" {
			for _, any := range v.keys {
				return any, nil // tokens without kid — first key wins
			}
		}
		return nil, fmt.Errorf("authjwt: unknown kid %q", kid)
	}
	return k, nil
}

// refresh performs a single-flight JWKS fetch. Concurrent callers
// wait for the one in-flight request rather than hammer identity-
// service on burst traffic.
func (v *Validator) refresh(ctx context.Context) error {
	v.singleflight.Lock()
	defer v.singleflight.Unlock()
	// Recheck after acquiring the lock — another goroutine may have
	// already refreshed while we waited.
	v.mu.RLock()
	fresh := time.Since(v.fetched) < v.cfg.CacheTTL && len(v.keys) > 0
	v.mu.RUnlock()
	if fresh {
		return v.fetchErr
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("authjwt: fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("authjwt: jwks status %d", resp.StatusCode)
	}
	var doc struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("authjwt: parse jwks: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		e := int(new(big.Int).SetBytes(eBytes).Int64())
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}
	}
	if len(keys) == 0 {
		return errors.New("authjwt: no usable RSA keys in JWKS")
	}

	v.mu.Lock()
	v.keys = keys
	v.fetched = time.Now()
	v.fetchErr = nil
	v.mu.Unlock()
	return nil
}

// audienceMatch accepts `aud` as string or []string/[]any.
func audienceMatch(aud any, accepted []string) bool {
	switch t := aud.(type) {
	case string:
		for _, a := range accepted {
			if a == t {
				return true
			}
		}
	case []any:
		for _, v := range t {
			s, _ := v.(string)
			for _, a := range accepted {
				if a == s {
					return true
				}
			}
		}
	}
	return false
}
