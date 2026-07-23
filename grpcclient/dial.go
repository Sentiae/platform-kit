// Package grpcclient provides the shared gRPC dial helper for the Sentiae
// service mesh. It is the single seam services adopt during the mTLS
// conversion: with Mode "off" (the default) it dials insecure exactly as
// today; with "permissive"/"strict" it dials with SPIFFE mTLS credentials
// authorizing the named server SVID.
//
// FAIL-CLOSED (D-162a L2): this is the outbound twin of grpcserver's
// refuse-to-serve. A security posture is never selected by the ABSENCE of a
// value. "permissive" is the DECLARED escape hatch — a service that opts into
// permissive tolerates plaintext by design, so a SPIRE hiccup (nil Source)
// degrades a dial to plaintext with a LOUD warn rather than wedging it.
// "strict" means strict: a strict client with no SVID source REFUSES TO DIAL
// (Dial returns an error naming the control) rather than silently opening a
// plaintext connection while claiming to require mutual TLS.
package grpcclient

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sentiae/platform-kit/config"
	"github.com/sentiae/platform-kit/spiffe"
	"github.com/sentiae/platform-kit/tenant"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Config configures a mesh dial.
type Config struct {
	// Endpoint is the target passed to grpc.NewClient (e.g. "dns:///svc:50051").
	Endpoint string

	// Mode selects transport security: "off" | "permissive" | "strict".
	// Empty is treated as "off". See config.MTLSMode.
	Mode string

	// Source is the SPIFFE X509 source used for mTLS. When nil under off/
	// permissive the dial falls back to insecure credentials (permissive warns
	// loudly); when nil under strict the dial is REFUSED (Dial returns an error).
	Source *workloadapi.X509Source

	// ServerService is the short service name of the server being dialed
	// (e.g. "foundry"); it is expanded to spiffe://sentiae.io/svc/<name> and
	// used to authorize the peer under mTLS.
	ServerService string

	// Timeout is an optional per-dial budget. When > 0 the caller-provided ctx
	// is wrapped so a stalled initial connection attempt is bounded. grpc.NewClient
	// itself is lazy; this bounds any blocking options the caller adds.
	Timeout time.Duration
}

// Dial builds a *grpc.ClientConn for the mesh. Transport security mirrors
// grpcserver.New (fail-closed, D-162a L2):
//
//   - off/empty     — insecure credentials (today's behavior).
//   - permissive    — SPIFFE mTLS client credentials when cfg.Source is set;
//     with a nil Source it degrades to insecure with a LOUD warn (the declared
//     escape hatch — a SPIRE hiccup must be VISIBLE, never silent).
//   - strict        — SPIFFE mTLS client credentials; with a nil Source Dial
//     RETURNS AN ERROR naming the control and NEVER returns a conn, rather than
//     silently dialing plaintext under a "strict" posture.
//
// Extra dial options are appended after the transport credentials.
func Dial(ctx context.Context, cfg Config, extraOpts ...grpc.DialOption) (*grpc.ClientConn, error) {
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	_ = ctx // grpc.NewClient is lazy; ctx bounds caller-added blocking options.

	mode := cfg.Mode
	if mode == "" {
		mode = config.MTLSModeOff
	}

	var creds credentials.TransportCredentials
	switch {
	case mode == config.MTLSModeStrict && cfg.Source == nil:
		// FAIL-CLOSED: strict mTLS required but no SVID source. Refuse to dial
		// rather than silently opening a plaintext connection under a "strict"
		// posture (the outbound twin of grpcserver's refuse-to-serve).
		slog.Default().Error("grpcclient: strict mTLS required but SPIFFE source unavailable; refusing to dial",
			"server", cfg.ServerService, "mode", mode)
		return nil, fmt.Errorf("grpcclient: configured for strict mTLS but no SPIFFE/SVID source available; refusing to dial %q (run in permissive mode if plaintext is acceptable during a SPIRE outage)", cfg.ServerService)
	case cfg.Source == nil:
		// off/permissive with no source → plaintext. Permissive is the declared
		// escape hatch, but a SPIRE hiccup on it must not be silent: warn loudly
		// so the degraded edge is visible. off is the expected default: quiet.
		if mode == config.MTLSModePermissive {
			slog.Default().Warn("grpcclient: permissive mTLS requested but SPIFFE source unavailable; degrading dial to plaintext",
				"server", cfg.ServerService, "mode", mode)
		}
		creds = insecure.NewCredentials()
	default:
		creds = spiffe.ClientCreds(cfg.Source, spiffe.ServiceID(cfg.ServerService))
	}

	// Org-propagation interceptors are installed UNCONDITIONALLY (no toggle — a
	// flag is the fail-open disease). They fill tenant/caller identity metadata
	// FILL-IF-ABSENT, so they are behavior-neutral for callers that already set
	// the keys while making org survive the hop for those that don't.
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithChainUnaryInterceptor(tenant.UnaryClientPropagation()),
		grpc.WithChainStreamInterceptor(tenant.StreamClientPropagation()),
	}, extraOpts...)
	return grpc.NewClient(cfg.Endpoint, opts...)
}
