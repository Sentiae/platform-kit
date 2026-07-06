package tenant

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/sentiae/platform-kit/authjwt"
	"github.com/sentiae/platform-kit/interceptor"
	"github.com/sentiae/platform-kit/middleware"
)

// JWKSConfig configures a JWKS-backed user-token validator.
type JWKSConfig struct {
	JWKSURL   string
	Issuer    string
	Audiences []string
	CacheTTL  time.Duration
}

// NewJWKSValidator returns a middleware.TokenValidator backed by authjwt
// (JWKS, lazy fetch, kid-rotation) so services get org + scopes into
// middleware.Claims.
func NewJWKSValidator(cfg JWKSConfig) (middleware.TokenValidator, error) {
	v, err := authjwt.New(authjwt.Config{
		JWKSURL:   cfg.JWKSURL,
		Issuer:    cfg.Issuer,
		Audiences: cfg.Audiences,
		CacheTTL:  cfg.CacheTTL,
	})
	if err != nil {
		return nil, err
	}
	return &jwksValidator{v: v}, nil
}

type jwksValidator struct {
	v *authjwt.Validator
}

func (j *jwksValidator) Validate(ctx context.Context, token string) (middleware.Claims, error) {
	c, err := j.v.Validate(ctx, token)
	if err != nil {
		return middleware.Claims{}, err
	}
	subject := ""
	if c.UserID != uuid.Nil {
		subject = c.UserID.String()
	}
	orgID := ""
	if c.OrganizationID != uuid.Nil {
		orgID = c.OrganizationID.String()
	}
	return middleware.Claims{
		Subject:        subject,
		OrganizationID: orgID,
		Email:          c.Email,
		Scopes:         c.Scopes,
		PlatformAdmin:  c.PlatformAdmin,
	}, nil
}

// ServiceTokenValidator validates a service-to-service shared token in
// constant time. It fails closed: an unconfigured (empty) Expected token
// rejects every call.
type ServiceTokenValidator struct {
	Expected string
}

func (v ServiceTokenValidator) Validate(_ context.Context, key string) error {
	if v.Expected == "" {
		return errors.New("service token not configured")
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(v.Expected)) != 1 {
		return errors.New("invalid service token")
	}
	return nil
}

var _ interceptor.APIKeyValidator = ServiceTokenValidator{}
