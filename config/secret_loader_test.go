package config

import (
	"context"
	"errors"
	"testing"
)

func TestEnvSecretLoader(t *testing.T) {
	t.Setenv("APP_DATABASE_PASSWORD", "hunter2")
	l := &EnvSecretLoader{Prefix: "APP"}
	v, err := l.Get(context.Background(), "database.password")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "hunter2" {
		t.Fatalf("got %q, want hunter2", v)
	}

	// Missing key returns empty, not error.
	v, err = l.Get(context.Background(), "nope")
	if err != nil || v != "" {
		t.Fatalf("missing key should be empty-no-error, got (%q, %v)", v, err)
	}
}

func TestEnvSecretLoader_NoPrefix(t *testing.T) {
	t.Setenv("DATABASE_PASSWORD", "hunter2")
	l := &EnvSecretLoader{}
	v, _ := l.Get(context.Background(), "database.password")
	if v != "hunter2" {
		t.Fatalf("got %q, want hunter2", v)
	}
}

func TestSplitVaultKey(t *testing.T) {
	cases := []struct {
		input       string
		defaultPath string
		wantPath    string
		wantField   string
	}{
		{"services/identity#db_password", "", "services/identity", "db_password"},
		{"db_password", "services/identity", "services/identity", "db_password"},
		{"stripe_key", "", "", "stripe_key"},
	}
	for _, tc := range cases {
		p, f := splitVaultKey(tc.input, tc.defaultPath)
		if p != tc.wantPath || f != tc.wantField {
			t.Errorf("splitVaultKey(%q, %q) = (%q, %q), want (%q, %q)",
				tc.input, tc.defaultPath, p, f, tc.wantPath, tc.wantField)
		}
	}
}

func TestChainSecretLoader(t *testing.T) {
	first := StaticSecretLoader{"db_password": ""}
	second := StaticSecretLoader{"db_password": "hunter2", "stripe_key": "sk_test"}
	chain := &ChainSecretLoader{Loaders: []SecretLoader{first, second}}

	v, err := chain.Get(context.Background(), "db_password")
	if err != nil || v != "hunter2" {
		t.Fatalf("chain fallback broken: got (%q, %v)", v, err)
	}

	v, err = chain.Get(context.Background(), "missing")
	if err != nil || v != "" {
		t.Fatalf("missing key should be empty-no-error, got (%q, %v)", v, err)
	}
}

// errLoader returns an error to verify chain keeps walking past failures.
type errLoader struct{}

func (errLoader) Get(_ context.Context, _ string) (string, error) {
	return "", errors.New("boom")
}

func TestChainSecretLoader_SkipsErroringLoader(t *testing.T) {
	chain := &ChainSecretLoader{Loaders: []SecretLoader{
		errLoader{},
		StaticSecretLoader{"k": "v"},
	}}
	v, err := chain.Get(context.Background(), "k")
	if err != nil || v != "v" {
		t.Fatalf("chain should have skipped erroring loader: got (%q, %v)", v, err)
	}
}

func TestStaticSecretLoader(t *testing.T) {
	s := StaticSecretLoader{"a": "1"}
	v, _ := s.Get(context.Background(), "a")
	if v != "1" {
		t.Fatalf("got %q, want 1", v)
	}
}

func TestNewDefaultSecretLoader_NoVault(t *testing.T) {
	t.Setenv("VAULT_ADDR", "")
	l := NewDefaultSecretLoader(context.Background(), "APP", "services/test")
	if _, ok := l.(*EnvSecretLoader); !ok {
		t.Fatalf("expected env-only loader when VAULT_ADDR unset, got %T", l)
	}
}
