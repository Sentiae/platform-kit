// Package spiffe provides the shared SPIFFE/mTLS primitives for the Sentiae
// zero-trust gRPC mesh: a Workload API X509 source and the transport
// credentials that authorize peers by SPIFFE ID within the trust domain.
//
// This package is dormant until a service opts in: it only constructs
// credentials from an X509Source obtained via the SPIFFE Workload API
// (SPIFFE_ENDPOINT_SOCKET). With no socket and no source, callers keep using
// insecure credentials (see grpcclient.Dial).
//
// TLS-layer authorization here is deliberately coarse — any SVID that is a
// member of the trust domain is accepted at the transport. Per-caller policy
// (which service may act in which org) is enforced one layer up, in
// tenant.Principal, off the peer SPIFFE ID.
package spiffe

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"google.golang.org/grpc/credentials"
)

// sourceStartupTimeout bounds how long NewSource / NewJWTSource wait for the
// first SVID before degrading. go-spiffe's constructors retry a missing or
// unreachable Workload API socket indefinitely; without this bound a service
// that opts into mTLS but lacks a reachable socket would HANG at startup
// instead of honoring the degrade-to-insecure contract callers rely on.
const sourceStartupTimeout = 15 * time.Second

// TrustDomain is the Sentiae SPIFFE trust domain in URI form.
const TrustDomain = "spiffe://sentiae.io"

// TrustDomainID returns the parsed trust domain. The error is discarded
// because TrustDomain is a compile-time constant that always parses.
func TrustDomainID() spiffeid.TrustDomain {
	// reason: TrustDomain is a known-good constant; parse never fails.
	td, _ := spiffeid.TrustDomainFromString(TrustDomain)
	return td
}

// ServiceID builds the workload SPIFFE ID for a Sentiae service:
// spiffe://sentiae.io/svc/<service>. Clients use it to name the server they
// dial; callers use it to parse peer IDs.
func ServiceID(service string) spiffeid.ID {
	// reason: trust domain is constant-valid and service is an internal,
	// controlled label; FromSegments only errors on malformed input.
	id, _ := spiffeid.FromSegments(TrustDomainID(), "svc", service)
	return id
}

// probeWorkloadAPI verifies the SPIFFE Workload API socket is reachable and
// actually delivers an SVID, bounded by sourceStartupTimeout. It exists so the
// source constructors below can degrade (return an error) instead of hanging:
// go-spiffe's NewX509Source/NewJWTSource retry a missing socket forever, so a
// service that opted into mTLS but lacks a reachable socket would otherwise
// wedge at startup rather than fall back to insecure.
func probeWorkloadAPI(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, sourceStartupTimeout)
	defer cancel()
	client, err := workloadapi.New(probeCtx)
	if err != nil {
		return fmt.Errorf("spiffe: workload API unreachable: %w", err)
	}
	defer client.Close()
	if _, err := client.FetchX509Context(probeCtx); err != nil {
		return fmt.Errorf("spiffe: no SVID from the workload API within %s (socket unreachable?): %w", sourceStartupTimeout, err)
	}
	return nil
}

// NewSource creates an X509Source backed by the SPIFFE Workload API. It reads
// the endpoint from SPIFFE_ENDPOINT_SOCKET. When the caller's ctx has no
// deadline, a bounded reachability probe runs first so an unreachable socket
// degrades (returns an error) instead of blocking forever; on success the
// source is built with the caller's long-lived ctx for SVID rotation. The
// caller owns the source and must Close it on shutdown.
func NewSource(ctx context.Context) (*workloadapi.X509Source, error) {
	if _, ok := ctx.Deadline(); !ok {
		if err := probeWorkloadAPI(ctx); err != nil {
			return nil, err
		}
	}
	return workloadapi.NewX509Source(ctx)
}

// NewJWTSource creates a JWTSource backed by the SPIFFE Workload API. It reads
// the endpoint from SPIFFE_ENDPOINT_SOCKET and, like NewSource, runs a bounded
// reachability probe first when the caller's ctx has no deadline. The caller
// owns the source and must Close it on shutdown. Callers fetch a JWT-SVID for a
// given audience via src.FetchJWTSVID.
func NewJWTSource(ctx context.Context) (*workloadapi.JWTSource, error) {
	if _, ok := ctx.Deadline(); !ok {
		if err := probeWorkloadAPI(ctx); err != nil {
			return nil, err
		}
	}
	return workloadapi.NewJWTSource(ctx)
}

// VaultServerTLS returns a *tls.Config that verifies and authorizes Vault's
// server X509-SVID against the SPIRE-provided bundle in src, requiring the
// server to present exactly svc/vault. It sets no client certificate — client
// authentication to Vault is carried by a JWT-SVID, not mTLS.
func VaultServerTLS(src *workloadapi.X509Source) *tls.Config {
	return tlsconfig.TLSClientConfig(src, tlsconfig.AuthorizeID(ServiceID("vault")))
}

// ServerCreds returns gRPC transport credentials that present the workload
// SVID and require + authorize any client SVID that is a member of the trust
// domain. Fine-grained per-caller policy is enforced above in authz.
func ServerCreds(src *workloadapi.X509Source) credentials.TransportCredentials {
	cfg := tlsconfig.MTLSServerConfig(src, src, tlsconfig.AuthorizeMemberOf(TrustDomainID()))
	return credentials.NewTLS(cfg)
}

// ClientCreds returns gRPC transport credentials that present the workload
// SVID and authorize the server to be exactly serverID.
func ClientCreds(src *workloadapi.X509Source, serverID spiffeid.ID) credentials.TransportCredentials {
	cfg := tlsconfig.MTLSClientConfig(src, src, tlsconfig.AuthorizeID(serverID))
	return credentials.NewTLS(cfg)
}
