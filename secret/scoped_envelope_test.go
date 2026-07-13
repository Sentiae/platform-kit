package secret

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	vault "github.com/hashicorp/vault/api"
)

// fakeVault is a minimal in-process Vault that exercises the D-085 Phase-1
// child-token mint path end-to-end: it records the mint request shape and
// asserts the KV-read + Transit-decrypt run under the minted CHILD token, not
// the parent. It speaks just enough of the KV-v2 + Transit + token-create HTTP
// contract for one Resolve.
type fakeVault struct {
	childToken string
	plaintext  string

	// captured from the token-create request
	mintPath     string
	mintToken    string   // X-Vault-Token presented on the mint call (parent)
	mintPolicies []string // policies requested for the child

	// captured from the KV + decrypt requests
	kvToken      string // X-Vault-Token on the KV read (want child)
	decryptToken string // X-Vault-Token on the decrypt (want child)
	decryptPath  string // e.g. /v1/transit-tenants/decrypt/tenant-<org>
}

func (f *fakeVault) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		tok := r.Header.Get("X-Vault-Token")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/auth/token/create/"):
			f.mintPath = r.URL.Path
			f.mintToken = tok
			var req struct {
				Policies []string `json:"policies"`
			}
			_ = json.Unmarshal(body, &req)
			f.mintPolicies = req.Policies
			writeJSON(w, map[string]any{
				"auth": map[string]any{
					"client_token":   f.childToken,
					"policies":       req.Policies,
					"lease_duration": 60,
					"renewable":      false,
				},
			})
		case strings.HasPrefix(r.URL.Path, "/v1/secret/data/"):
			f.kvToken = tok
			// KV v2 read shape: {"data":{"data":{...},"metadata":{...}}}
			writeJSON(w, map[string]any{
				"data": map[string]any{
					"data":     map[string]any{"db_password": "vault:v1:opaque-blob"},
					"metadata": map[string]any{"version": 1},
				},
			})
		case strings.Contains(r.URL.Path, "/decrypt/"):
			f.decryptToken = tok
			f.decryptPath = r.URL.Path
			writeJSON(w, map[string]any{
				"data": map[string]any{
					"plaintext": base64.StdEncoding.EncodeToString([]byte(f.plaintext)),
				},
			})
		default:
			t.Errorf("unexpected vault request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newParentClient(t *testing.T, url string) *vault.Client {
	t.Helper()
	cfg := vault.DefaultConfig()
	cfg.Address = url
	c, err := vault.NewClient(cfg)
	if err != nil {
		t.Fatalf("new vault client: %v", err)
	}
	c.SetToken("parent-svid-token")
	return c
}

func TestScopedEnvelopeVaultResolver_Resolve(t *testing.T) {
	fv := &fakeVault{childToken: "child-scoped-token", plaintext: "s3cr3t"}
	srv := httptest.NewServer(fv.handler(t))
	defer srv.Close()

	parent := newParentClient(t, srv.URL)
	r := NewScopedEnvelopeVaultResolver(parent, "runtime-tenant", "secret-tenant-", "secret", "transit-tenants")

	ref := TenantRef(orgA, "prod/app", "db_password")
	v, err := r.Resolve(context.Background(), ref, Principal{Service: "runtime-fleet", OrgID: orgA.String()})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if v.Reveal() != "s3cr3t" {
		t.Fatalf("Reveal = %q, want s3cr3t", v.Reveal())
	}

	// 1. The child token was minted via the runtime-tenant token ROLE, presented
	//    with the PARENT token, requesting exactly the one-org policy.
	if fv.mintPath != "/v1/auth/token/create/runtime-tenant" {
		t.Fatalf("mint path = %q, want /v1/auth/token/create/runtime-tenant", fv.mintPath)
	}
	if fv.mintToken != "parent-svid-token" {
		t.Fatalf("mint presented token %q, want parent-svid-token", fv.mintToken)
	}
	if len(fv.mintPolicies) != 1 || fv.mintPolicies[0] != "secret-tenant-"+orgA.String() {
		t.Fatalf("mint policies = %v, want [secret-tenant-%s]", fv.mintPolicies, orgA)
	}

	// 2. The KV read + decrypt ran under the CHILD token (never the parent) —
	//    the standing token has no standing decrypt/KV capability.
	if fv.kvToken != "child-scoped-token" {
		t.Fatalf("KV read used token %q, want child-scoped-token", fv.kvToken)
	}
	if fv.decryptToken != "child-scoped-token" {
		t.Fatalf("decrypt used token %q, want child-scoped-token", fv.decryptToken)
	}

	// 3. The decrypt keyName derives from the REF org (the cross-tenant barrier:
	//    a wrong-org child hitting the ref org's key 403s at the Vault layer).
	wantDecrypt := "/v1/transit-tenants/decrypt/tenant-" + orgA.String()
	if fv.decryptPath != wantDecrypt {
		t.Fatalf("decrypt path = %q, want %q", fv.decryptPath, wantDecrypt)
	}
}

// A cross-tenant caller is denied by authorizeRef (I28) BEFORE any Vault call —
// no token is minted, so the mint path is never hit (oracle-free).
func TestScopedEnvelopeVaultResolver_CrossTenantDeniedOracleFree(t *testing.T) {
	fv := &fakeVault{childToken: "child-scoped-token", plaintext: "s3cr3t"}
	srv := httptest.NewServer(fv.handler(t))
	defer srv.Close()

	parent := newParentClient(t, srv.URL)
	r := NewScopedEnvelopeVaultResolver(parent, "runtime-tenant", "secret-tenant-", "secret", "transit-tenants")

	ref := TenantRef(orgA, "prod/app", "db_password")
	_, err := r.Resolve(context.Background(), ref, Principal{Service: "runtime-fleet", OrgID: orgB.String()})
	if err == nil {
		t.Fatal("want cross-tenant denial, got nil")
	}
	if fv.mintPath != "" {
		t.Fatalf("token minted on a cross-tenant probe (%q) — I28 must deny before any Vault call", fv.mintPath)
	}
}
