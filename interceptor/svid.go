package interceptor

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// peerTransportTotal counts RPCs by the transport security of the peer:
// "mtls" when a peer SPIFFE SVID was presented, "plaintext" otherwise. This
// makes the mesh's mTLS adoption observable during the dormant rollout.
var peerTransportTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "grpc_peer_transport_total",
	Help: "gRPC calls by peer transport security (mtls when a peer SVID is present, else plaintext).",
}, []string{"security"})

// svidCtxKey is the unexported context key holding the peer SPIFFE ID.
type svidCtxKey struct{}

// SVIDFromContext returns the peer SPIFFE ID extracted by [UnarySVID] /
// [StreamSVID], and ok=false when the call had no peer SVID (e.g. plaintext).
func SVIDFromContext(ctx context.Context) (spiffeid.ID, bool) {
	id, ok := ctx.Value(svidCtxKey{}).(spiffeid.ID)
	return id, ok
}

// peerSVID extracts the SPIFFE ID from the peer's leaf certificate when the
// connection is mTLS with at least one peer cert carrying a well-formed
// SPIFFE ID. Returns ok=false for plaintext or a peer without an SVID.
func peerSVID(ctx context.Context) (spiffeid.ID, bool) {
	pr, ok := peer.FromContext(ctx)
	if !ok || pr.AuthInfo == nil {
		return spiffeid.ID{}, false
	}
	tlsInfo, ok := pr.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return spiffeid.ID{}, false
	}
	certs := tlsInfo.State.PeerCertificates
	if len(certs) == 0 {
		return spiffeid.ID{}, false
	}
	id, err := x509svid.IDFromCert(certs[0])
	if err != nil {
		return spiffeid.ID{}, false
	}
	return id, true
}

// enrichSVID stashes the peer SVID on ctx (when present) and records the
// transport-security metric. Safe on plaintext: it returns ctx unchanged and
// counts the call as "plaintext" — never errors.
func enrichSVID(ctx context.Context) context.Context {
	id, ok := peerSVID(ctx)
	if !ok {
		peerTransportTotal.WithLabelValues("plaintext").Inc()
		return ctx
	}
	peerTransportTotal.WithLabelValues("mtls").Inc()
	return context.WithValue(ctx, svidCtxKey{}, id)
}

// UnarySVID returns a unary server interceptor that extracts the peer SPIFFE
// ID (if any) into the context and records the transport-security metric. It
// is a no-op on plaintext connections and never errors — safe to install
// unconditionally.
func UnarySVID() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(enrichSVID(ctx), req)
	}
}

// StreamSVID returns a stream server interceptor mirroring [UnarySVID].
func StreamSVID() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, &wrappedServerStream{ServerStream: ss, ctx: enrichSVID(ss.Context())})
	}
}
