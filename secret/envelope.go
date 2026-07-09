package secret

import (
	"context"
	"fmt"

	"github.com/google/uuid"
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

	blob, err := r.kv.GetSecret(ctx, path, field)
	if err != nil {
		logger.FromContext(ctx).Warn("secret resolve failed",
			"secret_ref", secretRef, "principal", principal.String(), "err", err)
		if isNotFound(err) {
			return SecretValue{}, fmt.Errorf("%w: %s", ErrSecretNotFound, secretRef)
		}
		return SecretValue{}, fmt.Errorf("secret: resolve %s: %w", secretRef, err)
	}

	pt, err := r.kek.Decrypt(ctx, org, blob)
	if err != nil {
		logger.FromContext(ctx).Warn("secret unseal failed",
			"secret_ref", secretRef, "principal", principal.String(), "err", err)
		return SecretValue{}, fmt.Errorf("secret: unseal %s: %w", secretRef, err)
	}

	logger.FromContext(ctx).Info("secret resolved",
		"secret_ref", secretRef, "principal", principal.String())
	return SecretValue{value: string(pt)}, nil
}
