//go:build integration

package secret

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sentiae/platform-kit/config"
)

// Resolves a REAL secret_ref from a REAL Vault (the deployed server), proving
// the P14 provider end to end. Skips unless VAULT_ADDR/VAULT_TOKEN/SECRET_REF
// are set. Run:
//
//	VAULT_ADDR=http://10.0.10.20:8200 VAULT_TOKEN=dev-root-token \
//	SECRET_REF=foundry/llm#openrouter_api_key \
//	go test -tags integration ./secret/ -run RealVault -v
func TestVaultResolver_RealVault(t *testing.T) {
	addr, token, ref := os.Getenv("VAULT_ADDR"), os.Getenv("VAULT_TOKEN"), os.Getenv("SECRET_REF")
	if addr == "" || token == "" || ref == "" {
		t.Skip("set VAULT_ADDR, VAULT_TOKEN, SECRET_REF to run the real-Vault check")
	}
	mount := os.Getenv("VAULT_MOUNT")
	if mount == "" {
		mount = "secret"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	vc, err := config.NewVaultClient(ctx, config.VaultConfig{
		Address: addr, AuthMode: config.VaultAuthToken, Token: token, MountPath: mount,
	})
	if err != nil {
		t.Fatalf("connect vault: %v", err)
	}

	r := NewVaultResolver(vc)
	v, err := r.Resolve(ctx, ref, Principal{Service: "cp0-verify", Subject: "integration-test"})
	if err != nil {
		t.Fatalf("resolve %q: %v", ref, err)
	}
	if v.Reveal() == "" {
		t.Fatalf("resolved an EMPTY value for %q — expected a real secret", ref)
	}
	// Prove redaction holds even after a real resolve; never log the value.
	if v.String() != "[REDACTED]" {
		t.Fatalf("SecretValue.String() leaked: %q", v.String())
	}
	t.Logf("resolved %q from real Vault: %d bytes, String()=%q (redacted)", ref, len(v.Reveal()), v.String())
}
