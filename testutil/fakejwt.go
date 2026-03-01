package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	testKey     *rsa.PrivateKey
	testKeyOnce sync.Once
)

// TestRSAKey returns a shared RSA private key for test JWT signing.
// The key is generated once and reused across all tests in the process.
func TestRSAKey() *rsa.PrivateKey {
	testKeyOnce.Do(func() {
		var err error
		testKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic("testutil: generate RSA key: " + err.Error())
		}
	})
	return testKey
}

// FakeJWTOptions configures optional fields for FakeJWT.
type FakeJWTOptions struct {
	// Issuer sets the "iss" claim. Default: "testutil".
	Issuer string
	// Audience sets the "aud" claim. Default: "sentiae".
	Audience string
	// Email sets the custom "email" claim. Default: empty.
	Email string
	// ExpiresIn sets the token lifetime. Default: 1 hour.
	ExpiresIn time.Duration
	// Kid sets the JWT "kid" header. Default: "test-key-1".
	Kid string
	// Key is the RSA private key to sign with. Default: TestRSAKey().
	Key *rsa.PrivateKey
}

// fakeJWTClaims includes both registered and custom claims.
type fakeJWTClaims struct {
	jwt.RegisteredClaims
	Email  string   `json:"email,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
}

// FakeJWT generates a valid RS256-signed JWT token for use in tests.
// The returned token string includes the standard claims (sub, iss, aud, exp, iat)
// and custom claims (email, scopes).
//
// Example:
//
//	token := testutil.FakeJWT(t, "user-123", "org-456", []string{"org:org-456"}, nil)
//	req.Header.Set("Authorization", "Bearer "+token)
//
//	// With options:
//	token := testutil.FakeJWT(t, "user-123", "org-456", nil, &testutil.FakeJWTOptions{
//	    Email:     "test@example.com",
//	    ExpiresIn: 5 * time.Minute,
//	})
func FakeJWT(t *testing.T, userID, orgID string, scopes []string, opts *FakeJWTOptions) string {
	t.Helper()

	o := FakeJWTOptions{
		Issuer:    "testutil",
		Audience:  "sentiae",
		ExpiresIn: time.Hour,
		Kid:       "test-key-1",
	}
	if opts != nil {
		if opts.Issuer != "" {
			o.Issuer = opts.Issuer
		}
		if opts.Audience != "" {
			o.Audience = opts.Audience
		}
		if opts.Email != "" {
			o.Email = opts.Email
		}
		if opts.ExpiresIn != 0 {
			o.ExpiresIn = opts.ExpiresIn
		}
		if opts.Kid != "" {
			o.Kid = opts.Kid
		}
		if opts.Key != nil {
			o.Key = opts.Key
		}
	}

	key := o.Key
	if key == nil {
		key = TestRSAKey()
	}

	// Auto-add org scope if orgID is provided and not already in scopes.
	if orgID != "" && scopes == nil {
		scopes = []string{"org:" + orgID}
	}

	now := time.Now()
	claims := fakeJWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			Issuer:    o.Issuer,
			Audience:  jwt.ClaimStrings{o.Audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(o.ExpiresIn)),
		},
		Email:  o.Email,
		Scopes: scopes,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = o.Kid

	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("testutil.FakeJWT: sign token: %v", err)
	}

	return signed
}
