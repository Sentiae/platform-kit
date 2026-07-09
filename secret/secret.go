// Package secret is the platform's SecretResolver (integration-contracts P14):
// the one seam that turns an opaque secret_ref into a concrete value at the
// point of use — at deploy time (delivery), at VM boot (runtime-fleet), and
// for a node's vendor credentials (node-service). Secrets are fetched from
// Vault on demand, audited, and NEVER written to a descriptor at rest or
// logged (CLAUDE.md §26). The value is carried in a SecretValue that redacts
// itself in every string/log/JSON context, so it cannot leak by accident.
//
//	r := secret.NewVaultResolver(vaultClient)              // config.VaultClient satisfies vaultGetter
//	ref := secret.TenantRef(org, "prod/app", "db_password") // tenants/<org>/prod/app#db_password
//	v, err := r.Resolve(ctx, ref, secret.Principal{Service: "delivery", OrgID: org.String()})
//	dsn := "postgres://app:" + v.Reveal() + "@..."          // Reveal() only at the point of use
package secret

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/sentiae/platform-kit/logger"
)

// Errors (registered by callers via platformerrors at their boundary).
var (
	// ErrInvalidSecretRef is returned when the ref is not "<path>#<field>".
	ErrInvalidSecretRef = errors.New("secret: invalid secret_ref (want \"<path>#<field>\")")
	// ErrSecretNotFound is returned when Vault has no such path/field.
	ErrSecretNotFound = errors.New("secret: not found")
	// ErrUnscopedSecretRef is returned when the ref is not tenant-namespaced
	// as "tenants/<org-uuid>/<subpath>#<field>".
	ErrUnscopedSecretRef = errors.New("secret: ref is not tenant-namespaced (want \"tenants/<org-uuid>/<subpath>#<field>\")")
	// ErrCrossTenantSecret is returned when the caller's verified org does not
	// own the ref (I28): resolution is refused without any Vault call, so the
	// ref's existence cannot be probed across tenants.
	ErrCrossTenantSecret = errors.New("secret: cross-tenant resolution denied")
)

