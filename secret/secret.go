// Package secret is the platform's SecretResolver (integration-contracts P14):
// the one seam that turns an opaque secret_ref into a concrete value at the
// point of use — at deploy time (delivery), at VM boot (runtime-fleet), and
// for a node's vendor credentials (node-service). Secrets are fetched from
// Vault on demand, audited, and NEVER written to a descriptor at rest or
// logged (CLAUDE.md §26). The value is carried in a SecretValue that redacts
// itself in every string/log/JSON context, so it cannot leak by accident.
//
//	r := secret.NewVaultResolver(vaultClient)              // config.VaultClient satisfies vaultGetter
//	v, err := r.Resolve(ctx, "sentiae/prod/app#db_password", secret.Principal{Service: "delivery"})
//	dsn := "postgres://app:" + v.Reveal() + "@..."          // Reveal() only at the point of use
package secret

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sentiae/platform-kit/logger"
)

// Errors (registered by callers via platformerrors at their boundary).
var (
	// ErrInvalidSecretRef is returned when the ref is not "<path>#<field>".
	ErrInvalidSecretRef = errors.New("secret: invalid secret_ref (want \"<path>#<field>\")")
	// ErrSecretNotFound is returned when Vault has no such path/field.
	ErrSecretNotFound = errors.New("secret: not found")
)

// Principal identifies who is resolving a secret, for the audit trail. It is
// never the secret's owner — it is the caller (a service, or a user acting
// through one).
type Principal struct {
	Service string // the resolving service, e.g. "delivery", "runtime-fleet", "node-service"
	Subject string // optional: the acting user/agent id
	OrgID   string // optional: tenant scope
}

func (p Principal) String() string {
	s := p.Service
	if p.Subject != "" {
		s += "/" + p.Subject
	}
	if p.OrgID != "" {
		s += "@" + p.OrgID
	}
	return s
}

// SecretValue wraps a resolved secret so it cannot leak through logging,
// formatting, or JSON. Call Reveal() only at the exact point of use.
type SecretValue struct{ value string }

// Reveal returns the plaintext secret. Call site only — never log the result.
func (s SecretValue) Reveal() string { return s.value }

// IsZero reports whether the value is empty.
func (s SecretValue) IsZero() bool { return s.value == "" }

// String redacts — so `%s`, fmt.Println, etc. never print the secret.
func (s SecretValue) String() string { return "[REDACTED]" }

// GoString redacts `%#v`.
func (s SecretValue) GoString() string { return "secret.SecretValue{[REDACTED]}" }

// LogValue redacts the value in slog output (structured logging).
func (s SecretValue) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

// MarshalJSON redacts the value if a SecretValue is ever marshalled.
func (s SecretValue) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }

// Resolver turns a secret_ref into a value. Provider: platform-foundations
// (this Vault impl). Consumers: delivery, runtime-fleet, node-service.
type Resolver interface {
	Resolve(ctx context.Context, secretRef string, principal Principal) (SecretValue, error)
}

// vaultGetter is the minimal Vault surface this resolver needs;
// config.VaultClient satisfies it. Keeping it an interface avoids importing
// all of config and makes the resolver trivially testable.
type vaultGetter interface {
	GetSecret(ctx context.Context, path, key string) (string, error)
}

// VaultResolver resolves secret_refs against Vault KV. secret_ref format is
// "<path>#<field>" (e.g. "sentiae/prod/app#db_password"), where <path> is a
// KV path under the client's mount and <field> is the key within it.
type VaultResolver struct {
	vault vaultGetter
}

// NewVaultResolver constructs a resolver over a Vault client.
func NewVaultResolver(v vaultGetter) *VaultResolver { return &VaultResolver{vault: v} }

var _ Resolver = (*VaultResolver)(nil)

// Resolve fetches the secret_ref's value from Vault and audits the access.
// The value is never logged; only the ref (a reference, not the secret) and
// the principal are recorded.
func (r *VaultResolver) Resolve(ctx context.Context, secretRef string, principal Principal) (SecretValue, error) {
	path, field, ok := parseRef(secretRef)
	if !ok {
		return SecretValue{}, ErrInvalidSecretRef
	}

	val, err := r.vault.GetSecret(ctx, path, field)
	if err != nil {
		// Audit the denied/failed access, then surface a non-leaky error.
		logger.FromContext(ctx).Warn("secret resolve failed",
			"secret_ref", secretRef, "principal", principal.String(), "err", err)
		if isNotFound(err) {
			return SecretValue{}, fmt.Errorf("%w: %s", ErrSecretNotFound, secretRef)
		}
		return SecretValue{}, fmt.Errorf("secret: resolve %s: %w", secretRef, err)
	}

	// Audit the successful access — ref + principal only, never the value.
	logger.FromContext(ctx).Info("secret resolved",
		"secret_ref", secretRef, "principal", principal.String())
	return SecretValue{value: val}, nil
}

// parseRef splits "<path>#<field>". Both parts must be non-empty.
func parseRef(ref string) (path, field string, ok bool) {
	i := strings.LastIndex(ref, "#")
	if i <= 0 || i == len(ref)-1 {
		return "", "", false
	}
	return ref[:i], ref[i+1:], true
}

func isNotFound(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not found") || strings.Contains(s, "no value found") ||
		strings.Contains(s, "404")
}
