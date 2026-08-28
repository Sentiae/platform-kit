// Package grpcserver provides the shared dual-mode gRPC server builder for the
// Sentiae service mesh. It is the server-side counterpart to grpcclient.Dial:
// a service registers its handlers ONCE (via Registrar) and gets the right
// transport(s) for the configured mTLS mode transparently.
//
//   - off        — one plaintext server (today's behavior; the default).
//   - permissive — one plaintext server AND one mTLS server behind one listener
//     (via cmux), so peers may connect with or without an SVID on the same port.
//   - strict     — one mTLS server only.
//
// FAIL-CLOSED (D-162a L2, D-189): a security posture is never selected by the
// ABSENCE of a value. "permissive" tolerates plaintext PEERS on the same port;
// it never means "serve no TLS". ANY mode other than "off" requires an SVID
// source — a nil Source under permissive or strict is a build error, recorded
// by New and returned by Serve before the listener is touched, so nothing
// insecure is ever served. A mesh listener without an SVID is a plaintext
// surface, whatever posture it declares.
//
// And a declared posture is not evidence: Serve PROVES the mTLS transport
// answers on the bound listener — one real mTLS round trip against the exact
// SVID the source holds — before it hands control back (see selfprobe.go).
// A failed probe stops every server, closes the listener, and returns the
// error, so a boot that cannot serve mTLS is a loud refusal (non-zero exit,
// restart) rather than a "healthy" plaintext port.
//
// If a service must stay up through a SPIRE outage there is no honest escape
// hatch here: it runs "off" and declares itself outside the mesh.
package grpcserver

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/sentiae/platform-kit/config"
	"github.com/sentiae/platform-kit/interceptor"
	"github.com/sentiae/platform-kit/spiffe"
	"github.com/sentiae/platform-kit/tenant"
	"github.com/soheilhy/cmux"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

// Config configures the dual-mode server build.
type Config struct {
	// Mode selects transport security: "off" | "permissive" | "strict".
	// Empty is treated as "off". See config.MTLSMode.
	Mode string

	// Source is the SPIFFE X509 source used for the mTLS server. Nil under any
	// mode other than "off" is a build error: the builder refuses to serve
	// (see package doc).
	Source *workloadapi.X509Source

	// ServiceName is the short service name (e.g. "identity"), used only for
	// log context.
	ServiceName string
}

// Builder holds the one or two *grpc.Server(s) plus the listener strategy for
// the configured mode. Register handlers via Registrar, then call Serve.
type Builder struct {
	serviceName string

	// servers holds every underlying server (1 or 2). Registrar, reflection,
	// health, and Stop/GracefulStop fan out over this slice.
	servers []*grpc.Server

	// plain and mtls name the two servers when there are two (permissive). When
	// there is only one server, exactly one of these is set and the other nil.
	plain *grpc.Server
	mtls  *grpc.Server

	// source is the SVID source the mTLS server presents. It is also what the
	// boot-time self-probe dials with, and whose SVID ID the probe requires the
	// listener to answer with. Nil only under mode "off".
	source *workloadapi.X509Source

	// selfProbe performs the boot-time mTLS round trip against the bound
	// listener. It is probeMTLSListener in production; tests override it to
	// drive the refusal path.
	selfProbe func(ctx context.Context, addr net.Addr, src *workloadapi.X509Source) error

	// ready carries the single startup verdict: nil once the listener is
	// serving and (under a mesh mode) has answered the self-probe, else the
	// refusal error. Buffered so Serve never blocks on a caller that does not
	// read it.
	ready chan error

	// buildErr, when non-nil, marks a fail-closed-invalid configuration (a mesh
	// mode with no SVID source, or an unrecognized mode). No server is built;
	// Serve returns this error before opening the listener so nothing insecure
	// is ever served.
	buildErr error
}

