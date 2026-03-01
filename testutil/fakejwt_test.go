package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestFakeJWT_BasicToken(t *testing.T) {
	token := FakeJWT(t, "user-123", "org-456", nil, nil)
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	// Parse and verify.
	parsed, err := jwt.ParseWithClaims(token, &fakeJWTClaims{}, func(t *jwt.Token) (any, error) {
		return &TestRSAKey().PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}))
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}

	claims := parsed.Claims.(*fakeJWTClaims)
	if claims.Subject != "user-123" {
		t.Errorf("expected subject user-123, got %s", claims.Subject)
	}
	if claims.Issuer != "testutil" {
		t.Errorf("expected issuer testutil, got %s", claims.Issuer)
	}
	if len(claims.Scopes) != 1 || claims.Scopes[0] != "org:org-456" {
		t.Errorf("expected scopes [org:org-456], got %v", claims.Scopes)
	}
}

func TestFakeJWT_ExplicitScopes(t *testing.T) {
	scopes := []string{"admin", "org:org-1", "org:org-2"}
	token := FakeJWT(t, "user-1", "org-1", scopes, nil)

	parsed, err := jwt.ParseWithClaims(token, &fakeJWTClaims{}, func(t *jwt.Token) (any, error) {
		return &TestRSAKey().PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}))
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}

	claims := parsed.Claims.(*fakeJWTClaims)
	if len(claims.Scopes) != 3 {
		t.Errorf("expected 3 scopes, got %d: %v", len(claims.Scopes), claims.Scopes)
	}
}

func TestFakeJWT_WithOptions(t *testing.T) {
	opts := &FakeJWTOptions{
		Issuer:    "custom-issuer",
		Audience:  "custom-audience",
		Email:     "test@example.com",
		ExpiresIn: 5 * time.Minute,
		Kid:       "custom-kid",
	}
	token := FakeJWT(t, "user-1", "", nil, opts)

	parser := jwt.NewParser(jwt.WithValidMethods([]string{"RS256"}))
	parsed, err := parser.ParseWithClaims(token, &fakeJWTClaims{}, func(tok *jwt.Token) (any, error) {
		kid, ok := tok.Header["kid"].(string)
		if !ok || kid != "custom-kid" {
			t.Errorf("expected kid custom-kid, got %v", tok.Header["kid"])
		}
		return &TestRSAKey().PublicKey, nil
	})
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}

	claims := parsed.Claims.(*fakeJWTClaims)
	if claims.Issuer != "custom-issuer" {
		t.Errorf("expected issuer custom-issuer, got %s", claims.Issuer)
	}
	if claims.Email != "test@example.com" {
		t.Errorf("expected email test@example.com, got %s", claims.Email)
	}
}

func TestFakeJWT_CustomKey(t *testing.T) {
	customKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	opts := &FakeJWTOptions{Key: customKey}
	token := FakeJWT(t, "user-1", "org-1", nil, opts)

	// Should fail with default key.
	_, err = jwt.Parse(token, func(t *jwt.Token) (any, error) {
		return &TestRSAKey().PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}))
	if err == nil {
		t.Fatal("expected validation error with wrong key")
	}

	// Should succeed with custom key.
	_, err = jwt.Parse(token, func(t *jwt.Token) (any, error) {
		return &customKey.PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}))
	if err != nil {
		t.Fatalf("expected success with correct key: %v", err)
	}
}

func TestFakeJWT_NoOrgID(t *testing.T) {
	token := FakeJWT(t, "user-1", "", nil, nil)

	parsed, err := jwt.ParseWithClaims(token, &fakeJWTClaims{}, func(t *jwt.Token) (any, error) {
		return &TestRSAKey().PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}))
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}

	claims := parsed.Claims.(*fakeJWTClaims)
	if claims.Scopes != nil {
		t.Errorf("expected nil scopes when no orgID, got %v", claims.Scopes)
	}
}

func TestTestRSAKey_Stable(t *testing.T) {
	k1 := TestRSAKey()
	k2 := TestRSAKey()
	if k1 != k2 {
		t.Error("TestRSAKey should return same instance")
	}
}
