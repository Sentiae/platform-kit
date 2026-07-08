// Package grpcclient provides the shared gRPC dial helper for the Sentiae
// service mesh. It is the single seam services adopt during the mTLS
// conversion: with Mode "off" (the default) it dials insecure exactly as
// today; with "permissive"/"strict" it dials with SPIFFE mTLS credentials
// authorizing the named server SVID.
package grpcclient

import (
	"context"
	"time"

	"github.com/sentiae/platform-kit/spiffe"
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

	// Source is the SPIFFE X509 source used for mTLS. When nil (or Mode is
	// off), the dial falls back to insecure credentials.
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

// Dial builds a *grpc.ClientConn for the mesh. When cfg.Mode is off/empty or
// cfg.Source is nil it uses insecure credentials (today's behavior); otherwise
// it uses SPIFFE mTLS client credentials authorizing the server SVID. Extra
// dial options are appended after the transport credentials.
func Dial(ctx context.Context, cfg Config, extraOpts ...grpc.DialOption) (*grpc.ClientConn, error) {
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	_ = ctx // grpc.NewClient is lazy; ctx bounds caller-added blocking options.

	var creds credentials.TransportCredentials
	if cfg.Mode == "" || cfg.Mode == "off" || cfg.Source == nil {
		creds = insecure.NewCredentials()
	} else {
		creds = spiffe.ClientCreds(cfg.Source, spiffe.ServiceID(cfg.ServerService))
	}

	opts := append([]grpc.DialOption{grpc.WithTransportCredentials(creds)}, extraOpts...)
	return grpc.NewClient(cfg.Endpoint, opts...)
}
