package config

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	vault "github.com/hashicorp/vault/api"
)

// TestVaultClientBuildsWithUnreadableCACertWhenSPIFFEVerifies is the load-bearing
// test: it reproduces the live failure (a VAULT_CACERT pointing at a file that is
// not there kills vault.NewClient outright) and proves the neutralizer fixes it —
// without needing a SPIRE agent, because it exercises the SDK mechanism directly
// rather than the svid branch of NewVaultClient (which requires a live Workload
// API socket).
func TestVaultClientBuildsWithUnreadableCACertWhenSPIFFEVerifies(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "trust-bundle.pem") // never created
	t.Setenv("VAULT_CACERT", missing)

	// Negative control FIRST: without neutralization the SDK fails exactly the way
	// the fleet host did in production.
	_, err := vault.NewClient(vault.DefaultConfig())
	if err == nil {
		t.Fatal("negative control: vault.NewClient() error = nil, want the CA-file load failure")
	}
	if !strings.Contains(err.Error(), "Error loading CA File") {
		t.Fatalf("negative control: error = %q, want it to contain %q", err, "Error loading CA File")
	}
	t.Logf("negative control (no neutralization) failed as expected: %v", err)

	// Positive: neutralize, then the same call succeeds.
	restore := neutralizeVaultCAEnv()
	if v, ok := os.LookupEnv("VAULT_CACERT"); ok {
		t.Fatalf("VAULT_CACERT still set to %q after neutralizeVaultCAEnv()", v)
	}
	client, err := vault.NewClient(vault.DefaultConfig())
	if err != nil {
		t.Fatalf("vault.NewClient() after neutralizeVaultCAEnv() error = %v, want nil", err)
	}
	if client == nil {
		t.Fatal("vault.NewClient() returned a nil client")
	}
	t.Log("positive: vault.NewClient() succeeded with an unreadable VAULT_CACERT neutralized")

	// restore puts the env back verbatim — and the failure comes back with it,
	// which pins that the neutralizer (and nothing else) is what made the
	// difference.
	restore()
	if got := os.Getenv("VAULT_CACERT"); got != missing {
		t.Fatalf("VAULT_CACERT after restore = %q, want %q", got, missing)
	}
	if _, err := vault.NewClient(vault.DefaultConfig()); err == nil {
		t.Fatal("after restore: vault.NewClient() error = nil, want the CA-file load failure back")
	}
}

// TestNeutralizeVaultCAEnvClearsEveryCASource covers the other two CA sources the
// SDK reads (a CAPATH that cannot be walked, an unparseable inline bundle); each
// also fails client construction, and each is superseded by SPIFFE.
func TestNeutralizeVaultCAEnvClearsEveryCASource(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{"ca file", "VAULT_CACERT", filepath.Join(t.TempDir(), "nope.pem")},
		{"ca path", "VAULT_CAPATH", filepath.Join(t.TempDir(), "nope.d")},
		{"ca bytes", "VAULT_CACERT_BYTES", "not-a-pem"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.env, tt.value)

			if _, err := vault.NewClient(vault.DefaultConfig()); err == nil {
				t.Fatalf("negative control: vault.NewClient() with %s error = nil, want non-nil", tt.env)
			}

			defer neutralizeVaultCAEnv()()
			if _, err := vault.NewClient(vault.DefaultConfig()); err != nil {
				t.Fatalf("vault.NewClient() after neutralizing %s error = %v, want nil", tt.env, err)
			}
		})
	}
}

// TestShouldNeutralizeCAEnv pins the guard: the env is only ever touched for svid
// mode over https, which is exactly when spiffe.VaultServerTLS supersedes any CA
// file. Every other mode and a plain-http address must be left alone.
func TestShouldNeutralizeCAEnv(t *testing.T) {
	tests := []struct {
		name string
		mode VaultAuthMode
		addr string
		want bool
	}{
		{"svid https", VaultAuthSVID, "https://vault:8200", true},
		{"svid https mixed case scheme", VaultAuthSVID, "HTTPS://vault:8200", true},
		{"svid http", VaultAuthSVID, "http://vault:8200", false},
		{"svid empty address", VaultAuthSVID, "", false},
		{"token https", VaultAuthToken, "https://vault:8200", false},
		{"approle https", VaultAuthAppRole, "https://vault:8200", false},
		{"kubernetes https", VaultAuthKubernetes, "https://vault:8200", false},
		{"empty mode https", VaultAuthMode(""), "https://vault:8200", false},
		{"unknown mode https", VaultAuthMode("bogus"), "https://vault:8200", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldNeutralizeCAEnv(tt.mode, tt.addr); got != tt.want {
				t.Fatalf("shouldNeutralizeCAEnv(%q, %q) = %v, want %v", tt.mode, tt.addr, got, tt.want)
			}
		})
	}
}

