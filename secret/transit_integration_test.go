//go:build integration

package secret

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	vault "github.com/hashicorp/vault/api"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/sentiae/platform-kit/config"
)

const devRootToken = "root-token"

// startDevVault brings up a Vault dev-mode container and returns a connected
// config.VaultClient (token auth, KV v2 mount "secret") plus the raw API
// client. The container is terminated via t.Cleanup.
func startDevVault(t *testing.T) (*config.VaultClient, *vault.Client) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	req := testcontainers.ContainerRequest{
		Image:        "hashicorp/vault:1.17",
		ExposedPorts: []string{"8200/tcp"},
		Env: map[string]string{
			"VAULT_DEV_ROOT_TOKEN_ID":  devRootToken,
			"VAULT_DEV_LISTEN_ADDRESS": "0.0.0.0:8200",
		},
		Cmd:        []string{"server", "-dev"},
		WaitingFor: wait.ForHTTP("/v1/sys/health").WithPort("8200/tcp").WithStatusCodeMatcher(func(s int) bool { return s == 200 }).WithStartupTimeout(60 * time.Second),
	}
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start vault container: %v", err)
	}
	t.Cleanup(func() {
		if err := ctr.Terminate(context.Background()); err != nil {
			t.Logf("terminate vault container: %v", err)
		}
	})

	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("vault host: %v", err)
	}
	port, err := ctr.MappedPort(ctx, "8200/tcp")
	if err != nil {
		t.Fatalf("vault port: %v", err)
	}
	addr := fmt.Sprintf("http://%s:%s", host, port.Port())

	vc, err := config.NewVaultClient(ctx, config.VaultConfig{
		Address:   addr,
		AuthMode:  config.VaultAuthToken,
		Token:     devRootToken,
		MountPath: "secret", // dev mode mounts KV v2 at "secret"
	})
	if err != nil {
		t.Fatalf("connect vault: %v", err)
	}
	t.Cleanup(func() { _ = vc.Close() })

	return vc, vc.Raw()
}

// TestTenantTransit_EnvelopeResolve proves the I29 per-tenant KEK substrate end
// to end against a real Vault: an org's secret sealed under its KEK round-trips
// for that org, is refused (oracle-free I28) for another org, cannot be
// decrypted by the wrong org's KEK, and is crypto-shredded when the KEK is
// deleted.
func TestTenantTransit_EnvelopeResolve(t *testing.T) {
	ctx := context.Background()
	vc, raw := startDevVault(t)

	// Enable the DISTINCT tenant transit mount (locked footprint).
	if err := raw.Sys().MountWithContext(ctx, "transit-tenants", &vault.MountInput{Type: "transit"}); err != nil {
		t.Fatalf("mount transit-tenants: %v", err)
	}

	// Sealing side: AutoCreate so per-org KEKs are provisioned on first use.
	sealer, err := NewTenantTransit(raw, TransitConfig{Mount: "transit-tenants", KeyPrefix: "tenant-", AutoCreate: true})
	if err != nil {
		t.Fatalf("new sealing transit: %v", err)
	}
	// Resolving side: decrypt-only, fail-closed.
	kek, err := NewTenantTransit(raw, TransitConfig{Mount: "transit-tenants", KeyPrefix: "tenant-", AutoCreate: false})
	if err != nil {
		t.Fatalf("new decrypt transit: %v", err)
	}

	// Seal orgA's secret under tenant-<A> and store the ciphertext in KV.
	const plaintext = "orgA-db-password"
	blobA, err := sealer.Encrypt(ctx, orgA, []byte(plaintext))
	if err != nil {
		t.Fatalf("seal orgA: %v", err)
	}
	refA := TenantRef(orgA, "app", "value")
	if err := vc.PutSecret(ctx, "tenants/"+orgA.String()+"/app", "value", blobA); err != nil {
		t.Fatalf("write KV: %v", err)
	}

	r := NewEnvelopeVaultResolver(vc, kek)

	// orgA principal resolves the plaintext.
	v, err := r.Resolve(ctx, refA, Principal{Service: "delivery", OrgID: orgA.String()})
	if err != nil {
		t.Fatalf("resolve as orgA: %v", err)
	}
	if v.Reveal() != plaintext {
		t.Fatalf("round-trip = %q, want %q", v.Reveal(), plaintext)
	}

	// orgB principal is refused at the I28 seam (cross-tenant), oracle-free.
	if _, err := r.Resolve(ctx, refA, Principal{Service: "delivery", OrgID: orgB.String()}); !errors.Is(err, ErrCrossTenantSecret) {
		t.Fatalf("resolve as orgB: want ErrCrossTenantSecret, got %v", err)
	}

	// Cryptographic barrier: orgB's KEK cannot open orgA's blob. Create
	// tenant-<B> by sealing something for B, then decrypt orgA's blob under B.
	if _, err := sealer.Encrypt(ctx, orgB, []byte("orgB-secret")); err != nil {
		t.Fatalf("seal orgB (create tenant-<B>): %v", err)
	}
	if _, err := kek.Decrypt(ctx, orgB, blobA); err == nil {
		t.Fatalf("orgB KEK decrypted orgA blob — cross-tenant crypto barrier breached")
	}

	// Seal a real orgB blob (tenant-<B> exists) so the post-shred decrypt
	// targets a valid-shaped ciphertext, not an empty one.
	blobB, err := sealer.Encrypt(ctx, orgB, []byte("orgB-secret-2"))
	if err != nil {
		t.Fatalf("seal orgB blob: %v", err)
	}
	// Crypto-shred: delete tenant-<B>; a subsequent Decrypt for B fails closed
	// because the key material is gone (transit/decrypt/tenant-<B> → error).
	if _, err := raw.Logical().DeleteWithContext(ctx, "transit-tenants/keys/tenant-"+orgB.String()); err != nil {
		t.Fatalf("crypto-shred tenant-<B>: %v", err)
	}
	if _, err := kek.Decrypt(ctx, orgB, blobB); err == nil {
		t.Fatalf("decrypt for orgB succeeded after crypto-shred — key material still live")
	}
}
