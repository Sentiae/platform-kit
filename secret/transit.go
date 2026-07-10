package secret

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	vault "github.com/hashicorp/vault/api"
)

// TransitConfig wires a TenantTransit to a Vault Transit mount. Connection and
// authentication are owned by the injected *vault.Client (typically obtained
// via config.VaultClient.Raw()); only the per-tenant key semantics live here.
//
// The locked I29 footprint for the per-tenant KEK substrate is a mount
// DISTINCT from the ops KMS mount, with crypto-shred enabled:
//
//	TransitConfig{
//	    Mount:      "transit-tenants", // separate from the ops "transit" mount
//	    KeyPrefix:  "tenant-",          // per-org KEK key name: "tenant-<org>"
//	    AutoCreate: true,               // seal path only; decrypt is fail-closed
//	}
//
// (Live Vault provisioning of transit-tenants + its policies is a deploy step;
// this type only encodes the expected config and speaks the Transit API.)
type TransitConfig struct {
	// Mount is the transit engine mount path, e.g. "transit-tenants".
	// Defaults to "transit-tenants".
	Mount string

	// KeyPrefix is prepended to every org key name so the key for an org is
	// "<KeyPrefix><org>" (e.g. "tenant-<org-uuid>"). Defaults to "tenant-".
	KeyPrefix string

	// AutoCreate controls whether a missing per-org KEK is created on the
	// ENCRYPT (seal) path. The DECRYPT path never creates a key — a decrypt
	// against a missing key fails closed. Set false for a decrypt-only
	// resolver (EnvelopeVaultResolver uses AutoCreate:false).
	AutoCreate bool
}

// TenantTransit is envelope encryption over a Vault Transit mount, keyed per
// tenant (org). Each org gets its own aes256-gcm96 KEK ("<KeyPrefix><org>")
// that never leaves Vault (exportable:false). Encrypt/Decrypt are Transit API
// calls returning/consuming Vault's versioned ciphertext string
// ("vault:v1:..."). Because the KEK is per-org, the WRONG org's key decrypts a
// blob to noise (Vault rejects it) — cross-tenant read is a cryptographic
// impossibility, not merely an authorization check.
//
// This generalizes the ops-service VaultTransit KMS: same Transit semantics,
// but parameterized (mount/prefix/auto-create) and free of any service's
// domain types, so it can live in platform-kit.
type TenantTransit struct {
	client     *vault.Client
	mount      string
	keyPrefix  string
	autoCreate bool

	// ensuredKeys caches which org keys we've created so the encrypt path
	// doesn't re-issue the create round trip per call.
	mu          sync.Mutex
	ensuredKeys map[string]struct{}
}

// keyType and the key-hardening flags are fixed for the tenant KEK substrate:
// modern AEAD, never exportable, no plaintext backup, and deletion_allowed so
// an org's key can be crypto-shredded (owner-locked decision).
const (
	tenantKeyType = "aes256-gcm96"
)

// NewTenantTransit builds a per-tenant Transit KEM over a raw Vault API client
// (config.VaultClient.Raw()). It creates no keys eagerly — a per-org KEK is
// provisioned lazily on first Encrypt when AutoCreate is set.
func NewTenantTransit(vaultAPI *vault.Client, cfg TransitConfig) (*TenantTransit, error) {
	if vaultAPI == nil {
		return nil, errors.New("secret: vault api client is required")
	}
	mount := cfg.Mount
	if mount == "" {
		mount = "transit-tenants"
	}
	keyPrefix := cfg.KeyPrefix
	if keyPrefix == "" {
		keyPrefix = "tenant-"
	}
	return &TenantTransit{
		client:      vaultAPI,
		mount:       mount,
		keyPrefix:   keyPrefix,
		autoCreate:  cfg.AutoCreate,
		ensuredKeys: make(map[string]struct{}),
	}, nil
}

// keyName is the only place the org→KEK-name mapping is defined.
func (t *TenantTransit) keyName(org uuid.UUID) string {
	return t.keyPrefix + org.String()
}