// Principal identifies who is resolving a secret. It is never the secret's
// owner — it is the caller (a service, or a user acting through one).
//
// OrgID is the caller's VERIFIED tenant: the resolving service MUST bind it
// from the JWKS/SVID-verified tenant.Principal (never from a request header),
// because Resolve ENFORCES ref.org == OrgID (I28) — it is authoritative, not
// merely audited.
type Principal struct {
	Service string // the resolving service, e.g. "delivery", "runtime-fleet", "node-service"
	Subject string // optional: the acting user/agent id
	OrgID   string // REQUIRED + ENFORCED: the caller's verified tenant (org uuid)
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
//
// Refs are tenant-namespaced: "tenants/<org-uuid>/<subpath>#<field>". A secret
// resolves only for a principal whose verified OrgID equals the ref's org
// (I28); cross-tenant or unscoped resolution is refused at the seam.
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
// tenant-namespaced: "tenants/<org-uuid>/<subpath>#<field>" (e.g.
// "tenants/c883c1d0-249a-4262-bf9c-f4c30f0850b6/prod/app#db_password"), where
// "tenants/<org-uuid>/<subpath>" is a KV path under the client's mount and
// <field> is the key within it.
//
// I28 guarantee: Resolve enforces ref.org == principal.OrgID structurally,
// BEFORE any Vault call — a caller whose verified tenant does not own the ref
// is denied without an existence oracle. The ref string never conveys
// authority.
type VaultResolver struct {
	vault vaultGetter
}

// NewVaultResolver constructs a resolver over a Vault client.
func NewVaultResolver(v vaultGetter) *VaultResolver { return &VaultResolver{vault: v} }

var _ Resolver = (*VaultResolver)(nil)

// Resolve fetches the secret_ref's value from Vault and audits the access.
// It enforces I28 (ref.org == principal's verified org) BEFORE any Vault call,
// so a cross-tenant caller cannot even learn whether the ref exists. The value
// is never logged; only the ref (a reference, not the secret) and the
// principal are recorded.
func (r *VaultResolver) Resolve(ctx context.Context, secretRef string, principal Principal) (SecretValue, error) {
	_, path, field, err := authorizeRef(ctx, secretRef, principal)
	if err != nil {
		return SecretValue{}, err
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

// authorizeRef is the single I28 authorization codepath shared by every
// Resolver over tenant-namespaced refs. It parses the ref, requires an
// authenticated caller, and enforces ref.org == principal's verified org
// BEFORE any I/O — so a cross-tenant probe cannot use resolution as an
// existence oracle. On success it returns the ref's org plus the derived KV
// path and field. It performs NO Vault/KV/KEK call itself; that oracle-free
// guarantee is what every resolver depends on.
func authorizeRef(ctx context.Context, secretRef string, principal Principal) (org uuid.UUID, path, field string, err error) {
	refOrg, path, field, ok := parseTenantRef(secretRef)
	if !ok {
		return uuid.Nil, "", "", ErrUnscopedSecretRef
	}

	// Require an authenticated tenant: an anonymous/unscoped caller resolves
	// nothing.
	if principal.OrgID == "" {
		logger.FromContext(ctx).Warn("secret resolve denied: unscoped caller",
			"secret_ref", secretRef, "principal", principal.String())
		return uuid.Nil, "", "", ErrCrossTenantSecret
	}
	principalOrg, err := uuid.Parse(principal.OrgID)
	if err != nil {
		logger.FromContext(ctx).Warn("secret resolve denied: unparseable caller org",
			"secret_ref", secretRef, "principal", principal.String())
		return uuid.Nil, "", "", ErrCrossTenantSecret
	}

	// I28: enforce tenant ownership WITHOUT any I/O, so a cross-tenant probe
	// cannot use resolution as an existence oracle.
	if refOrg != principalOrg {
		logger.FromContext(ctx).Warn("secret resolve denied: cross-tenant",
			"secret_ref", secretRef, "principal", principal.String())
		return uuid.Nil, "", "", ErrCrossTenantSecret
	}

	return refOrg, path, field, nil
}

// parseRef splits "<path>#<field>". Both parts must be non-empty.
func parseRef(ref string) (path, field string, ok bool) {
	i := strings.LastIndex(ref, "#")
	if i <= 0 || i == len(ref)-1 {
		return "", "", false
	}
	return ref[:i], ref[i+1:], true
}

// TenantRef builds a tenant-namespaced secret_ref:
// "tenants/<org-uuid>/<subpath>#<field>".
func TenantRef(org uuid.UUID, subpath, field string) string {
	return "tenants/" + org.String() + "/" + subpath + "#" + field
}

// parseTenantRef parses a tenant-namespaced ref
// "tenants/<org-uuid>/<subpath>#<field>", returning the ref's org, the full
// KV path ("tenants/<org-uuid>/<subpath>"), and the field. ok is false for any
// malformed or non-namespaced ref (including an empty <subpath>).
func parseTenantRef(secretRef string) (org uuid.UUID, path, field string, ok bool) {
	path, field, ok = parseRef(secretRef)
	if !ok {
		return uuid.Nil, "", "", false
	}
	const prefix = "tenants/"
	rest, found := strings.CutPrefix(path, prefix)
	if !found {
		return uuid.Nil, "", "", false
	}
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 || slash == len(rest)-1 {
		// need a non-empty org segment AND a non-empty subpath after it.
		return uuid.Nil, "", "", false
	}
	org, err := uuid.Parse(rest[:slash])
	if err != nil {
		return uuid.Nil, "", "", false
	}
	return org, path, field, true
}

func isNotFound(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not found") || strings.Contains(s, "no value found") ||
		strings.Contains(s, "404")
}
