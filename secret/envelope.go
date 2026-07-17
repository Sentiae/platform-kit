package secret

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	vault "github.com/hashicorp/vault/api"
	"github.com/sentiae/platform-kit/logger"
)

// tenantKEK is the minimal decrypt surface EnvelopeVaultResolver needs from a
// per-tenant KEK (TenantTransit satisfies it). Keeping it an interface makes
// the resolver trivially testable and lets it stay decrypt-only.
type tenantKEK interface {
	Decrypt(ctx context.Context, org uuid.UUID, ciphertext string) ([]byte, error)
}

// EnvelopeVaultResolver resolves tenant-namespaced secret_refs whose KV value
// is not the plaintext but Vault-Transit CIPHERTEXT sealed under the ref org's
// per-tenant KEK (I29 envelope encryption). It shares the exact I28 codepath
// (authorizeRef) with VaultResolver — a cross-tenant caller is denied BEFORE
// any KV or KEK call — then reads the ciphertext blob from KV and unseals it
// with the org's KEK.
//
// Because the blob is sealed under the ref org's key, even a bug that leaked
// the wrong tenant's blob to KV would decrypt to a Vault error, not another
// tenant's secret: cross-tenant read is a cryptographic impossibility.
//
// Its KEK is configured decrypt-only (TransitConfig.AutoCreate:false); a
// decrypt against a missing key fails closed.
type EnvelopeVaultResolver struct {
	kv  vaultGetter
	kek tenantKEK
}

// NewEnvelopeVaultResolver wires a KV getter (config.VaultClient) to a
// per-tenant KEK (a decrypt-only TenantTransit).
func NewEnvelopeVaultResolver(kv vaultGetter, kek tenantKEK) *EnvelopeVaultResolver {
	return &EnvelopeVaultResolver{kv: kv, kek: kek}
}

var _ Resolver = (*EnvelopeVaultResolver)(nil)

// Resolve enforces I28 (authorizeRef, oracle-free) then unseals the ref's
// envelope: KV holds the ciphertext blob, the org's KEK decrypts it. Neither
// the KV read nor the KEK decrypt runs for a cross-tenant caller. The
// plaintext is never logged — only the ref and principal are audited.
func (r *EnvelopeVaultResolver) Resolve(ctx context.Context, secretRef string, principal Principal) (SecretValue, error) {
	org, path, field, err := authorizeRef(ctx, secretRef, principal)
	if err != nil {
		return SecretValue{}, err
	}
	return unsealBlob(ctx, r.kv, r.kek, org, path, field, secretRef, principal)
}

// unsealBlob runs the post-authorization envelope leg shared by every envelope
// resolver: read the sealed blob from KV, decrypt it under the ref org's KEK,
// and audit (ref + principal only, never the value). It performs NO
// authorization — callers MUST run authorizeRef first. Keeping it shared means
// the standing and the per-org-scoped resolvers behave identically (same
// not-found mapping, same audit lines).
func unsealBlob(ctx context.Context, kv vaultGetter, kek tenantKEK, org uuid.UUID, path, field, secretRef string, principal Principal) (SecretValue, error) {
	blob, err := kv.GetSecret(ctx, path, field)
	if err != nil {
		logger.FromContext(ctx).Warn("secret resolve failed",
			"secret_ref", secretRef, "principal", principal.String(), "err", err)
		if isNotFound(err) {
			return SecretValue{}, fmt.Errorf("%w: %s", ErrSecretNotFound, secretRef)
		}
		return SecretValue{}, fmt.Errorf("secret: resolve %s: %w", secretRef, err)
	}

	pt, err := kek.Decrypt(ctx, org, blob)
	if err != nil {
		logger.FromContext(ctx).Warn("secret unseal failed",
			"secret_ref", secretRef, "principal", principal.String(), "err", err)
		return SecretValue{}, fmt.Errorf("secret: unseal %s: %w", secretRef, err)
	}

	logger.FromContext(ctx).Info("secret resolved",
		"secret_ref", secretRef, "principal", principal.String())
	return SecretValue{value: string(pt)}, nil
}

// ScopedEnvelopeVaultResolver is the D-085 Phase-1 hardening of
// EnvelopeVaultResolver: it holds NO standing decrypt capability. Its parent
// Vault client (svc/runtime's JWT-SVID) can do exactly ONE thing —
// mint a child token via the `runtime-tenant` token role — and nothing else.
//
// Per Resolve it (1) runs the same I28 authorizeRef (KEPT as defense-in-depth),
// (2) mints a child token bound server-side to the single per-org named policy
// `secret-tenant-<principal.OrgID>` (via the token role's allowed_policies_glob,
// with a low TTL / num_uses / no-default-policy the role enforces), (3) clones a
// client bearing that child token, and (4) runs the SINGLE KV-read + Transit
// decrypt under it, then lets the token self-expire (no cache, no revoke).
//
// The decrypt keyName still derives from the REF org, while the child token is
// scoped to the PRINCIPAL org. Because authorizeRef guarantees ref.org ==
// principal.OrgID, they match on the happy path — but if authorizeRef were ever
// bypassed with ref.org=B / principal.OrgID=A, the A-scoped child hits
// `decrypt/tenant-B` and Vault returns 403. Cross-tenant decrypt therefore
// requires TWO independent failures (bypass the app check AND mint a wrong-org
// child), which the token role makes impossible: the child can only ever carry
// one org's policy. The standing token is cryptographically incapable of
// decrypting any tenant directly.
type ScopedEnvelopeVaultResolver struct {
	parent       *vault.Client
	tokenRole    string
	policyPrefix string
	kvMount      string
	transitMount string
}

