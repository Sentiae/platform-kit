package authjwt

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// newJWKSServer generates an RSA key, mints a JWT with given claims,
// and exposes JWKS over httptest — gives us an end-to-end validator
// exercise without touching identity-service.
func newJWKSServer(t *testing.T, kid string) (*httptest.Server, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	nB := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
	eB := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []any{
				map[string]any{"kid": kid, "kty": "RSA", "alg": "RS256", "use": "sig", "n": nB, "e": eB},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, key
}

func mintToken(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestValidator_RoundTrip(t *testing.T) {
	srv, key := newJWKSServer(t, "k1")
	v, _ := New(Config{JWKSURL: srv.URL, Issuer: "https://sentiae", Audiences: []string{"sentiae-api"}})
	uid := uuid.New()
	oid := uuid.New()
	tok := mintToken(t, key, "k1", jwt.MapClaims{
		"iss":             "https://sentiae",
		"aud":             "sentiae-api",
		"sub":             uid.String(),
		"organization_id": oid.String(),
		"email":           "user@sentiae.io",
		"scopes":          []any{"spec:read", "spec:write"},
		"exp":             time.Now().Add(time.Hour).Unix(),
	})
	claims, err := v.Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("validate err: %v", err)
	}
	if claims.UserID != uid {
		t.Fatalf("user id mismatch: got %s want %s", claims.UserID, uid)
	}
	if claims.OrganizationID != oid {
		t.Fatalf("org id mismatch")
	}
	if len(claims.Scopes) != 2 {
		t.Fatalf("scopes: %v", claims.Scopes)
	}
}

func TestValidator_WrongIssuer(t *testing.T) {
	srv, key := newJWKSServer(t, "k1")
	v, _ := New(Config{JWKSURL: srv.URL, Issuer: "https://sentiae"})
	tok := mintToken(t, key, "k1", jwt.MapClaims{
		"iss": "https://evil",
		"sub": uuid.New().String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Fatal("expected error on wrong issuer")
	}
}

func TestValidator_ExpiredToken(t *testing.T) {
	srv, key := newJWKSServer(t, "k1")
	v, _ := New(Config{JWKSURL: srv.URL})
	tok := mintToken(t, key, "k1", jwt.MapClaims{
		"sub": uuid.New().String(),
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Fatal("expected error on expired token")
	}
}

func TestValidator_MissingJWKSURL(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error on empty JWKSURL")
	}
}

func TestValidator_HSNotAccepted(t *testing.T) {
	srv, _ := newJWKSServer(t, "k1")
	v, _ := New(Config{JWKSURL: srv.URL})
	// Mint an HS256 token (shared-secret). Must be rejected.
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": uuid.New().String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	s, _ := tok.SignedString([]byte("shhh"))
	if _, err := v.Validate(context.Background(), s); err == nil {
		t.Fatal("expected HS256 rejection")
	}
}

// Silence unused import that would otherwise land on some build
// configs after edits.
var _ = fmt.Sprint
