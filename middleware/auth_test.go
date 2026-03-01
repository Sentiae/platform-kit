package middleware

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	platformerrors "github.com/sentiae/platform-kit/errors"
	"github.com/sentiae/platform-kit/logger"
)

// testValidator is a simple TokenValidator for tests.
type testValidator struct {
	claims Claims
	err    error
}

func (v *testValidator) Validate(_ context.Context, _ string) (Claims, error) {
	return v.claims, v.err
}

func TestAuth_ValidToken(t *testing.T) {
	v := &testValidator{
		claims: Claims{
			Subject: "user-123",
			Email:   "test@example.com",
			Scopes:  []string{"org:abc"},
		},
	}

	var gotClaims Claims
	var gotOK bool
	var gotUserID string

	handler := Auth(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims, gotOK = GetClaims(r.Context())
		gotUserID, _ = r.Context().Value(logger.UserIDKey).(string)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer test-token")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !gotOK {
		t.Fatal("expected claims in context")
	}
	if gotClaims.Subject != "user-123" {
		t.Fatalf("expected subject user-123, got %q", gotClaims.Subject)
	}
	if gotClaims.Email != "test@example.com" {
		t.Fatalf("expected email, got %q", gotClaims.Email)
	}
	if gotUserID != "user-123" {
		t.Fatalf("expected user ID in context, got %q", gotUserID)
	}
}

func TestAuth_MissingHeader(t *testing.T) {
	v := &testValidator{}

	handler := Auth(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}

	var resp platformerrors.ErrorResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Status != 401 {
		t.Fatalf("expected 401 in body, got %d", resp.Status)
	}
}

func TestAuth_InvalidFormat(t *testing.T) {
	v := &testValidator{}

	handler := Auth(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestAuth_InvalidToken(t *testing.T) {
	v := &testValidator{err: jwt.ErrSignatureInvalid}

	handler := Auth(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer bad-token")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestAuth_NoXUserIDFallback(t *testing.T) {
	// Ensure X-User-ID header is NOT used as fallback.
	v := &testValidator{err: jwt.ErrSignatureInvalid}

	handler := Auth(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called when token is invalid, even with X-User-ID")
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer bad-token")
	req.Header.Set("X-User-ID", "user-from-header")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 even with X-User-ID, got %d", rr.Code)
	}
}

// --- RS256Validator tests ---

func generateTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func signToken(t *testing.T, key *rsa.PrivateKey, claims jwt.Claims, kid string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if kid != "" {
		token.Header["kid"] = kid
	}
	s, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

func TestRS256Validator_ValidToken(t *testing.T) {
	key := generateTestKey(t)

	v := &RS256Validator{
		KeyFunc: func(kid string) (*rsa.PublicKey, error) {
			return &key.PublicKey, nil
		},
	}

	type customClaims struct {
		jwt.RegisteredClaims
		Email  string   `json:"email"`
		Scopes []string `json:"scopes"`
	}

	tokenStr := signToken(t, key, &customClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-456",
			Issuer:    "test-issuer",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		Email:  "user@example.com",
		Scopes: []string{"org:xyz"},
	}, "key-1")

	claims, err := v.Validate(context.Background(), tokenStr)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.Subject != "user-456" {
		t.Fatalf("expected subject user-456, got %q", claims.Subject)
	}
	if claims.Issuer != "test-issuer" {
		t.Fatalf("expected issuer test-issuer, got %q", claims.Issuer)
	}
	if claims.Email != "user@example.com" {
		t.Fatalf("expected email, got %q", claims.Email)
	}
	if len(claims.Scopes) != 1 || claims.Scopes[0] != "org:xyz" {
		t.Fatalf("expected scopes [org:xyz], got %v", claims.Scopes)
	}
}

func TestRS256Validator_ExpiredToken(t *testing.T) {
	key := generateTestKey(t)

	v := &RS256Validator{
		KeyFunc: func(kid string) (*rsa.PublicKey, error) {
			return &key.PublicKey, nil
		},
	}

	tokenStr := signToken(t, key, &jwt.RegisteredClaims{
		Subject:   "user-789",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
	}, "")

	_, err := v.Validate(context.Background(), tokenStr)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestRS256Validator_WrongKey(t *testing.T) {
	signKey := generateTestKey(t)
	otherKey := generateTestKey(t)

	v := &RS256Validator{
		KeyFunc: func(kid string) (*rsa.PublicKey, error) {
			return &otherKey.PublicKey, nil // wrong key
		},
	}

	tokenStr := signToken(t, signKey, &jwt.RegisteredClaims{
		Subject:   "user",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}, "")

	_, err := v.Validate(context.Background(), tokenStr)
	if err == nil {
		t.Fatal("expected error for wrong key")
	}
}

func TestRS256Validator_IssuerValidation(t *testing.T) {
	key := generateTestKey(t)

	v := &RS256Validator{
		KeyFunc: func(kid string) (*rsa.PublicKey, error) {
			return &key.PublicKey, nil
		},
		Issuer: "expected-issuer",
	}

	tokenStr := signToken(t, key, &jwt.RegisteredClaims{
		Subject:   "user",
		Issuer:    "wrong-issuer",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}, "")

	_, err := v.Validate(context.Background(), tokenStr)
	if err == nil {
		t.Fatal("expected error for wrong issuer")
	}
}