// ensureKey lazily creates the per-org KEK when AutoCreate is set. It is only
// ever called on the encrypt path; the decrypt path is fail-closed and never
// creates. POST transit/keys/:name is idempotent, so repeat calls are safe.
func (t *TenantTransit) ensureKey(ctx context.Context, org uuid.UUID) error {
	if !t.autoCreate {
		return nil
	}
	keyName := t.keyName(org)

	t.mu.Lock()
	_, cached := t.ensuredKeys[keyName]
	t.mu.Unlock()
	if cached {
		return nil
	}

	path := fmt.Sprintf("%s/keys/%s", t.mount, keyName)
	_, err := t.client.Logical().WriteWithContext(ctx, path, map[string]any{
		"type":                   tenantKeyType,
		"exportable":             false,
		"allow_plaintext_backup": false,
	})
	if err != nil {
		// A pre-existing key may surface as an error on some Vault versions;
		// treat that as success (create is idempotent).
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("secret: ensure tenant KEK %q: %w", keyName, err)
		}
	}

	// Enable crypto-shred (owner-locked D-069): deleting the org's KEK renders
	// every blob sealed under it permanently opaque. `deletion_allowed` is NOT a
	// key-create parameter (Vault ignores it there) — it must be set via the
	// key's /config endpoint. Idempotent, so safe to (re)apply each ensure.
	cfgPath := fmt.Sprintf("%s/keys/%s/config", t.mount, keyName)
	if _, cfgErr := t.client.Logical().WriteWithContext(ctx, cfgPath, map[string]any{
		"deletion_allowed": true,
	}); cfgErr != nil {
		return fmt.Errorf("secret: enable crypto-shred on tenant KEK %q: %w", keyName, cfgErr)
	}

	t.mu.Lock()
	t.ensuredKeys[keyName] = struct{}{}
	t.mu.Unlock()
	return nil
}

// Encrypt seals plaintext under the org's KEK and returns Vault's versioned
// ciphertext string ("vault:v1:..."). Plaintext is base64-encoded per the
// Transit API contract. When AutoCreate is set the org's KEK is provisioned on
// first use.
func (t *TenantTransit) Encrypt(ctx context.Context, org uuid.UUID, plaintext []byte) (string, error) {
	if err := t.ensureKey(ctx, org); err != nil {
		return "", err
	}
	path := fmt.Sprintf("%s/encrypt/%s", t.mount, t.keyName(org))
	resp, err := t.client.Logical().WriteWithContext(ctx, path, map[string]any{
		"plaintext": base64.StdEncoding.EncodeToString(plaintext),
	})
	if err != nil {
		return "", fmt.Errorf("secret: transit encrypt: %w", err)
	}
	if resp == nil || resp.Data == nil {
		return "", errors.New("secret: transit encrypt: empty response from vault")
	}
	ctRaw, ok := resp.Data["ciphertext"]
	if !ok {
		return "", errors.New("secret: transit encrypt: missing ciphertext in vault response")
	}
	ct, ok := ctRaw.(string)
	if !ok {
		return "", errors.New("secret: transit encrypt: ciphertext is not a string")
	}
	return ct, nil
}

// Decrypt opens a ciphertext under the org's KEK and returns the plaintext.
// It NEVER creates a key: a decrypt against a missing KEK fails closed (Vault
// returns an error, surfaced verbatim). The wrong org's KEK decrypts a foreign
// blob to a Vault error — the cross-tenant cryptographic barrier.
func (t *TenantTransit) Decrypt(ctx context.Context, org uuid.UUID, ciphertext string) ([]byte, error) {
	if ciphertext == "" {
		return nil, errors.New("secret: transit decrypt: empty ciphertext")
	}
	path := fmt.Sprintf("%s/decrypt/%s", t.mount, t.keyName(org))
	resp, err := t.client.Logical().WriteWithContext(ctx, path, map[string]any{
		"ciphertext": ciphertext,
	})
	if err != nil {
		return nil, fmt.Errorf("secret: transit decrypt: %w", err)
	}
	if resp == nil || resp.Data == nil {
		return nil, errors.New("secret: transit decrypt: empty response from vault")
	}
	ptRaw, ok := resp.Data["plaintext"]
	if !ok {
		return nil, errors.New("secret: transit decrypt: missing plaintext in vault response")
	}
	ptB64, ok := ptRaw.(string)
	if !ok {
		return nil, errors.New("secret: transit decrypt: plaintext is not a string")
	}
	pt, err := base64.StdEncoding.DecodeString(ptB64)
	if err != nil {
		return nil, fmt.Errorf("secret: transit decrypt: base64 decode: %w", err)
	}
	return pt, nil
}
