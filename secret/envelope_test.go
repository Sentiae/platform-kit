package secret

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	vault "github.com/hashicorp/vault/api"
)

// fakeKEK records whether Decrypt was invoked so tests can assert the
// oracle-free property (a denied caller must not reach the KEK).
type fakeKEK struct {
	pt    []byte
	err   error
	calls int
}

func (f *fakeKEK) Decrypt(_ context.Context, _ uuid.UUID, _ string) ([]byte, error) {
	f.calls++
	return f.pt, f.err
}

func TestAuthorizeRef(t *testing.T) {
	refA := TenantRef(orgA, "prod/app", "db_password")

	tests := []struct {
		name      string
		ref       string
		principal Principal
		wantErr   error
		wantOrg   uuid.UUID
		wantPath  string
		wantField string
	}{
		{
			name:      "valid same-org",
			ref:       refA,
			principal: Principal{Service: "delivery", OrgID: orgA.String()},
			wantOrg:   orgA,
			wantPath:  "tenants/c883c1d0-249a-4262-bf9c-f4c30f0850b6/prod/app",
			wantField: "db_password",
		},
		{
			name:      "unscoped ref",
			ref:       "sentiae/prod/app#db_password",
			principal: Principal{Service: "delivery", OrgID: orgA.String()},
			wantErr:   ErrUnscopedSecretRef,
		},
		{
			name:      "cross-org denied",
			ref:       refA,
			principal: Principal{Service: "delivery", OrgID: orgB.String()},
			wantErr:   ErrCrossTenantSecret,
		},
		{
			name:      "empty principal org",
			ref:       refA,
			principal: Principal{Service: "delivery"},
			wantErr:   ErrCrossTenantSecret,
		},
		{
			name:      "unparseable principal org",
			ref:       refA,
			principal: Principal{Service: "delivery", OrgID: "not-a-uuid"},
			wantErr:   ErrCrossTenantSecret,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			org, path, field, err := authorizeRef(context.Background(), tt.ref, tt.principal)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if org != tt.wantOrg {
				t.Fatalf("org = %v, want %v", org, tt.wantOrg)
			}
			if path != tt.wantPath || field != tt.wantField {
				t.Fatalf("path/field = %q/%q, want %q/%q", path, field, tt.wantPath, tt.wantField)
			}
		})
	}
}

