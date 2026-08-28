package grpcserver

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// probeMTLSListener proves, in one round trip against the LIVE listener, that
// the mesh transport actually works: the ClientHello reaches the mTLS server
// (cmux routing under permissive), the server presents exactly the SVID this
// source holds, the server accepts a trust-domain client SVID, and the
// interceptor chain admits a peer-SVID call. It is a measurement of the
// listener, not of the configuration — the difference between "strict is
// configured" and "strict is served".
//
// The expected server identity is the SVID the source ACTUALLY HOLDS, never
// spiffe.ServiceID(cfg.ServiceName): ServiceName is a log label and services
// legitimately differ from it (augur's label is "augur", its SVID is
// svc/infrastructure-intelligence), so authorizing the label would refuse a
// correct boot.
func probeMTLSListener(ctx context.Context, addr net.Addr, src *workloadapi.X509Source) error {
	if src == nil {
		return errors.New("source holds no SVID: source is nil")
	}
	svid, err := src.GetX509SVID()
	if err != nil {
		return fmt.Errorf("source holds no SVID: %w", err)
	}
	if svid == nil {
		return errors.New("source holds no SVID")
	}

	network, target := probeTarget(addr)
	creds := credentials.NewTLS(tlsconfig.MTLSClientConfig(src, src, tlsconfig.AuthorizeID(svid.ID)))
	conn, err := grpc.NewClient("passthrough:///"+target,
		grpc.WithTransportCredentials(creds),
		grpc.WithContextDialer(func(dialCtx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(dialCtx, network, target)
		}),
	)
	if err != nil {
		return fmt.Errorf("dial own listener %s: %w", target, err)
	}
	defer func() {
		// reason: the probe connection is torn down either way; a close error
		// tells the operator nothing the probe result does not already say.
		_ = conn.Close()
	}()

	// WaitForReady tolerates the race between the serve goroutine and this
	// probe; it does NOT hide a handshake failure, which keeps failing until
	// ctx expires and surfaces as the connection error.
	_, err = grpc_health_v1.NewHealthClient(conn).Check(ctx,
		&grpc_health_v1.HealthCheckRequest{}, grpc.WaitForReady(true))

	// NotFound means a caller-registered health server does not know service
	// "" — the handshake and routing are still proven, which is what the probe
	// is for.
	if err == nil || status.Code(err) == codes.NotFound {
		return nil
	}
	return fmt.Errorf("health check against %s as %s: %w", target, svid.ID, err)
}

// probeTarget turns a listener address into something dialable from this
// process. net.Listen("tcp", ":50054") reports [::]:50054, and an unspecified
// address cannot be dialed, so it is rewritten to the matching loopback.
// Non-TCP networks (unix) are dialed as reported.
func probeTarget(addr net.Addr) (network, target string) {
	network, target = addr.Network(), addr.String()

	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return network, target
	}
	if host == "" {
		return network, net.JoinHostPort("127.0.0.1", port)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsUnspecified() {
		return network, target
	}
	if ip.To4() != nil {
		return network, net.JoinHostPort("127.0.0.1", port)
	}
	return network, net.JoinHostPort("::1", port)
}

// svidIDString renders the SPIFFE ID a source currently holds for logging, or
// "" when the source has none.
func svidIDString(src *workloadapi.X509Source) string {
	if src == nil {
		return ""
	}
	svid, err := src.GetX509SVID()
	if err != nil || svid == nil {
		return ""
	}
	return svid.ID.String()
}
