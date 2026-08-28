package grpcserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sentiae/platform-kit/config"
	"github.com/sentiae/platform-kit/spiffe"
	"github.com/sentiae/platform-kit/spiffe/spiffetest"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// startServer serves a bare grpc.NewServer with the given transport credentials
// (nil = plaintext) on the given listener, registering the health service the
// probe calls. It returns nothing: the caller already holds the listener.
func startServer(t *testing.T, lis net.Listener, creds credentials.TransportCredentials, registerHealth func(*grpc.Server)) {
	t.Helper()

	var opts []grpc.ServerOption
	if creds != nil {
		opts = append(opts, grpc.Creds(creds))
	}
	srv := grpc.NewServer(opts...)
	registerHealth(srv)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
}

// healthServing registers an all-SERVING health service, matching what
// Builder.Serve installs when the caller registered none.
func healthServing(srv *grpc.Server) {
	h := health.NewServer()
	h.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(srv, h)
}

// healthNotFoundForEmpty registers a health service that knows a named service
// but NOT "", so Check("") answers NotFound — the shape a caller gets when it
// registered its own health server. The handshake is still proven.
func healthNotFoundForEmpty(srv *grpc.Server) {
	h := health.NewServer()
	h.SetServingStatus("some.Named.Service", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(srv, h)
}

func listen(t *testing.T, network, addr string) net.Listener {
	t.Helper()
	lis, err := net.Listen(network, addr)
	if err != nil {
		t.Fatalf("listen %s %s: %v", network, addr, err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	return lis
}

func TestProbeMTLSListener(t *testing.T) {
	ca := spiffetest.NewCA(t)
	vigil := ca.NewSource(t, "vigil")
	impostor := ca.NewSource(t, "impostor")

	tests := []struct {
		name string
		// setup binds a listener, starts a server on it, and returns the addr
		// the probe is pointed at.
		setup   func(t *testing.T) net.Addr
		src     *workloadapi.X509Source
		budget  time.Duration
		wantErr bool
		// wantErrContains is the exact operator-facing evidence the failure must
		// carry; empty means "any error".
		wantErrContains string
	}{
		{
			// The measured 2026-08-06 homelab condition: a listener serving
			// plaintext while the service claims a mesh posture. This is the
			// verbatim text delivery-service logged for three weeks.
			name: "plaintext listener is refused",
			setup: func(t *testing.T) net.Addr {
				lis := listen(t, "tcp", "127.0.0.1:0")
				startServer(t, lis, nil, healthServing)
				return lis.Addr()
			},
			src:             vigil,
			budget:          3 * time.Second,
			wantErr:         true,
			wantErrContains: "first record does not look like a TLS handshake",
		},
		{
			name: "mTLS listener with the source's own SVID passes",
			setup: func(t *testing.T) net.Addr {
				lis := listen(t, "tcp", "127.0.0.1:0")
				startServer(t, lis, spiffe.ServerCreds(vigil), healthServing)
				return lis.Addr()
			},
			src:    vigil,
			budget: 10 * time.Second,
		},
		{
			// The probe authorizes the SVID the source holds. A listener that
			// answers TLS with SOMEONE ELSE'S identity (same CA, same trust
			// domain) is not this service's listener.
			name: "mTLS listener presenting another SVID is refused",
			setup: func(t *testing.T) net.Addr {
				lis := listen(t, "tcp", "127.0.0.1:0")
				startServer(t, lis, spiffe.ServerCreds(impostor), healthServing)
				return lis.Addr()
			},
			src:     vigil,
			budget:  3 * time.Second,
			wantErr: true,
		},
		{
			// net.Listen("tcp", ":0") reports [::]:port, which cannot be dialed.
			name: "unspecified IPv6 address is rewritten to loopback",
			setup: func(t *testing.T) net.Addr {
				lis := listen(t, "tcp", "[::]:0")
				startServer(t, lis, spiffe.ServerCreds(vigil), healthServing)
				return lis.Addr()
			},
			src:    vigil,
			budget: 10 * time.Second,
		},
		{
			name: "unspecified IPv4 address is rewritten to loopback",
			setup: func(t *testing.T) net.Addr {
				lis := listen(t, "tcp4", "0.0.0.0:0")
				startServer(t, lis, spiffe.ServerCreds(vigil), healthServing)
				return lis.Addr()
			},
			src:    vigil,
			budget: 10 * time.Second,
		},
		{
			// A caller-registered health server that does not know "" still
			// proves the handshake, the routing and the interceptor chain.
			name: "caller health server answering NotFound still passes",
			setup: func(t *testing.T) net.Addr {
				lis := listen(t, "tcp", "127.0.0.1:0")
				startServer(t, lis, spiffe.ServerCreds(vigil), healthNotFoundForEmpty)
				return lis.Addr()
			},
			src:    vigil,
			budget: 10 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := tt.setup(t)

			ctx, cancel := context.WithTimeout(context.Background(), tt.budget)
			defer cancel()

			err := probeMTLSListener(ctx, addr, tt.src)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("probe of %s returned nil; want a refusal", addr)
				}
				if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("probe error %q must contain %q (the operator-facing evidence)", err, tt.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("probe of %s returned %v; want nil", addr, err)
			}
		})
	}
}

func TestProbeMTLSListener_NilSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	lis := listen(t, "tcp", "127.0.0.1:0")
	err := probeMTLSListener(ctx, lis.Addr(), nil)
	if err == nil || !strings.Contains(err.Error(), "source holds no SVID") {
		t.Fatalf("nil source: got %v, want an error naming the missing SVID", err)
	}
}