func TestEnvelopeVaultResolver_Resolve(t *testing.T) {
	refA := TenantRef(orgA, "prod/app", "db_password")

	t.Run("same-org unseals blob to plaintext", func(t *testing.T) {
		kv := &fakeGetter{val: "vault:v1:opaque-blob"}
		kek := &fakeKEK{pt: []byte("s3cr3t")}
		r := NewEnvelopeVaultResolver(kv, kek)

		v, err := r.Resolve(context.Background(), refA, Principal{Service: "delivery", OrgID: orgA.String()})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if v.Reveal() != "s3cr3t" {
			t.Fatalf("Reveal = %q", v.Reveal())
		}
		if kv.gotPath != "tenants/c883c1d0-249a-4262-bf9c-f4c30f0850b6/prod/app" || kv.gotKey != "db_password" {
			t.Fatalf("kv got path=%q key=%q", kv.gotPath, kv.gotKey)
		}
		if kv.calls != 1 || kek.calls != 1 {
			t.Fatalf("calls kv=%d kek=%d, want 1/1", kv.calls, kek.calls)
		}
	})

	// I28 oracle-free: a cross-tenant caller must reach NEITHER kv NOR kek.
	t.Run("cross-org denied oracle-free (neither kv nor kek called)", func(t *testing.T) {
		kv := &fakeGetter{val: "vault:v1:opaque-blob"}
		kek := &fakeKEK{pt: []byte("s3cr3t")}
		r := NewEnvelopeVaultResolver(kv, kek)

		_, err := r.Resolve(context.Background(), refA, Principal{Service: "delivery", OrgID: orgB.String()})
		if !errors.Is(err, ErrCrossTenantSecret) {
			t.Fatalf("want ErrCrossTenantSecret, got %v", err)
		}
		if kv.calls != 0 {
			t.Fatalf("kv called %d times on cross-tenant probe — existence oracle leak", kv.calls)
		}
		if kek.calls != 0 {
			t.Fatalf("kek called %d times on cross-tenant probe — existence oracle leak", kek.calls)
		}
	})

	t.Run("unscoped ref rejected, neither kv nor kek called", func(t *testing.T) {
		kv := &fakeGetter{val: "blob"}
		kek := &fakeKEK{pt: []byte("s3cr3t")}
		r := NewEnvelopeVaultResolver(kv, kek)

		_, err := r.Resolve(context.Background(), "sentiae/prod/app#db_password", Principal{Service: "delivery", OrgID: orgA.String()})
		if !errors.Is(err, ErrUnscopedSecretRef) {
			t.Fatalf("want ErrUnscopedSecretRef, got %v", err)
		}
		if kv.calls != 0 || kek.calls != 0 {
			t.Fatalf("calls kv=%d kek=%d, want 0/0", kv.calls, kek.calls)
		}
	})

	t.Run("kv not-found maps to ErrSecretNotFound, kek not called", func(t *testing.T) {
		kv := &fakeGetter{err: errors.New("secret not found at path")}
		kek := &fakeKEK{pt: []byte("s3cr3t")}
		r := NewEnvelopeVaultResolver(kv, kek)

		_, err := r.Resolve(context.Background(), refA, Principal{Service: "delivery", OrgID: orgA.String()})
		if !errors.Is(err, ErrSecretNotFound) {
			t.Fatalf("want ErrSecretNotFound, got %v", err)
		}
		if kek.calls != 0 {
			t.Fatalf("kek called %d times after kv miss", kek.calls)
		}
	})

	t.Run("kek decrypt error surfaces, no value", func(t *testing.T) {
		kv := &fakeGetter{val: "vault:v1:foreign-blob"}
		kek := &fakeKEK{err: errors.New("cipher: message authentication failed")}
		r := NewEnvelopeVaultResolver(kv, kek)

		_, err := r.Resolve(context.Background(), refA, Principal{Service: "delivery", OrgID: orgA.String()})
		if err == nil || errors.Is(err, ErrSecretNotFound) {
			t.Fatalf("want a wrapped unseal error, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// HandedTokenEnvelopeResolver: CA rotation + capability containment
// ---------------------------------------------------------------------------

// testCA is a throwaway self-signed CA used to prove the resolver follows a
// rotating trust anchor without any SPIRE involvement.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T, cn string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ca key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create ca cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse ca cert: %v", err)
	}
	return &testCA{cert: cert, key: key}
}

// issue signs a server leaf for dnsName under this CA.
func (ca *testCA) issue(t *testing.T, dnsName string) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: dnsName},
		DNSNames:              []string{dnsName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func poolOf(cas ...*testCA) *x509.CertPool {
	p := x509.NewCertPool()
	for _, ca := range cas {
		p.AddCert(ca.cert)
	}
	return p
}

// servedCert is the server's rotatable identity: GetCertificate reads it per
// handshake, so swapping it mid-test is exactly a CA rotation on Vault's side.
type servedCert struct {
	mu   sync.Mutex
	cert *tls.Certificate
}

func (s *servedCert) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cert, nil
}

func (s *servedCert) rotate(c *tls.Certificate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cert = c
}

// livePool is a mutable, mutex-guarded root pool read on EVERY handshake — the
// unit-test stand-in for spiffe.VaultServerTLS's verification closures reading
// the live *workloadapi.X509Source.
type livePool struct {
	mu   sync.Mutex
	pool *x509.CertPool
}

func (p *livePool) set(pool *x509.CertPool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pool = pool
}

func (p *livePool) verify(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return errors.New("no peer certificate")
	}
	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return err
	}
	inter := x509.NewCertPool()
	for _, raw := range rawCerts[1:] {
		c, err := x509.ParseCertificate(raw)
		if err != nil {
			return err
		}
		inter.AddCert(c)
	}
	p.mu.Lock()
	roots := p.pool
	p.mu.Unlock()
	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inter,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	return err
}

// newParentWithTLS builds a base Vault client whose transport carries tlsCfg —
// the shape config.NewVaultClient produces (it overwrites the DefaultConfig
// transport's TLSClientConfig). The transport is returned so the test can force
// a fresh handshake after rotation.
func newParentWithTLS(t *testing.T, addr string, tlsCfg *tls.Config) (*vault.Client, *http.Transport) {
	t.Helper()
	cfg := vault.DefaultConfig()
	cfg.Address = addr
	cfg.MaxRetries = 0 // a TLS failure must surface, not be retried for seconds
	tr, ok := cfg.HttpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type %T", cfg.HttpClient.Transport)
	}
	tr.TLSClientConfig = tlsCfg
	c, err := vault.NewClient(cfg)
	if err != nil {
		t.Fatalf("new vault client: %v", err)
	}
	return c, tr
}

func isUnknownAuthority(err error) bool {
	var ua x509.UnknownAuthorityError
	if errors.As(err, &ua) {
		return true
	}
	// The TLS stack surfaces this through url.Error / retryablehttp wrapping,
	// which does not always preserve the typed error; the message is the exact
	// string the production outage logged.
	return strings.Contains(err.Error(), "certificate signed by unknown authority")
}