// TestNeutralizeVaultCAEnvIsIdempotent confirms a second call is a no-op and that
// restoring after either call yields the original values (the fleet host builds
// more than one Vault client per process).
func TestNeutralizeVaultCAEnvIsIdempotent(t *testing.T) {
	t.Setenv("VAULT_CACERT", "/etc/spire/trust-bundle.pem")

	first := neutralizeVaultCAEnv()
	second := neutralizeVaultCAEnv()

	if _, ok := os.LookupEnv("VAULT_CACERT"); ok {
		t.Fatal("VAULT_CACERT set after two neutralize calls")
	}

	// The second call captured nothing, so its restore must not resurrect anything.
	second()
	if _, ok := os.LookupEnv("VAULT_CACERT"); ok {
		t.Fatal("second (no-op) restore resurrected VAULT_CACERT")
	}

	first()
	if got := os.Getenv("VAULT_CACERT"); got != "/etc/spire/trust-bundle.pem" {
		t.Fatalf("VAULT_CACERT after first restore = %q, want %q", got, "/etc/spire/trust-bundle.pem")
	}
}

// TestNeutralizeVaultCAEnvLeavesUnrelatedVarsAlone guards the blast radius: the
// client mTLS identity, the verification switch and every other VAULT_* var must
// survive untouched. In particular VAULT_SKIP_VERIFY is never removed and never
// set — this path must not weaken verification.
func TestNeutralizeVaultCAEnvLeavesUnrelatedVarsAlone(t *testing.T) {
	untouched := map[string]string{
		"VAULT_CLIENT_CERT":     "/etc/vault/client.pem",
		"VAULT_CLIENT_KEY":      "/etc/vault/client-key.pem",
		"VAULT_SKIP_VERIFY":     "false",
		"VAULT_TLS_SERVER_NAME": "vault.sentiae.internal",
		"VAULT_ADDR":            "https://vault:8200",
		"VAULT_AUTH_MODE":       "svid",
		"VAULT_SVID_ROLE":       "fleet",
		"VAULT_NAMESPACE":       "ns",
		"VAULT_KV_MOUNT":        "secret",
	}
	for k, v := range untouched {
		t.Setenv(k, v)
	}
	t.Setenv("VAULT_CACERT", "/etc/spire/trust-bundle.pem")

	defer neutralizeVaultCAEnv()()

	for k, want := range untouched {
		if got, ok := os.LookupEnv(k); !ok || got != want {
			t.Fatalf("%s = %q (set=%v), want %q — neutralizeVaultCAEnv must not touch it", k, got, ok, want)
		}
	}
	if _, ok := os.LookupEnv("VAULT_CACERT"); ok {
		t.Fatal("VAULT_CACERT survived neutralizeVaultCAEnv()")
	}
}

// TestNewVaultClientLeavesCAEnvAloneOutsideSVIDHTTPS proves the non-svid paths are
// byte-identical to before: NewVaultClient may fail for its own reasons (no Vault
// reachable, missing credentials), but it must not have mutated the CA env on the
// way. Only the guarded svid+https path is allowed to.
func TestNewVaultClientLeavesCAEnvAloneOutsideSVIDHTTPS(t *testing.T) {
	caFile := writeTempCA(t)

	tests := []struct {
		name string
		cfg  VaultConfig
	}{
		{"token https", VaultConfig{Address: "https://vault:8200", AuthMode: VaultAuthToken, Token: "t"}},
		{"approle https", VaultConfig{Address: "https://vault:8200", AuthMode: VaultAuthAppRole}},
		{"kubernetes https", VaultConfig{Address: "https://vault:8200", AuthMode: VaultAuthKubernetes}},
		{"svid http", VaultConfig{Address: "http://vault:8200", AuthMode: VaultAuthSVID, SVIDRole: "fleet"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("VAULT_CACERT", caFile)

			// A bounded context keeps the svid case deterministic: with no Workload
			// API socket, spiffe.NewSource gives up at the deadline instead of
			// retrying.
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()

			// The result is irrelevant here (no Vault, and svid needs a SPIRE socket);
			// the env after the attempt is what is under test.
			if c, err := NewVaultClient(ctx, tt.cfg); err == nil && c != nil {
				_ = c.Close()
			}

			if got := os.Getenv("VAULT_CACERT"); got != caFile {
				t.Fatalf("VAULT_CACERT = %q after NewVaultClient(%s), want it untouched (%q)", got, tt.name, caFile)
			}
		})
	}
}

// writeTempCA generates a throwaway self-signed CA and writes it as PEM, so the
// SDK can actually load it — a test that used a bogus file would fail client
// construction for the very reason under test and prove nothing about the env
// being left alone. The certificate is generated per-test and trusted nowhere.
func writeTempCA(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "platform-kit-test-root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}

	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write temp CA: %v", err)
	}
	return path
}