// New builds the server(s) for the given mode. The variadic opts are the
// service's base server options (its ChainUnary/ChainStream interceptors and
// any others); transport credentials are added by New per mode.
//
// Interceptor ordering (execution order, outermost → innermost): the SVID
// interceptors are PREPENDED first (they extract the peer identity every later
// layer reads); then the service's own opts (Auth, any OrgField); then the
// org-propagation interceptors, APPENDED innermost so they run AFTER Auth has
// established the principal and AFTER any service OrgField has resolved a proto
// org — exactly what inboundPropagation needs to re-verify an asserted org and
// to honor an already-scoped active org. Propagation is installed on every
// underlying server (both transports in permissive mode).
//
// New does not panic. Fail-closed (D-162a L2, D-189): any mode other than "off"
// with a nil Source is an invalid configuration — New builds NO server and
// records a build error naming the service and the mode; Serve returns that
// error before opening the listener.
func New(cfg Config, opts ...grpc.ServerOption) *Builder {
	b := &Builder{
		serviceName: cfg.ServiceName,
		source:      cfg.Source,
		selfProbe:   probeMTLSListener,
		ready:       make(chan error, 1),
	}

	mode := cfg.Mode
	if mode == "" {
		mode = config.MTLSModeOff
	}

	// FAIL-CLOSED: an unrecognized non-empty mode is a misconfiguration, not a
	// request for plaintext. config.Load already rejects a typo'd
	// APP_GRPC_MTLS_MODE fleet-wide, but the builder must not silently pick
	// "off" for a bad value that reaches it another way — that is exactly the
	// posture-by-absence this package's doc forbids. Refuse to serve.
	if mode != config.MTLSModeOff && mode != config.MTLSModePermissive && mode != config.MTLSModeStrict {
		b.buildErr = fmt.Errorf("grpcserver: service %q configured with unrecognized mTLS mode %q; refusing to serve (want one of %q, %q, %q)", cfg.ServiceName, mode, config.MTLSModeOff, config.MTLSModePermissive, config.MTLSModeStrict)
		slog.Default().Error("grpcserver: unrecognized mTLS mode; refusing to serve",
			"service", cfg.ServiceName, "mode", mode)
		return b
	}

	// FAIL-CLOSED: a mesh mode with no SVID source. There is no degrade branch:
	// permissive tolerates plaintext peers, never a listener with no TLS half.
	if mode != config.MTLSModeOff && cfg.Source == nil {
		b.buildErr = fmt.Errorf("grpcserver: service %q configured for %s mTLS but no SPIFFE/SVID source is available; refusing to serve (D-189: a mesh listener without an SVID is a plaintext surface)", cfg.ServiceName, mode)
		slog.Default().Error("grpcserver: mesh mTLS required but SPIFFE source unavailable; refusing to serve",
			"service", cfg.ServiceName, "mode", mode)
		return b
	}

	// The SVID interceptors extract the peer's SPIFFE ID (if any) into ctx and
	// record grpc_peer_transport_total{security}, making mTLS adoption
	// observable across the mesh. They are no-ops on plaintext and never error,
	// so they are prepended to every underlying server (both transports).
	svidOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(interceptor.UnarySVID()),
		grpc.ChainStreamInterceptor(interceptor.StreamSVID()),
	}

	// Org-propagation interceptors, APPENDED after the service's own opts so
	// they run innermost — after Auth (principal) and any service OrgField
	// (active org). Installed unconditionally (fill-if-absent, behavior-neutral).
	propagationOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(tenant.UnaryInboundPropagation()),
		grpc.ChainStreamInterceptor(tenant.StreamInboundPropagation()),
	}

	// serverOpts concatenates svid (prepended) + the service's base opts +
	// propagation (appended) into the option order gRPC chains in.
	serverOpts := func(base ...grpc.ServerOption) []grpc.ServerOption {
		out := make([]grpc.ServerOption, 0, len(base)+len(svidOpts)+len(propagationOpts))
		out = append(out, base...)
		out = append(out, opts...)
		out = append(out, propagationOpts...)
		return out
	}

	switch mode {
	case config.MTLSModePermissive:
		b.plain = grpc.NewServer(serverOpts(svidOpts...)...)
		b.mtls = grpc.NewServer(serverOpts(append([]grpc.ServerOption{grpc.Creds(spiffe.ServerCreds(cfg.Source))}, svidOpts...)...)...)
		b.servers = []*grpc.Server{b.plain, b.mtls}
	case config.MTLSModeStrict:
		b.mtls = grpc.NewServer(serverOpts(append([]grpc.ServerOption{grpc.Creds(spiffe.ServerCreds(cfg.Source))}, svidOpts...)...)...)
		b.servers = []*grpc.Server{b.mtls}
	default: // off — the only value reaching here; unrecognized modes are
		// rejected above, so default no longer swallows a typo into plaintext.
		b.plain = grpc.NewServer(serverOpts(svidOpts...)...)
		b.servers = []*grpc.Server{b.plain}
	}

	return b
}

// Registrar returns a grpc.ServiceRegistrar that fans RegisterService out to
// every underlying server, so `xv1.RegisterFooServer(b.Registrar(), impl)`
// registers into both the plaintext and mTLS servers unchanged.
func (b *Builder) Registrar() grpc.ServiceRegistrar {
	return multiRegistrar{servers: b.servers}
}

// Server returns a primary underlying *grpc.Server for introspection only
// (e.g. GetServiceInfo in tests). It is the plaintext server when present
// (modes off/permissive) else the mTLS server (strict); nil if no server was
// built — including a fail-closed builder (strict mTLS with no SVID source),
// which builds no server at all. Do NOT call Serve on it directly — use
// Builder.Serve so every configured transport is served and so the fail-closed
// build error is honored.
func (b *Builder) Server() *grpc.Server {
	if len(b.servers) == 0 {
		return nil
	}
	return b.servers[0]
}

// selfProbeTimeout bounds the boot-time mTLS round trip. The identity wait is
// already bounded upstream by spiffe.sourceStartupTimeout; this budget covers
// only the handshake against a listener that is already accepting.
const selfProbeTimeout = 15 * time.Second

// Ready reports the startup verdict exactly once: nil when the listener is
// serving and, under a mesh mode, has answered a real mTLS round trip; else the
// refusal error Serve returned. Callers gate anything that advertises the
// service (an HTTP listener, a readiness probe) on this channel so a refused
// mesh boot is never reported healthy.
func (b *Builder) Ready() <-chan error {
	return b.ready
}

