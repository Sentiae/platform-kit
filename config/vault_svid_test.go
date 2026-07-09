package config

import (
	"context"
	"testing"

	vault "github.com/hashicorp/vault/api"
)

// TestConfigFromEnvSVID verifies that the svid mode is selected and its config
// fields are read from the environment, with AutoRenew defaulting to true.
func TestConfigFromEnvSVID(t *testing.T) {
	t.Setenv("VAULT_ADDR", "https://vault:8200")
	t.Setenv("VAULT_AUTH_MODE", "svid")
	t.Setenv("VAULT_SVID_ROLE", "foundry")

	cfg, err := configFromEnv()
	if err != nil {
		t.Fatalf("configFromEnv() error = %v", err)
	}
	if cfg.AuthMode != VaultAuthSVID {
		t.Fatalf("AuthMode = %q, want %q", cfg.AuthMode, VaultAuthSVID)
	}
	if cfg.SVIDRole != "foundry" {
		t.Fatalf("SVIDRole = %q, want %q", cfg.SVIDRole, "foundry")
	}
	if cfg.Address != "https://vault:8200" {
		t.Fatalf("Address = %q, want %q", cfg.Address, "https://vault:8200")
	}
	if !cfg.AutoRenew {
		t.Fatal("AutoRenew = false, want true for svid mode")
	}
}

// TestConfigFromEnvSVIDAutoRenewOverride confirms VAULT_AUTO_RENEW still
// overrides the svid default.
func TestConfigFromEnvSVIDAutoRenewOverride(t *testing.T) {
	t.Setenv("VAULT_ADDR", "https://vault:8200")
	t.Setenv("VAULT_AUTH_MODE", "svid")
	t.Setenv("VAULT_SVID_ROLE", "foundry")
	t.Setenv("VAULT_AUTO_RENEW", "false")

	cfg, err := configFromEnv()
	if err != nil {
		t.Fatalf("configFromEnv() error = %v", err)
	}
	if cfg.AutoRenew {
		t.Fatal("AutoRenew = true, want false after VAULT_AUTO_RENEW=false override")
	}
}

// TestAuthenticateSVIDGuards verifies the svid branch fails cleanly (no panic)
// when required inputs are missing — notably a nil jwt source, which is the
// state in a unit context with no live SPIRE socket.
func TestAuthenticateSVIDGuards(t *testing.T) {
	client, err := vault.NewClient(vault.DefaultConfig())
	if err != nil {
		t.Fatalf("vault.NewClient: %v", err)
	}

	tests := []struct {
		name string
		cfg  VaultConfig
	}{
		{"missing role", VaultConfig{AuthMode: VaultAuthSVID}},
		{"nil jwt source", VaultConfig{AuthMode: VaultAuthSVID, SVIDRole: "foundry"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret, err := authenticate(context.Background(), client, tt.cfg)
			if err == nil {
				t.Fatal("authenticate() error = nil, want non-nil")
			}
			if secret != nil {
				t.Fatalf("authenticate() secret = %v, want nil", secret)
			}
		})
	}
}

// TestAuthenticateBackwardCompatModes confirms the existing modes still guard
// their required inputs identically (svid is a new switch case that does not
// alter them).
func TestAuthenticateBackwardCompatModes(t *testing.T) {
	client, err := vault.NewClient(vault.DefaultConfig())
	if err != nil {
		t.Fatalf("vault.NewClient: %v", err)
	}

	tests := []struct {
		name string
		cfg  VaultConfig
	}{
		{"token requires token", VaultConfig{AuthMode: VaultAuthToken}},
		{"approle requires ids", VaultConfig{AuthMode: VaultAuthAppRole}},
		{"kubernetes requires role", VaultConfig{AuthMode: VaultAuthKubernetes}},
		{"unknown mode", VaultConfig{AuthMode: VaultAuthMode("bogus")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := authenticate(context.Background(), client, tt.cfg); err == nil {
				t.Fatal("authenticate() error = nil, want non-nil")
			}
		})
	}

	// token mode with a token succeeds and sets it (no network).
	c2, _ := vault.NewClient(vault.DefaultConfig())
	secret, err := authenticate(context.Background(), c2, VaultConfig{AuthMode: VaultAuthToken, Token: "dev-token"})
	if err != nil {
		t.Fatalf("token authenticate() error = %v", err)
	}
	if secret != nil {
		t.Fatalf("token authenticate() secret = %v, want nil (no lease)", secret)
	}
	if c2.Token() != "dev-token" {
		t.Fatalf("token = %q, want %q", c2.Token(), "dev-token")
	}
}
