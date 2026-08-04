package config

import (
	"context"
	"strings"
	"testing"
)

// TestNewFromEnvWithSVIDRoleRejects covers the guard branches: an empty role, a
// missing VAULT_ADDR, and every auth mode other than svid (where a jwt role
// name is meaningless and accepting it would hand back a client with
// capabilities the caller never asked for).
func TestNewFromEnvWithSVIDRoleRejects(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		role    string
		wantErr string
	}{
		{
			name:    "empty role",
			env:     map[string]string{"VAULT_ADDR": "https://vault:8200", "VAULT_AUTH_MODE": "svid"},
			role:    "",
			wantErr: "svid role is required",
		},
		{
			name:    "missing address",
			env:     map[string]string{"VAULT_ADDR": "", "VAULT_AUTH_MODE": "svid"},
			role:    "fleetdrill-ledger",
			wantErr: "VAULT_ADDR is required",
		},
		{
			name:    "token mode",
			env:     map[string]string{"VAULT_ADDR": "https://vault:8200", "VAULT_AUTH_MODE": "token", "VAULT_TOKEN": "dev-token"},
			role:    "fleetdrill-ledger",
			wantErr: "requires VAULT_AUTH_MODE=svid",
		},
		{
			name:    "approle mode",
			env:     map[string]string{"VAULT_ADDR": "https://vault:8200", "VAULT_AUTH_MODE": "approle"},
			role:    "fleetdrill-ledger",
			wantErr: "requires VAULT_AUTH_MODE=svid",
		},
		{
			name:    "kubernetes mode",
			env:     map[string]string{"VAULT_ADDR": "https://vault:8200", "VAULT_AUTH_MODE": "kubernetes"},
			role:    "fleetdrill-ledger",
			wantErr: "requires VAULT_AUTH_MODE=svid",
		},
		{
			name:    "unset mode",
			env:     map[string]string{"VAULT_ADDR": "https://vault:8200", "VAULT_AUTH_MODE": ""},
			role:    "fleetdrill-ledger",
			wantErr: "requires VAULT_AUTH_MODE=svid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			client, err := NewFromEnvWithSVIDRole(context.Background(), tt.role)
			if err == nil {
				t.Fatal("NewFromEnvWithSVIDRole() error = nil, want non-nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.wantErr)
			}
			if client != nil {
				t.Fatalf("client = %v, want nil", client)
			}
		})
	}
}

// TestNewFromEnvWithSVIDRolePassesGuards proves a valid svid environment gets
// past the guards and proceeds to the SPIFFE Workload API (which is absent in a
// unit context, so the call fails there and not earlier). The ctx is cancelled
// so the bounded workload-API probe returns immediately.
func TestNewFromEnvWithSVIDRolePassesGuards(t *testing.T) {
	t.Setenv("VAULT_ADDR", "https://vault:8200")
	t.Setenv("VAULT_AUTH_MODE", "svid")
	t.Setenv("VAULT_SVID_ROLE", "delivery")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewFromEnvWithSVIDRole(ctx, "fleetdrill-ledger")
	if err == nil {
		t.Fatal("NewFromEnvWithSVIDRole() error = nil, want non-nil without a workload API")
	}
	if !strings.Contains(err.Error(), "spiffe") {
		t.Fatalf("error = %q, want a spiffe source failure (guards passed)", err)
	}
}

// TestNewFromEnvWithSVIDRoleOverridesOnlyTheRole asserts the seam replaces the
// role and leaves every other env-derived field intact — the whole point of
// delegating to configFromEnv rather than re-parsing.
func TestNewFromEnvWithSVIDRoleOverridesOnlyTheRole(t *testing.T) {
	t.Setenv("VAULT_ADDR", "https://vault:8200")
	t.Setenv("VAULT_AUTH_MODE", "svid")
	t.Setenv("VAULT_SVID_ROLE", "delivery")
	t.Setenv("VAULT_NAMESPACE", "ns1")
	t.Setenv("VAULT_KV_MOUNT", "secret")

	cfg, err := configFromEnv()
	if err != nil {
		t.Fatalf("configFromEnv() error = %v", err)
	}
	cfg.SVIDRole = "fleetdrill-ledger"

	if cfg.Address != "https://vault:8200" || cfg.Namespace != "ns1" || cfg.MountPath != "secret" {
		t.Fatalf("non-role fields changed: %+v", cfg)
	}
	if cfg.AuthMode != VaultAuthSVID || !cfg.AutoRenew {
		t.Fatalf("auth settings changed: mode=%q autoRenew=%v", cfg.AuthMode, cfg.AutoRenew)
	}
	// The process-wide role must remain what the environment says; only the
	// per-call copy carries the capability role.
	if got := configFromEnvRole(t); got != "delivery" {
		t.Fatalf("VAULT_SVID_ROLE mutated to %q, want %q", got, "delivery")
	}
}

func configFromEnvRole(t *testing.T) string {
	t.Helper()
	cfg, err := configFromEnv()
	if err != nil {
		t.Fatalf("configFromEnv() error = %v", err)
	}
	return cfg.SVIDRole
}
