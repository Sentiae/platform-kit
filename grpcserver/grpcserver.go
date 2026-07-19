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
// FAIL-CLOSED (D-162a L2): a security posture is never selected by the ABSENCE
// of a value. "permissive" is the DECLARED escape hatch — a service that opts
// into permissive tolerates plaintext by design, so a SPIRE hiccup (nil Source)
// degrades it to plaintext-only rather than wedging it. "strict" means strict:
// a strict service with no SVID source REFUSES TO SERVE rather than silently
// serving unencrypted traffic while claiming to require mutual TLS. New records
// that misconfiguration as a build error and Serve returns it before opening
// the listener, so nothing insecure is ever served.
//
// If identity-service or permission-service must stay up through a SPIRE
// outage, the honest mechanism is to run them "permissive" — never to let
// "strict" secretly mean plaintext.
package grpcserver

import (
	"fmt"
	"log/slog"
	"net"

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

	// Source is the SPIFFE X509 source used for the mTLS server. When nil under
	// "permissive" the builder degrades to plaintext-only; when nil under
	// "strict" the builder refuses to serve (see package doc).
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

	// buildErr, when non-nil, marks a fail-closed-invalid configuration (strict
	// mTLS required but no SVID source). No server is built; Serve returns this
	// error before opening the listener so nothing insecure is ever served.
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
// New does not panic. Fail-closed (D-162a L2): "strict" with a nil Source is an
// invalid configuration — New builds NO server and records a build error naming
// the service and the problem; Serve returns that error before opening the
// listener. "permissive" with a nil Source is the declared escape hatch and
// degrades to plaintext-only (see package doc).
func New(cfg Config, opts ...grpc.ServerOption) *Builder {
	b := &Builder{serviceName: cfg.ServiceName}

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

	// FAIL-CLOSED: strict mTLS required but no SVID source. Refuse to serve
	// rather than silently serving plaintext under a "strict" posture.
	if mode == config.MTLSModeStrict && cfg.Source == nil {
		b.buildErr = fmt.Errorf("grpcserver: service %q configured for strict mTLS but no SPIFFE/SVID source is available; refusing to serve (run in permissive mode if plaintext is acceptable during a SPIRE outage)", cfg.ServiceName)
		slog.Default().Error("grpcserver: strict mTLS required but SPIFFE source unavailable; refusing to serve",
			"service", cfg.ServiceName, "mode", mode)
		return b
	}

	// Degrade permissive with no source to plaintext-only (the declared escape
	// hatch — a SPIRE hiccup must not wedge a service that opted into it).
	if mode == config.MTLSModePermissive && cfg.Source == nil {
		slog.Default().Warn("grpcserver: permissive mTLS requested but SPIFFE source unavailable; degrading to plaintext-only",
			"service", cfg.ServiceName, "mode", mode)
		mode = config.MTLSModeOff
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

// Serve registers reflection (and a default health service if the caller did
// not already register one) on each underlying server, then serves. With one
// server it serves directly; with two it multiplexes plaintext and TLS on the
// single listener via cmux. Serve blocks until the listener is closed.
func (b *Builder) Serve(lis net.Listener) error {
	// Fail-closed: a poisoned builder (strict mTLS with no SVID source) never
	// serves. Return before touching the listener so nothing insecure is served.
	if b.buildErr != nil {
		return b.buildErr
	}

	for _, srv := range b.servers {
		ensureHealth(srv)
		reflection.Register(srv)
	}

	if len(b.servers) == 1 {
		return b.servers[0].Serve(lis)
	}

	// Two servers: cmux routes TLS handshakes to the mTLS server and everything
	// else to the plaintext server.
	m := cmux.New(lis)
	tlsL := m.Match(cmux.TLS())
	plainL := m.Match(cmux.Any())

	go b.serve(b.mtls, tlsL, "mtls")
	go b.serve(b.plain, plainL, "plain")

	return m.Serve()
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