// TestHandedTokenResolverInheritsTransport pins the mechanism the CA-rotation
// fix rests on: the resolver does NOT rebuild an http client from
// vault.DefaultConfig, it carries the handed-in client's transport, and every
// per-Resolve Clone keeps sharing that same *http.Transport pointer (so whatever
// live TLS verification the caller wired stays in force).
func TestHandedTokenResolverInheritsTransport(t *testing.T) {
	marker := &http.Transport{}
	cfg := vault.DefaultConfig()
	cfg.Address = "https://vault.invalid:8200"
	cfg.HttpClient = &http.Client{Transport: marker}
	base, err := vault.NewClient(cfg)
	if err != nil {
		t.Fatalf("new vault client: %v", err)
	}

	r := NewHandedTokenEnvelopeResolverWithClient(base, "", "")
	if r.base != base {
		t.Fatal("resolver did not adopt the handed-in base client")
	}

	// CloneConfig copies the *http.Client STRUCT (so that pointer differs by
	// design) but copies the Transport field — which is what holds TLSClientConfig
	// — so transport identity is the property that matters.
	if got := r.base.CloneConfig().HttpClient.Transport; got != http.RoundTripper(marker) {
		t.Fatalf("resolver base transport = %p, want the handed-in marker %p", got, marker)
	}

	clone, err := r.base.Clone()
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if got := clone.CloneConfig().HttpClient.Transport; got != http.RoundTripper(marker) {
		t.Fatalf("per-Resolve clone rebuilt its transport (%p), want the inherited marker %p", got, marker)
	}
	if clone.Token() != "" {
		t.Fatalf("clone carried a token (%q) — CloneToken must stay false", clone.Token())
	}
}

// TestHandedTokenResolverFollowsCARotation is the outage, reproduced and fixed
// in a unit test — no SPIRE required.
//
// Two base clients talk to the SAME Vault:
//
//	A — RootCAs pinned to a static pool built once (what VAULT_CACERT +
//	    vault.DefaultConfig gives you: a snapshot frozen at construction).
//	B — VerifyPeerCertificate reading a mutable pool on every handshake (what
//	    spiffe.VaultServerTLS gives you: a LIVE source).
//
// Both resolve fine while the server still serves CA-1. After the CA rotates
// (server swaps to a CA-2 leaf, the live pool learns CA-2), A must fail with
// "certificate signed by unknown authority" — every secret resolve dead until
// the process restarts, which is precisely why no Postgres data-VM could boot —
// while B keeps working with no restart and no static trust-bundle file.
func TestHandedTokenResolverFollowsCARotation(t *testing.T) {
	ca1 := newTestCA(t, "sentiae-test-ca-1")
	ca2 := newTestCA(t, "sentiae-test-ca-2")

	served := &servedCert{cert: ca1.issue(t, "localhost")}
	fv := &fakeVault{plaintext: "s3cr3t"}
	srv := httptest.NewUnstartedServer(fv.handler(t))
	srv.TLS = &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: served.get,
	}
	srv.StartTLS()
	defer srv.Close()

	// ServerName is set explicitly so the client sends SNI (the address is an IP);
	// without SNI crypto/tls would skip GetCertificate and the server could not
	// rotate.
	staticParent, staticTr := newParentWithTLS(t, srv.URL, &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: "localhost",
		RootCAs:    poolOf(ca1), // snapshot: built once, never revisited
	})

	live := &livePool{pool: poolOf(ca1)}
	liveParent, liveTr := newParentWithTLS(t, srv.URL, &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: "localhost",
		// Verification is fully delegated to the closure below, which re-reads the
		// live pool per handshake — the same construction tlsconfig.TLSClientConfig
		// (spiffe.VaultServerTLS) uses.
		InsecureSkipVerify:    true, //nolint:gosec // VerifyPeerCertificate does the verification
		VerifyPeerCertificate: live.verify,
	})

	staticRes := NewHandedTokenEnvelopeResolverWithClient(staticParent, "secret", "transit-tenants")
	liveRes := NewHandedTokenEnvelopeResolverWithClient(liveParent, "secret", "transit-tenants")

	ref := TenantRef(orgA, "prod/app", "db_password")
	principal := Principal{Service: "runtime-fleet", OrgID: orgA.String(), Token: "handed-deployment-token"}

	resolve := func(r *HandedTokenEnvelopeResolver) (string, error) {
		v, err := r.Resolve(context.Background(), ref, principal)
		return v.Reveal(), err
	}

	// Before rotation: both trust CA-1.
	if got, err := resolve(staticRes); err != nil || got != "s3cr3t" {
		t.Fatalf("pre-rotation static-pool resolve = %q, %v; want s3cr3t, nil", got, err)
	}
	if got, err := resolve(liveRes); err != nil || got != "s3cr3t" {
		t.Fatalf("pre-rotation live-source resolve = %q, %v; want s3cr3t, nil", got, err)
	}

	// ---- CA rotation ----
	served.rotate(ca2.issue(t, "localhost"))
	live.set(poolOf(ca1, ca2))
	// Force a fresh handshake on both — a pooled connection would mask rotation.
	staticTr.CloseIdleConnections()
	liveTr.CloseIdleConnections()

	got, err := resolve(staticRes)
	if err == nil {
		t.Fatalf("static-pool resolve succeeded after CA rotation (%q) — the frozen-snapshot bug is not being reproduced", got)
	}
	if !isUnknownAuthority(err) {
		t.Fatalf("static-pool resolve failed with %v; want an unknown-authority error", err)
	}
	t.Logf("static CA snapshot after rotation (the outage): %v", err)

	if got, err := resolve(liveRes); err != nil || got != "s3cr3t" {
		t.Fatalf("live-source resolve = %q, %v after CA rotation; want s3cr3t, nil (must follow rotation with no restart)", got, err)
	}
}

