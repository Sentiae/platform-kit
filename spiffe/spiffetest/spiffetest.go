// Package spiffetest runs an in-process SPIFFE Workload API over a unix socket
// so a test can obtain a REAL *workloadapi.X509Source and perform a REAL mTLS
// handshake. workloadapi.X509Source has no public constructor other than a
// Workload API, so without this package the mesh transport can only be asserted
// against configuration, never against a live listener.
//
// TEST SUPPORT ONLY. Never import this package from non-test code: it mints a
// self-signed CA and hands out SVIDs for any name asked for.
package spiffetest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/proto/spiffe/workload"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"google.golang.org/grpc"
)

// TrustDomain mirrors spiffe.TrustDomain. It is duplicated rather than imported
// because the spiffe package's own in-package tests import spiffetest, and an
// import back into spiffe would be an import cycle.
const TrustDomain = "spiffe://sentiae.io"

// CA is a throwaway SPIFFE certificate authority for the Sentiae trust domain.
// One CA can issue SVIDs for several services, which is what lets a test stand
// up a server holding one identity and a client holding another.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	der  []byte
}

// NewCA mints a self-signed CA for spiffe://sentiae.io, valid one hour.
func NewCA(tb testing.TB) *CA {
	tb.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatalf("spiffetest: generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(tb),
		Subject:               pkix.Name{Organization: []string{"SPIFFETEST"}, CommonName: "spiffetest-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		URIs:                  []*url.URL{mustURL(tb, TrustDomain)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		tb.Fatalf("spiffetest: create CA cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		tb.Fatalf("spiffetest: parse CA cert: %v", err)
	}
	return &CA{cert: cert, key: key, der: der}
}

// StartWorkloadAPI serves a SPIFFE Workload API on socketPath that hands out a
// single X509-SVID for spiffe://sentiae.io/svc/<service>, signed by ca. The
// returned stop function shuts the server down; it is also registered with
// tb.Cleanup.
func (ca *CA) StartWorkloadAPI(tb testing.TB, socketPath, service string) (stop func()) {
	tb.Helper()

	id, err := spiffeid.FromSegments(mustTrustDomain(tb), "svc", service)
	if err != nil {
		tb.Fatalf("spiffetest: build SPIFFE ID for %q: %v", service, err)
	}
	leafDER, leafKeyPKCS8 := ca.issue(tb, id)

	impl := &workloadAPI{
		svids: &workload.X509SVIDResponse{
			Svids: []*workload.X509SVID{{
				SpiffeId:    id.String(),
				X509Svid:    leafDER,
				X509SvidKey: leafKeyPKCS8,
				Bundle:      ca.der,
			}},
		},
		bundles: &workload.X509BundlesResponse{
			Bundles: map[string][]byte{TrustDomain: ca.der},
		},
	}

	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		tb.Fatalf("spiffetest: listen on %q: %v", socketPath, err)
	}
	srv := grpc.NewServer()
	workload.RegisterSpiffeWorkloadAPIServer(srv, impl)
	go func() {
		// reason: Serve always ends with an error once the listener is closed by
		// stop(); surfacing it would fail an already-finished test.
		_ = srv.Serve(lis)
	}()

	var once bool
	stop = func() {
		if once {
			return
		}
		once = true
		srv.Stop()
	}
	tb.Cleanup(stop)
	return stop
}

// NewSource starts a Workload API on a short temporary socket path and returns
// a live X509Source holding svc/<service>. The socket lives under os.MkdirTemp
// rather than tb.TempDir because a unix socket path is limited to 104 bytes on
// macOS and tb.TempDir paths routinely exceed it.
func (ca *CA) NewSource(tb testing.TB, service string) *workloadapi.X509Source {
	tb.Helper()

	dir, err := os.MkdirTemp("", "wl")
	if err != nil {
		tb.Fatalf("spiffetest: temp dir: %v", err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, "api.sock")
	ca.StartWorkloadAPI(tb, sock, service)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	src, err := workloadapi.NewX509Source(ctx,
		workloadapi.WithClientOptions(workloadapi.WithAddr("unix://"+sock)))
	if err != nil {
		tb.Fatalf("spiffetest: new X509 source for %q: %v", service, err)
	}
	tb.Cleanup(func() { _ = src.Close() })
	return src
}

// issue signs a leaf certificate carrying id as its only URI SAN and returns
// the DER certificate plus its PKCS#8 private key, the two encodings the
// Workload API wire format requires.
func (ca *CA) issue(tb testing.TB, id spiffeid.ID) (certDER, keyPKCS8 []byte) {
	tb.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatalf("spiffetest: generate leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(tb),
		Subject:      pkix.Name{Organization: []string{"SPIFFETEST"}},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{mustURL(tb, id.String())},
	}
	certDER, err = x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		tb.Fatalf("spiffetest: sign leaf for %s: %v", id, err)
	}
	keyPKCS8, err = x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		tb.Fatalf("spiffetest: marshal leaf key: %v", err)
	}
	return certDER, keyPKCS8
}

// workloadAPI answers the two X.509 streams go-spiffe's X509Source consumes and
// leaves the JWT methods Unimplemented (the embedded server supplies them).
type workloadAPI struct {
	workload.UnimplementedSpiffeWorkloadAPIServer

	svids   *workload.X509SVIDResponse
	bundles *workload.X509BundlesResponse
}

func (w *workloadAPI) FetchX509SVID(_ *workload.X509SVIDRequest, stream grpc.ServerStreamingServer[workload.X509SVIDResponse]) error {
	if err := stream.Send(w.svids); err != nil {
		return err
	}
	<-stream.Context().Done()
	return stream.Context().Err()
}

func (w *workloadAPI) FetchX509Bundles(_ *workload.X509BundlesRequest, stream grpc.ServerStreamingServer[workload.X509BundlesResponse]) error {
	if err := stream.Send(w.bundles); err != nil {
		return err
	}
	<-stream.Context().Done()
	return stream.Context().Err()
}

func serial(tb testing.TB) *big.Int {
	tb.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		tb.Fatalf("spiffetest: serial: %v", err)
	}
	return n
}

func mustURL(tb testing.TB, raw string) *url.URL {
	tb.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		tb.Fatalf("spiffetest: parse %q: %v", raw, err)
	}
	return u
}

func mustTrustDomain(tb testing.TB) spiffeid.TrustDomain {
	tb.Helper()
	td, err := spiffeid.TrustDomainFromString(TrustDomain)
	if err != nil {
		tb.Fatalf("spiffetest: parse trust domain: %v", err)
	}
	return td
}