// Serve registers reflection (and a default health service if the caller did
// not already register one) on each underlying server, then serves: one server
// directly, or plaintext and TLS multiplexed on the single listener via cmux.
//
// Under any mesh mode Serve then PROVES the transport before reporting ready —
// it dials its own listener with a real mTLS client built from the same source
// and completes one health check (see selfprobe.go). A failed probe stops every
// server, closes the listener, and returns the error, so a listener that cannot
// answer mTLS is a boot refusal rather than a silent plaintext surface (D-189).
//
// Serve blocks until the listener is closed.
func (b *Builder) Serve(lis net.Listener) error {
	// Fail-closed: a poisoned builder (a mesh mode with no SVID source, or an
	// unrecognized mode) never serves. Return before touching the listener so
	// nothing insecure is served.
	if b.buildErr != nil {
		b.ready <- b.buildErr
		return b.buildErr
	}

	for _, srv := range b.servers {
		ensureHealth(srv)
		reflection.Register(srv)
	}

	// Mode off: one plaintext server, no mesh posture claimed, nothing to prove.
	if b.mtls == nil {
		b.ready <- nil
		return b.servers[0].Serve(lis)
	}

	serveErr := make(chan error, 1)
	if b.plain == nil {
		// strict: the mTLS server owns the listener outright.
		//
		// reason: no ctx and no recover on purpose — the accept loop is ended by
		// the listener closing, not by a context, and a panic inside it must
		// crash the boot (the restart is the recovery) rather than be logged past.
		go func() { serveErr <- b.mtls.Serve(lis) }()
	} else {
		// permissive: cmux routes TLS handshakes to the mTLS server and
		// everything else to the plaintext server.
		m := cmux.New(lis)
		tlsL := m.Match(cmux.TLS())
		plainL := m.Match(cmux.Any())

		go b.serve(b.mtls, tlsL, "mtls")
		go b.serve(b.plain, plainL, "plain")

		// reason: no ctx and no recover, same reason as above.
		go func() { serveErr <- m.Serve() }()
	}

	// reason: Serve has no inbound ctx — this is boot, not an inbound-triggered
	// outbound call, the one case CLAUDE.md §27 leaves to a fresh context.
	ctx, cancel := context.WithTimeout(context.Background(), selfProbeTimeout)
	defer cancel()

	if err := b.selfProbe(ctx, lis.Addr(), b.source); err != nil {
		b.Stop()
		// reason: under strict, Stop() has already closed lis (grpc closes every
		// listener it serves) and this Close returns "use of closed network
		// connection"; under permissive it closes the cmux root that Stop() does
		// not own. Either way the listener is down, which is all the refusal needs.
		_ = lis.Close()
		slog.Default().Error("grpcserver: mTLS self-probe failed; refusing to serve",
			"service", b.serviceName, "addr", lis.Addr().String(), "err", err)
		refusal := fmt.Errorf("grpcserver: service %q refused to serve: mTLS self-probe of %s failed: %w",
			b.serviceName, lis.Addr().String(), err)
		b.ready <- refusal
		return refusal
	}

	slog.Default().Info("grpcserver: mTLS self-probe passed",
		"service", b.serviceName, "addr", lis.Addr().String(), "spiffe_id", svidIDString(b.source))
	b.ready <- nil

	return <-serveErr
}

// serve runs one underlying server on its matched listener. cmux closing the
// listener surfaces as a serve error on shutdown, which is expected; it is
// logged at debug so it does not look like a failure.
func (b *Builder) serve(srv *grpc.Server, lis net.Listener, kind string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Default().Error("grpcserver: serve goroutine panicked",
				"service", b.serviceName, "listener", kind, "panic", r)
		}
	}()
	if err := srv.Serve(lis); err != nil {
		slog.Default().Debug("grpcserver: underlying server stopped",
			"service", b.serviceName, "listener", kind, "err", err)
	}
}

// GracefulStop gracefully stops every underlying server.
func (b *Builder) GracefulStop() {
	for _, srv := range b.servers {
		srv.GracefulStop()
	}
}

// Stop force-stops every underlying server.
func (b *Builder) Stop() {
	for _, srv := range b.servers {
		srv.Stop()
	}
}

// multiRegistrar fans grpc service registration out to every server.
type multiRegistrar struct {
	servers []*grpc.Server
}

// RegisterService is the single method of grpc.ServiceRegistrar; every
// generated RegisterXServer helper calls it. Fanning it out registers the impl
// into both transports.
func (m multiRegistrar) RegisterService(desc *grpc.ServiceDesc, impl any) {
	for _, s := range m.servers {
		s.RegisterService(desc, impl)
	}
}

// ensureHealth registers a default (all-SERVING) health service on srv unless
// the caller already registered one via Registrar. Registering the same
// service twice on a *grpc.Server panics, so the GetServiceInfo guard is
// required.
func ensureHealth(srv *grpc.Server) {
	if _, ok := srv.GetServiceInfo()["grpc.health.v1.Health"]; ok {
		return
	}
	h := health.NewServer()
	h.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(srv, h)
}