// TestHandedTokenResolverNilBaseFailsClosed guards the delegation refactor: a nil
// base must still be a constructible, non-panicking state that fails EVERY
// Resolve closed, and the mount defaults must still be applied.
func TestHandedTokenResolverNilBaseFailsClosed(t *testing.T) {
	r := NewHandedTokenEnvelopeResolverWithClient(nil, "", "")
	if r.kvMount != "secret" || r.transitMount != "transit-tenants" {
		t.Fatalf("mount defaults lost: kv=%q transit=%q", r.kvMount, r.transitMount)
	}

	_, err := r.Resolve(context.Background(), TenantRef(orgA, "prod/app", "db_password"),
		Principal{Service: "runtime-fleet", OrgID: orgA.String(), Token: "handed-deployment-token"})
	if !errors.Is(err, ErrVaultUnavailable) {
		t.Fatalf("nil base must fail closed with ErrVaultUnavailable; got %v", err)
	}
}

// TestHandedTokenResolverIgnoresBaseToken is the SECURITY regression guard for
// inheriting the service's primary Vault client: sharing that client's transport
// must NOT smuggle in its standing capability. The fleet host is a bearer of a
// control-plane-minted, single-org token and must never hold a standing Vault
// credential (D-085 / D-089 / D-183). If this test ever fails, the credential
// broker model is broken.
func TestHandedTokenResolverIgnoresBaseToken(t *testing.T) {
	var hits int32
	fv := &fakeVault{plaintext: "s3cr3t"}
	inner := fv.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		inner(w, r)
	}))
	defer srv.Close()

	base := newParentClient(t, srv.URL)
	base.SetToken("service-primary-standing-token")
	r := NewHandedTokenEnvelopeResolverWithClient(base, "secret", "transit-tenants")

	ref := TenantRef(orgA, "prod/app", "db_password")

	// No handed token: the resolver must refuse rather than fall back to the base
	// client's standing token — and must not touch Vault at all.
	_, err := r.Resolve(context.Background(), ref,
		Principal{Service: "runtime-fleet", OrgID: orgA.String(), Token: ""})
	if !errors.Is(err, ErrNoHandedToken) {
		t.Fatalf("empty handed token must yield ErrNoHandedToken; got %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("resolver made %d Vault request(s) with no handed token — the base token may have been presented", n)
	}
	if fv.kvToken != "" || fv.decryptToken != "" {
		t.Fatalf("base token reached vault: kv=%q decrypt=%q", fv.kvToken, fv.decryptToken)
	}

	// Positive control: with a handed token, ONLY the handed token is presented.
	v, err := r.Resolve(context.Background(), ref,
		Principal{Service: "runtime-fleet", OrgID: orgA.String(), Token: "handed-deployment-token"})
	if err != nil {
		t.Fatalf("Resolve with handed token: %v", err)
	}
	if v.Reveal() != "s3cr3t" {
		t.Fatalf("Reveal = %q, want s3cr3t", v.Reveal())
	}
	if fv.kvToken != "handed-deployment-token" || fv.decryptToken != "handed-deployment-token" {
		t.Fatalf("wrong token presented: kv=%q decrypt=%q, want handed-deployment-token (base token must never be sent)",
			fv.kvToken, fv.decryptToken)
	}
	if base.Token() != "service-primary-standing-token" {
		t.Fatalf("resolver mutated the shared base client's token to %q", base.Token())
	}
}