// NewScopedEnvelopeVaultResolver wires the per-org-scoped resolver over the
// standing svc/runtime Vault client (the token minter). tokenRole is the Vault
// token role that escapes the parent-subset check (default "runtime-tenant");
// policyPrefix is prepended to the org to form the per-org named policy
// (default "secret-tenant-"); kvMount / transitMount default to "secret" /
// "transit-tenants".
func NewScopedEnvelopeVaultResolver(parent *vault.Client, tokenRole, policyPrefix, kvMount, transitMount string) *ScopedEnvelopeVaultResolver {
	if tokenRole == "" {
		tokenRole = "runtime-tenant"
	}
	if policyPrefix == "" {
		policyPrefix = "secret-tenant-"
	}
	if kvMount == "" {
		kvMount = "secret"
	}
	if transitMount == "" {
		transitMount = "transit-tenants"
	}
	return &ScopedEnvelopeVaultResolver{
		parent:       parent,
		tokenRole:    tokenRole,
		policyPrefix: policyPrefix,
		kvMount:      kvMount,
		transitMount: transitMount,
	}
}

var _ Resolver = (*ScopedEnvelopeVaultResolver)(nil)

// Resolve authorizes (I28), mints a per-org child token, then runs the single
// KV-read + Transit decrypt under that scoped token. A mint/clone failure fails
// closed (no value). The child token is never cached; it self-expires via the
// token role's TTL / num_uses.
func (r *ScopedEnvelopeVaultResolver) Resolve(ctx context.Context, secretRef string, principal Principal) (SecretValue, error) {
	org, path, field, err := authorizeRef(ctx, secretRef, principal)
	if err != nil {
		return SecretValue{}, err
	}

	child, err := r.scopedClient(ctx, principal.OrgID)
	if err != nil {
		logger.FromContext(ctx).Warn("secret scope-token mint failed",
			"secret_ref", secretRef, "principal", principal.String(), "err", err)
		return SecretValue{}, fmt.Errorf("secret: scope %s: %w", secretRef, err)
	}

	kek, err := NewTenantTransit(child, TransitConfig{
		Mount:      r.transitMount,
		KeyPrefix:  "tenant-",
		AutoCreate: false,
	})
	if err != nil {
		return SecretValue{}, fmt.Errorf("secret: scope %s: %w", secretRef, err)
	}

	return unsealBlob(ctx, scopedKV{client: child, mount: r.kvMount}, kek, org, path, field, secretRef, principal)
}

// scopedClient mints a child token bound to the per-org named policy and
// returns a cloned Vault client bearing it. The token role enforces the TTL /
// num_uses / no-default-policy caps server-side regardless of what is requested
// here, and constrains the requested policy to allowed_policies_glob — so a
// resolver bug cannot widen the child beyond one org. The token has no default
// policy (cannot revoke-self); it is left to self-expire and is never cached.
func (r *ScopedEnvelopeVaultResolver) scopedClient(ctx context.Context, policyOrg string) (*vault.Client, error) {
	tok, err := r.parent.Auth().Token().CreateWithRoleWithContext(ctx, &vault.TokenCreateRequest{
		Policies: []string{r.policyPrefix + policyOrg},
	}, r.tokenRole)
	if err != nil {
		return nil, fmt.Errorf("mint scoped token: %w", err)
	}
	if tok == nil || tok.Auth == nil || tok.Auth.ClientToken == "" {
		return nil, errors.New("mint scoped token: vault returned no child token")
	}

	child, err := r.parent.Clone()
	if err != nil {
		return nil, fmt.Errorf("clone vault client: %w", err)
	}
	child.SetToken(tok.Auth.ClientToken)
	if ns := r.parent.Namespace(); ns != "" {
		child.SetNamespace(ns)
	}
	return child, nil
}

