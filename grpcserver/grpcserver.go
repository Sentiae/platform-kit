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
// SAFETY: identity-service and permission-service sit under the whole stack. A
// SPIRE hiccup (nil Source when Mode != off) must never wedge them, so the
// builder DEGRADES to plaintext-only and logs a warning rather than failing.
package grpcserver

import (
	"log/slog"
	"net"

	"github.com/sentiae/platform-kit/config"
	"github.com/sentiae/platform-kit/spiffe"
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

	// Source is the SPIFFE X509 source used for the mTLS server. When nil and
	// Mode != off the builder degrades to plaintext-only (see package doc).
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
}

// New builds the server(s) for the given mode. The variadic opts are the
// service's base server options (its ChainUnary/ChainStream interceptors and
// any others); transport credentials are added by New per mode. New never
// fails: if Mode != off but Source is nil, it degrades to plaintext-only.
func New(cfg Config, opts ...grpc.ServerOption) *Builder {
	b := &Builder{serviceName: cfg.ServiceName}

	mode := cfg.Mode
	if mode == "" {
		mode = config.MTLSModeOff
	}

	// Degrade any non-off mode with no source to plaintext-only.
	if mode != config.MTLSModeOff && cfg.Source == nil {
		slog.Default().Warn("grpcserver: mTLS mode requested but SPIFFE source unavailable; degrading to plaintext-only",
			"service", cfg.ServiceName, "mode", mode)
		mode = config.MTLSModeOff
	}

	switch mode {
	case config.MTLSModePermissive:
		b.plain = grpc.NewServer(opts...)
		mtlsOpts := append([]grpc.ServerOption{grpc.Creds(spiffe.ServerCreds(cfg.Source))}, opts...)
		b.mtls = grpc.NewServer(mtlsOpts...)
		b.servers = []*grpc.Server{b.plain, b.mtls}
	case config.MTLSModeStrict:
		mtlsOpts := append([]grpc.ServerOption{grpc.Creds(spiffe.ServerCreds(cfg.Source))}, opts...)
		b.mtls = grpc.NewServer(mtlsOpts...)
		b.servers = []*grpc.Server{b.mtls}
	default: // off
		b.plain = grpc.NewServer(opts...)
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
// (modes off/permissive) else the mTLS server (strict); nil only if no server
// was built. Do NOT call Serve on it directly — use Builder.Serve so every
// configured transport is served.
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
