package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testConfig struct {
	Port        string `mapstructure:"port"`
	Environment string `mapstructure:"environment"`
	Debug       bool   `mapstructure:"debug"`
}

func TestLoad_Defaults(t *testing.T) {
	var cfg testConfig
	err := Load(&cfg, Options{
		Defaults: map[string]any{
			"port":        "8080",
			"environment": "development",
			"debug":       false,
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want %q", cfg.Port, "8080")
	}
	if cfg.Environment != "development" {
		t.Errorf("Environment = %q, want %q", cfg.Environment, "development")
	}
	if cfg.Debug != false {
		t.Errorf("Debug = %v, want %v", cfg.Debug, false)
	}
}

func TestLoad_EnvVars(t *testing.T) {
	t.Setenv("TESTAPP_PORT", "9090")
	t.Setenv("TESTAPP_ENVIRONMENT", "production")
	t.Setenv("TESTAPP_DEBUG", "true")

	var cfg testConfig
	err := Load(&cfg, Options{
		EnvPrefix: "TESTAPP",
		Defaults: map[string]any{
			"port":        "8080",
			"environment": "development",
			"debug":       false,
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != "9090" {
		t.Errorf("Port = %q, want %q", cfg.Port, "9090")
	}
	if cfg.Environment != "production" {
		t.Errorf("Environment = %q, want %q", cfg.Environment, "production")
	}
	if cfg.Debug != true {
		t.Errorf("Debug = %v, want %v", cfg.Debug, true)
	}
}

func TestLoad_ConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(cfgFile, []byte("port: \"3000\"\nenvironment: staging\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	var cfg testConfig
	err = Load(&cfg, Options{
		ConfigFile: cfgFile,
		Defaults: map[string]any{
			"port":        "8080",
			"environment": "development",
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != "3000" {
		t.Errorf("Port = %q, want %q", cfg.Port, "3000")
	}
	if cfg.Environment != "staging" {
		t.Errorf("Environment = %q, want %q", cfg.Environment, "staging")
	}
}

func TestLoad_ConfigFile_NotFound(t *testing.T) {
	var cfg testConfig
	err := Load(&cfg, Options{
		ConfigFile: "/nonexistent/config.yaml",
	})
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestLoad_ConfigPaths(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(cfgFile, []byte("port: \"4000\"\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	var cfg testConfig
	err = Load(&cfg, Options{
		ConfigPaths: []string{dir},
		Defaults: map[string]any{
			"port": "8080",
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != "4000" {
		t.Errorf("Port = %q, want %q", cfg.Port, "4000")
	}
}

func TestLoad_EnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(cfgFile, []byte("port: \"3000\"\nenvironment: staging\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("MYAPP_PORT", "5555")

	var cfg testConfig
	err = Load(&cfg, Options{
		EnvPrefix:  "MYAPP",
		ConfigFile: cfgFile,
		Defaults: map[string]any{
			"port":        "8080",
			"environment": "development",
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// Env var should override file.
	if cfg.Port != "5555" {
		t.Errorf("Port = %q, want %q (env override)", cfg.Port, "5555")
	}
	// File value should still apply where no env var is set.
	if cfg.Environment != "staging" {
		t.Errorf("Environment = %q, want %q (from file)", cfg.Environment, "staging")
	}
}

type nestedConfig struct {
	Server struct {
		Host string `mapstructure:"host"`
		Port int    `mapstructure:"port"`
	} `mapstructure:"server"`
}

func TestLoad_NestedDefaults(t *testing.T) {
	var cfg nestedConfig
	err := Load(&cfg, Options{
		Defaults: map[string]any{
			"server.host": "localhost",
			"server.port": 8080,
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Host != "localhost" {
		t.Errorf("Server.Host = %q, want %q", cfg.Server.Host, "localhost")
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want %d", cfg.Server.Port, 8080)
	}
}

func TestLoad_ProfileOverride(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(base, []byte("port: \"3000\"\nenvironment: development\ndebug: false\n"), 0644); err != nil {
		t.Fatal(err)
	}
	prod := filepath.Join(dir, "config.prod.yaml")
	if err := os.WriteFile(prod, []byte("environment: production\ndebug: true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var cfg testConfig
	err := Load(&cfg, Options{
		ConfigFile: base,
		Profile:    "prod",
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// Base value untouched.
	if cfg.Port != "3000" {
		t.Errorf("Port = %q, want %q", cfg.Port, "3000")
	}
	// Overridden by profile.
	if cfg.Environment != "production" {
		t.Errorf("Environment = %q, want %q", cfg.Environment, "production")
	}
	if cfg.Debug != true {
		t.Errorf("Debug = %v, want %v (profile override)", cfg.Debug, true)
	}
}

func TestLoad_ProfileDefault_Dev(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(base, []byte("port: \"3000\"\nenvironment: base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dev := filepath.Join(dir, "config.dev.yaml")
	if err := os.WriteFile(dev, []byte("environment: development\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Ensure no profile env var leaks in.
	t.Setenv("APP_PROFILE", "")

	var cfg testConfig
	if err := Load(&cfg, Options{ConfigFile: base}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Environment != "development" {
		t.Errorf("Environment = %q, want %q (default profile = dev)", cfg.Environment, "development")
	}
}

func TestLoad_ProfileFromEnv(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(base, []byte("port: \"3000\"\nenvironment: base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(dir, "config.staging.yaml")
	if err := os.WriteFile(staging, []byte("environment: staging\n"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("APP_PROFILE", "staging")

	var cfg testConfig
	if err := Load(&cfg, Options{ConfigFile: base}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Environment != "staging" {
		t.Errorf("Environment = %q, want %q (APP_PROFILE=staging)", cfg.Environment, "staging")
	}
}

func TestLoad_ProfileMissingFile_NotAnError(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(base, []byte("port: \"3000\"\nenvironment: base\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// No config.canary.yaml exists — should silently fall back to base values.
	var cfg testConfig
	if err := Load(&cfg, Options{ConfigFile: base, Profile: "canary"}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Environment != "base" {
		t.Errorf("Environment = %q, want %q (no override file)", cfg.Environment, "base")
	}
}

func TestIsKnownProfile(t *testing.T) {
	cases := map[string]bool{
		"dev":     true,
		"staging": true,
		"prod":    true,
		"canary":  false,
		"":        false,
	}
	for p, want := range cases {
		if got := IsKnownProfile(p); got != want {
			t.Errorf("IsKnownProfile(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestMustLoad_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustLoad should panic on error")
		}
	}()

	var cfg testConfig
	MustLoad(&cfg, Options{
		ConfigFile: "/nonexistent/config.yaml",
	})
}

// --- validate-on-Load (§17: validate on Load, fail fast) ---

type validatedConfig struct {
	Port        string `mapstructure:"port" validate:"required,numeric"`
	Environment string `mapstructure:"environment" validate:"required,oneof=development staging production"`
	Optional    string `mapstructure:"optional"`
}

func TestLoad_Validation(t *testing.T) {
	tests := []struct {
		name       string
		defaults   map[string]any
		wantErr    bool
		wantFields []string
	}{
		{
			name:     "satisfied config passes",
			defaults: map[string]any{"port": "8080", "environment": "development"},
			wantErr:  false,
		},
		{
			name:       "missing required field names it",
			defaults:   map[string]any{"environment": "development"},
			wantErr:    true,
			wantFields: []string{"validatedConfig.Port"},
		},
		{
			name:       "value outside oneof names it",
			defaults:   map[string]any{"port": "8080", "environment": "bogus"},
			wantErr:    true,
			wantFields: []string{"validatedConfig.Environment"},
		},
		{
			name:       "every offending field is reported",
			defaults:   map[string]any{},
			wantErr:    true,
			wantFields: []string{"validatedConfig.Port", "validatedConfig.Environment"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg validatedConfig
			err := Load(&cfg, Options{Defaults: tt.defaults})

			if !tt.wantErr {
				if err != nil {
					t.Fatalf("Load() = %v, want nil", err)
				}
				return
			}

			if err == nil {
				t.Fatal("Load() = nil, want validation error")
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Load() = %v (%T), want *ValidationError", err, err)
			}
			if len(ve.Fields) != len(tt.wantFields) {
				t.Fatalf("Fields = %v, want %d entries", ve.Fields, len(tt.wantFields))
			}
			for _, want := range tt.wantFields {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name field %q", err.Error(), want)
				}
			}
		})
	}
}

// An untagged struct must be unaffected by the validator.
func TestLoad_UntaggedStructUnaffected(t *testing.T) {
	var cfg testConfig
	if err := Load(&cfg, Options{Defaults: map[string]any{}}); err != nil {
		t.Fatalf("Load() on untagged struct = %v, want nil", err)
	}
}

// A non-struct target cannot carry validate tags; Load must not reject it.
func TestLoad_NonStructTargetSkipsValidation(t *testing.T) {
	target := map[string]any{}
	if err := Load(&target, Options{Defaults: map[string]any{"port": "8080"}}); err != nil {
		t.Fatalf("Load() on map target = %v, want nil", err)
	}
}

// Load asserts the global mesh mTLS mode at boot (D-162a): a typo'd
// APP_GRPC_MTLS_MODE fails boot rather than silently degrading to "off".
func TestLoad_RejectsBadMTLSMode(t *testing.T) {
	t.Setenv("APP_GRPC_MTLS_MODE", "stric")
	var cfg testConfig
	err := Load(&cfg, Options{Defaults: map[string]any{"port": "8080"}})
	if err == nil {
		t.Fatal("Load() = nil, want error for a mistyped APP_GRPC_MTLS_MODE")
	}
}

func TestLoad_AcceptsValidMTLSMode(t *testing.T) {
	for _, mode := range []string{"", "off", "permissive", "strict"} {
		t.Run("mode="+mode, func(t *testing.T) {
			t.Setenv("APP_GRPC_MTLS_MODE", mode)
			var cfg testConfig
			if err := Load(&cfg, Options{Defaults: map[string]any{"port": "8080"}}); err != nil {
				t.Fatalf("Load() with APP_GRPC_MTLS_MODE=%q = %v, want nil", mode, err)
			}
		})
	}
}