func TestServe_Permissive_TLSHalfAnswers(t *testing.T) {
	ca := spiffetest.NewCA(t)
	src := ca.NewSource(t, "vigil")

	b := New(Config{Mode: config.MTLSModePermissive, Source: src, ServiceName: "vigil"})
	lis := listen(t, "tcp", "127.0.0.1:0")

	serveDone := make(chan error, 1)
	go func() { serveDone <- b.Serve(lis) }()
	t.Cleanup(func() {
		b.Stop()
		_ = lis.Close()
		<-serveDone
	})

	select {
	case err := <-b.Ready():
		if err != nil {
			t.Fatalf("permissive+source: Ready() = %v, want nil (the self-probe must pass)", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("permissive+source: Ready() never fired")
	}

	// The TLS half answers with vigil's SVID, verified independently of the
	// probe: a raw tls.Dial, not the code under test.
	tlsCfg := tlsconfig.MTLSClientConfig(src, src, tlsconfig.AuthorizeID(spiffe.ServiceID("vigil")))
	conn, err := tls.Dial("tcp", lis.Addr().String(), tlsCfg)
	if err != nil {
		t.Fatalf("tls.Dial of the permissive listener: %v", err)
	}
	defer conn.Close()

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 || len(certs[0].URIs) == 0 {
		t.Fatal("permissive listener presented no SVID URI")
	}
	if got, want := certs[0].URIs[0].String(), "spiffe://sentiae.io/svc/vigil"; got != want {
		t.Fatalf("listener presented %q, want %q", got, want)
	}

	// The plaintext half still answers on the same port (that is what permissive
	// means), so cmux routing is proven in both directions.
	plain, err := grpc.NewClient("passthrough:///"+lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("plaintext client: %v", err)
	}
	defer plain.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := grpc_health_v1.NewHealthClient(plain).Check(ctx,
		&grpc_health_v1.HealthCheckRequest{}, grpc.WaitForReady(true))
	if err != nil {
		t.Fatalf("plaintext health check: %v", err)
	}
	if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("plaintext health status = %v, want SERVING", resp.GetStatus())
	}
}

func TestServe_RefusesWhenSelfProbeFails(t *testing.T) {
	ca := spiffetest.NewCA(t)
	src := ca.NewSource(t, "vigil")

	b := New(Config{Mode: config.MTLSModeStrict, Source: src, ServiceName: "vigil"})

	// held is an mTLS gRPC connection accepted while the listener was serving,
	// before the forced refusal. Only b.Stop() tears accepted connections
	// down; lis.Close() alone leaves them being served — so this connection is
	// what makes b.Stop() on the refusal path load-bearing.
	var held *grpc.ClientConn
	// The override IS the control: any reason the listener cannot answer its own
	// mTLS round trip must end the boot, not be logged and continued past.
	b.selfProbe = func(ctx context.Context, addr net.Addr, src *workloadapi.X509Source) error {
		svid, err := src.GetX509SVID()
		if err != nil {
			return err
		}
		network, target := probeTarget(addr)
		conn, err := grpc.NewClient("passthrough:///"+target,
			grpc.WithTransportCredentials(credentials.NewTLS(
				tlsconfig.MTLSClientConfig(src, src, tlsconfig.AuthorizeID(svid.ID)))),
			grpc.WithContextDialer(func(dialCtx context.Context, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(dialCtx, network, target)
			}))
		if err != nil {
			return err
		}
		if _, err := grpc_health_v1.NewHealthClient(conn).Check(ctx,
			&grpc_health_v1.HealthCheckRequest{}, grpc.WaitForReady(true)); err != nil {
			return fmt.Errorf("pre-refusal health check: %w", err)
		}
		held = conn
		return errors.New("forced")
	}
	t.Cleanup(func() {
		if held != nil {
			_ = held.Close()
		}
	})

	lis := listen(t, "tcp", "127.0.0.1:0")
	addr := lis.Addr().String()

	done := make(chan error, 1)
	go func() { done <- b.Serve(lis) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("failed self-probe: Serve returned nil; want a refusal")
		}
		for _, want := range []string{"forced", "vigil", addr} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("Serve error %q must mention %q", err, want)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed self-probe: Serve blocked instead of refusing")
	}

	select {
	case err := <-b.Ready():
		if err == nil {
			t.Fatal("failed self-probe: Ready() yielded nil; a refused boot must not report ready")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed self-probe: Ready() never fired")
	}

	// The refusal must have TORN THE LISTENER DOWN. A listener still accepting
	// after a refused boot is the plaintext surface this whole slice removes.
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if dialErr != nil {
			break
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			t.Fatalf("listener at %s still accepts connections after a refused boot", addr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if held == nil {
		t.Fatal("test bug: no pre-refusal connection was established")
	}
	// The accepted connection must be torn down, not orphaned: an mTLS peer
	// admitted before the refusal must not keep being served after it.
	// WaitForReady stays default-false so a torn-down transport fails fast with
	// Unavailable while a surviving one answers SERVING.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := grpc_health_v1.NewHealthClient(held).Check(ctx,
		&grpc_health_v1.HealthCheckRequest{}); err == nil {
		t.Fatal("an mTLS connection accepted before the refusal still answers after it; b.Stop() is not tearing accepted connections down")
	}
}
