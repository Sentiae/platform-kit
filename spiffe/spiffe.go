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

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"google.golang.org/grpc/credentials"
)

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

// NewSource creates an X509Source backed by the SPIFFE Workload API. It reads
// the endpoint from SPIFFE_ENDPOINT_SOCKET and blocks until the first SVID
// update arrives. The caller owns the source and must Close it on shutdown.
func NewSource(ctx context.Context) (*workloadapi.X509Source, error) {
	return workloadapi.NewX509Source(ctx)
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