// HandedTokenEnvelopeResolver is the D-125 execution of D-089: it holds NO
// standing Vault capability and NEVER mints. Where ScopedEnvelopeVaultResolver
// mints a per-org child token on every Resolve (svc/runtime's standing
// mint-any-org capability), this resolver instead runs the KV-read +
// Transit-decrypt under a per-deployment token the CALLER hands in on
// principal.Token — a token minted once by the control plane (delivery),
// scoped to a single org, and handed to the fleet host alongside the
// descriptor. The fleet host is a bearer, never a minter: a stolen host
// credential can no longer mint a child for any org.
//
// It clones a base Vault client (address + TLS from the standard VAULT_* env,
// via vault.DefaultConfig), sets the handed token on the clone, and reuses the
// exact scopedKV + unsealBlob legs ScopedEnvelopeVaultResolver runs after its
// mint. authorizeRef (I28) stays as defense-in-depth: a bug that handed an
// A-org token for a B-org ref is refused by authorizeRef, and even if that were
// bypassed the A-scoped token hits decrypt/tenant-B and Vault returns 403.
type HandedTokenEnvelopeResolver struct {
	base         *vault.Client
	kvMount      string
	transitMount string
}

// NewHandedTokenEnvelopeResolver builds the handed-token resolver. It reads the
// Vault address + TLS from the standard VAULT_* env (vault.DefaultConfig) — the
// same env pkconfig.NewFromEnv reads — and holds NO token: the token arrives
// per-call on principal.Token. A DefaultConfig failure leaves base nil and
// every Resolve fails closed. kvMount / transitMount default to "secret" /
// "transit-tenants".
func NewHandedTokenEnvelopeResolver(kvMount, transitMount string) *HandedTokenEnvelopeResolver {
	if kvMount == "" {
		kvMount = "secret"
	}
	if transitMount == "" {
		transitMount = "transit-tenants"
	}
	// A nil/failed client is a valid state — Resolve fails closed. Never panic at
	// construction (mirrors the runtime's degrade-not-crash secret wiring).
	base, _ := vault.NewClient(vault.DefaultConfig())
	return &HandedTokenEnvelopeResolver{
		base:         base,
		kvMount:      kvMount,
		transitMount: transitMount,
	}
}

var _ Resolver = (*HandedTokenEnvelopeResolver)(nil)

// Resolve authorizes (I28), then runs the single KV-read + Transit decrypt under
// the caller-handed token (principal.Token). A missing token or unbuildable
// client fails closed (no value) — the resolver never falls back to any standing
// capability. The handed token is never logged (Principal.String redacts it).
func (r *HandedTokenEnvelopeResolver) Resolve(ctx context.Context, secretRef string, principal Principal) (SecretValue, error) {
	org, path, field, err := authorizeRef(ctx, secretRef, principal)
	if err != nil {
		return SecretValue{}, err
	}
	if r.base == nil {
		return SecretValue{}, fmt.Errorf("%w: %s", ErrVaultUnavailable, secretRef)
	}
	if principal.Token == "" {
		logger.FromContext(ctx).Warn("secret resolve denied: no handed token",
			"secret_ref", secretRef, "principal", principal.String())
		return SecretValue{}, fmt.Errorf("%w: %s", ErrNoHandedToken, secretRef)
	}

	client, err := r.base.Clone()
	if err != nil {
		return SecretValue{}, fmt.Errorf("secret: resolve %s: clone vault client: %w", secretRef, err)
	}
	client.SetToken(principal.Token)
	if ns := r.base.Namespace(); ns != "" {
		client.SetNamespace(ns)
	}

	kek, err := NewTenantTransit(client, TransitConfig{
		Mount:      r.transitMount,
		KeyPrefix:  "tenant-",
		AutoCreate: false,
	})
	if err != nil {
		return SecretValue{}, fmt.Errorf("secret: resolve %s: %w", secretRef, err)
	}

	return unsealBlob(ctx, scopedKV{client: client, mount: r.kvMount}, kek, org, path, field, secretRef, principal)
}

// scopedKV reads a single field from a KV v2 secret under a specific Vault
// client + mount. It exists so the scoped resolver can run the KV read under a
// per-org child token (the standing config.VaultClient is bound to the standing
// token). Its not-found message contains "not found" so isNotFound maps a miss
// to ErrSecretNotFound, matching the standing resolver's behavior.
type scopedKV struct {
	client *vault.Client
	mount  string
}

var _ vaultGetter = scopedKV{}

func (s scopedKV) GetSecret(ctx context.Context, path, key string) (string, error) {
	sec, err := s.client.KVv2(s.mount).Get(ctx, path)
	if err != nil {
		return "", fmt.Errorf("vault: read %s/%s: %w", s.mount, path, err)
	}
	if sec == nil || sec.Data == nil {
		return "", fmt.Errorf("vault: key %q not found at %s/%s", key, s.mount, path)
	}
	raw, ok := sec.Data[key]
	if !ok {
		return "", fmt.Errorf("vault: key %q not found at %s/%s", key, s.mount, path)
	}
	val, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("vault: key %q at %s/%s is not a string", key, s.mount, path)
	}
	return val, nil
}
