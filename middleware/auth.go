package middleware

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	platformerrors "github.com/sentiae/platform-kit/errors"
	"github.com/sentiae/platform-kit/logger"
)

// TokenValidator validates a JWT token string and returns its claims.
type TokenValidator interface {
	Validate(ctx context.Context, token string) (Claims, error)
}

// Claims holds parsed JWT claims.
type Claims struct {
	// Subject is the JWT "sub" claim (typically the user ID).
	Subject string
	// Issuer is the JWT "iss" claim.
	Issuer string
	// Email is a custom "email" claim.
	Email string
	// Scopes is a custom "scopes" claim (e.g. ["org:<uuid>"]).
	Scopes []string
	// PlatformAdmin is a custom "platform_admin" boolean claim that
	// downstream services (e.g. ops-service RequirePlatformAdmin) use
	// to gate cross-tenant admin endpoints. Populated by identity-
	// service when the user holds the platform_admin role. §18.2.
	PlatformAdmin bool
}

// claimsCtxKey is the unexported context key for storing parsed claims.
type claimsCtxKey struct{}

// Auth returns middleware that validates JWT tokens from the Authorization header.
// It rejects requests without a valid Bearer token and never falls back to
// trusting headers like X-User-ID.
func Auth(validator TokenValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				writeAuthError(w, "authorization header required")
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
				writeAuthError(w, "invalid authorization header format")
				return
			}

			claims, err := validator.Validate(r.Context(), parts[1])
			if err != nil {
				writeAuthError(w, "invalid or expired token")
				return
			}

			ctx := r.Context()
			ctx = context.WithValue(ctx, logger.UserIDKey, claims.Subject)
			ctx = context.WithValue(ctx, claimsCtxKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetClaims returns the JWT claims from the context, or zero-value Claims if not set.
func GetClaims(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsCtxKey{}).(Claims)
	return c, ok
}

// InjectClaimsForTest stashes the supplied claims on the context using
// the same unexported key that Auth() would. Only intended for tests —
// production code must route through Auth() so the token is validated.
func InjectClaimsForTest(ctx context.Context, claims Claims) context.Context {
	return context.WithValue(ctx, claimsCtxKey{}, claims)
}

func writeAuthError(w http.ResponseWriter, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUnauthorized)
	resp := platformerrors.ErrorResponse{
		Type:   "about:blank",
		Title:  http.StatusText(http.StatusUnauthorized),
		Status: http.StatusUnauthorized,
		Detail: detail,
	}
	json.NewEncoder(w).Encode(resp)
}

// RS256Validator validates JWT tokens signed with RS256.
type RS256Validator struct {
	// KeyFunc returns the RSA public key for the given key ID (kid).
	// If the token has no kid header, an empty string is passed.
	KeyFunc func(kid string) (*rsa.PublicKey, error)

	// Issuer, if non-empty, is validated against the token's "iss" claim.
	Issuer string

	// Audience, if non-empty, is validated against the token's "aud" claim.
	Audience string
}

// jwtClaims extends jwt.RegisteredClaims with custom fields.
type jwtClaims struct {
	jwt.RegisteredClaims
	Email  string   `json:"email,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
}

// Validate parses and validates the token, returning the extracted claims.
func (v *RS256Validator) Validate(_ context.Context, tokenString string) (Claims, error) {
	var opts []jwt.ParserOption
	opts = append(opts, jwt.WithValidMethods([]string{"RS256"}))
	if v.Issuer != "" {
		opts = append(opts, jwt.WithIssuer(v.Issuer))
	}
	if v.Audience != "" {
		opts = append(opts, jwt.WithAudience(v.Audience))
	}

	token, err := jwt.ParseWithClaims(tokenString, &jwtClaims{}, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		key, err := v.KeyFunc(kid)
		if err != nil {
			return nil, fmt.Errorf("key lookup: %w", err)
		}
		return key, nil
	}, opts...)
	if err != nil {
		return Claims{}, fmt.Errorf("token validation: %w", err)
	}

	parsed, ok := token.Claims.(*jwtClaims)
	if !ok {
		return Claims{}, fmt.Errorf("unexpected claims type")
	}

	return Claims{
		Subject: parsed.Subject,
		Issuer:  parsed.Issuer,
		Email:   parsed.Email,
		Scopes:  parsed.Scopes,
	}, nil
}
