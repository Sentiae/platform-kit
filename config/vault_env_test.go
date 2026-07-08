package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvOrFile(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret_id")
	if err := os.WriteFile(secretPath, []byte("  file-secret\n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	missingPath := filepath.Join(dir, "does_not_exist")

	tests := []struct {
		name   string
		env    string // value for the direct var ("" = unset)
		file   string // value for the _FILE var ("" = unset)
		want   string
	}{
		{"direct env wins over file", "direct-value", secretPath, "direct-value"},
		{"file used when direct empty", "", secretPath, "file-secret"},
		{"file contents trimmed", "", secretPath, "file-secret"},
		{"missing file yields empty", "", missingPath, ""},
		{"both unset yields empty", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const name = "TEST_VAULT_ENVORFILE"
			if tt.env != "" {
				t.Setenv(name, tt.env)
			} else {
				t.Setenv(name, "")
			}
			if tt.file != "" {
				t.Setenv(name+"_FILE", tt.file)
			} else {
				t.Setenv(name+"_FILE", "")
			}
			if got := envOrFile(name); got != tt.want {
				t.Fatalf("envOrFile() = %q, want %q", got, tt.want)
			}
		})
	}
}
