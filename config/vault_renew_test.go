package config

import (
	"context"
	"testing"

	vault "github.com/hashicorp/vault/api"
)

// TestTokenModeNoRenewer verifies token-mode builds a client with no lease and
// no renewer, and that Close is a safe no-op on the renewer.
func TestTokenModeNoRenewer(t *testing.T) {
	c, err := NewVaultClient(context.Background(), VaultConfig{
		Address:   "http://127.0.0.1:8200",
		AuthMode:  VaultAuthToken,
		Token:     "dev-token",
		AutoRenew: true, // even if requested, token mode has no lease to renew
	})
	if err != nil {
		t.Fatalf("NewVaultClient: %v", err)
	}
	if c.loginSecret != nil {
		t.Fatalf("token mode should have nil loginSecret, got %+v", c.loginSecret)
	}
	if c.renewCancel != nil {
		t.Fatalf("token mode should not start a renewer (renewCancel non-nil)")
	}
	// Close must be a no-op and idempotent.
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestStartRenewerNonRenewableLease verifies the renewer guard: a non-renewable
// lease starts no renewer and does not panic.
func TestStartRenewerNonRenewableLease(t *testing.T) {
	c := &VaultClient{
		client:    mustClient(t),
		mountPath: "secret",
		cfg:       VaultConfig{AuthMode: VaultAuthAppRole},
		loginSecret: &vault.Secret{Auth: &vault.SecretAuth{
			ClientToken: "t",
			Renewable:   false,
		}},
	}
	c.startRenewer(context.Background()) // must be a no-op
	if c.renewCancel != nil {
		t.Fatalf("non-renewable lease must not start a renewer")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestConsumeRenewalsCancel verifies the renewal-consuming loop exits cleanly
// (no panic) when its context is cancelled — the Close path.
func TestConsumeRenewalsCancel(t *testing.T) {
	c := &VaultClient{client: mustClient(t)}
	w, err := c.client.NewLifetimeWatcher(&vault.LifetimeWatcherInput{
		Secret: &vault.Secret{Auth: &vault.SecretAuth{ClientToken: "t", Renewable: true, LeaseDuration: 3600}},
	})
	if err != nil {
		t.Fatalf("NewLifetimeWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if reauth := c.consumeRenewals(ctx, w); reauth {
		t.Fatalf("cancelled context should return false (no re-login)")
	}
	w.Stop()
}

func mustClient(t *testing.T) *vault.Client {
	t.Helper()
	cfg := vault.DefaultConfig()
	cfg.Address = "http://127.0.0.1:8200"
	cl, err := vault.NewClient(cfg)
	if err != nil {
		t.Fatalf("vault.NewClient: %v", err)
	}
	return cl
}
