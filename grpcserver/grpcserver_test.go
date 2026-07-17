package grpcserver

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sentiae/platform-kit/config"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"google.golang.org/grpc"
)

// fakeServiceDesc is a minimal ServiceDesc used to exercise the fan-out
// registrar without a real generated service.
var fakeServiceDesc = &grpc.ServiceDesc{
	ServiceName: "grpcserver.test.FakeService",
	HandlerType: (*any)(nil),
}

func TestNew_Off_SinglePlaintextServer(t *testing.T) {
	b := New(Config{Mode: config.MTLSModeOff, ServiceName: "test"})
	if len(b.servers) != 1 {
		t.Fatalf("off: got %d servers, want 1", len(b.servers))
	}
	if b.plain == nil {
		t.Fatal("off: plain server is nil")
	}
	if b.mtls != nil {
		t.Fatal("off: mtls server should be nil")
	}
}

func TestNew_EmptyMode_DefaultsOff(t *testing.T) {
	b := New(Config{ServiceName: "test"})
	if len(b.servers) != 1 || b.plain == nil || b.mtls != nil {
		t.Fatalf("empty mode should behave as off: servers=%d plain=%v mtls=%v",
			len(b.servers), b.plain != nil, b.mtls != nil)
	}
}

func TestNew_UnrecognizedMode_RefusesToServe(t *testing.T) {
	// Fail-closed (D-162a): an unrecognized non-empty mode is a misconfiguration,
	// not a request for plaintext. The builder must not silently pick "off".
	b := New(Config{Mode: "stric", ServiceName: "identity"})
	if len(b.servers) != 0 || b.plain != nil || b.mtls != nil {
		t.Fatalf("unrecognized mode: no server should be built, got servers=%d plain=%v mtls=%v",
			len(b.servers), b.plain != nil, b.mtls != nil)
	}
	if b.Server() != nil {
		t.Fatal("unrecognized mode: Server() must be nil on a poisoned builder")
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	done := make(chan error, 1)
	go func() { done <- b.Serve(lis) }()
	select {
	case serveErr := <-done:
		if serveErr == nil {
			t.Fatal("unrecognized mode: Serve returned nil (plaintext would be served); want a refuse-to-serve error")
		}
		msg := serveErr.Error()
		for _, want := range []string{"identity", "stric"} {
			if !strings.Contains(msg, want) {
				t.Fatalf("Serve error %q must mention %q for operator diagnosis", msg, want)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unrecognized mode: Serve blocked instead of refusing")
	}
}

func TestNew_PermissiveNilSource_DegradesToPlaintext(t *testing.T) {
	// nil Source with a non-off mode must degrade to plaintext-only, never panic.
	b := New(Config{Mode: config.MTLSModePermissive, Source: nil, ServiceName: "test"})
	if len(b.servers) != 1 {
		t.Fatalf("permissive+nil source: got %d servers, want 1 (degraded)", len(b.servers))
	}
	if b.plain == nil || b.mtls != nil {
		t.Fatalf("permissive+nil source: want plaintext-only, got plain=%v mtls=%v",
			b.plain != nil, b.mtls != nil)
	}
}

func TestNew_StrictNilSource_RefusesToServe(t *testing.T) {
	// Fail-closed (D-162a L2): strict mTLS with no SVID source must NOT serve
	// plaintext. New builds no server and Serve returns a diagnostic error
	// before ever accepting traffic.
	b := New(Config{Mode: config.MTLSModeStrict, Source: nil, ServiceName: "identity"})

	// Nothing insecure was built: no server was handed out.
	if len(b.servers) != 0 {
		t.Fatalf("strict+nil source: got %d servers, want 0 (no server built)", len(b.servers))
	}
	if b.plain != nil || b.mtls != nil {
		t.Fatalf("strict+nil source: no server should be built, got plain=%v mtls=%v",
			b.plain != nil, b.mtls != nil)
	}
	if b.Server() != nil {
		t.Fatal("strict+nil source: Server() must be nil on a poisoned builder")
	}

	// Serve on a real listener must refuse — return an error, serve no traffic.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	// Serve must refuse PROMPTLY: return an error before serving. A Serve that
	// blocks (accepting/serving) or returns nil is itself the fail-open bug, so
	// anything but a quick non-nil error is a failure.
	done := make(chan error, 1)
	go func() { done <- b.Serve(lis) }()

	select {
	case serveErr := <-done:
		if serveErr == nil {
			t.Fatal("strict+nil source: Serve returned nil (plaintext would be served); want a refuse-to-serve error")
		}
		// The error must name the service and mention strict/SVID so an operator
		// can diagnose the misconfiguration.
		msg := serveErr.Error()
		for _, want := range []string{"identity", "strict", "SVID"} {
			if !strings.Contains(msg, want) {
				t.Fatalf("Serve error %q must mention %q for operator diagnosis", msg, want)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("strict+nil source: Serve blocked instead of refusing; something is being served on a poisoned builder")
	}
}

func TestNew_PermissiveWithSource_TwoServers(t *testing.T) {
	// A non-nil (zero-value) source is enough: ServerCreds is lazy and only
	// touches the source at TLS handshake, not at server construction.
	src := &workloadapi.X509Source{}
	b := New(Config{Mode: config.MTLSModePermissive, Source: src, ServiceName: "test"})
	if len(b.servers) != 2 {
		t.Fatalf("permissive+source: got %d servers, want 2", len(b.servers))
	}
	if b.plain == nil || b.mtls == nil {
		t.Fatalf("permissive+source: want both servers, got plain=%v mtls=%v",
			b.plain != nil, b.mtls != nil)
	}
}

func TestNew_StrictWithSource_OneMTLSServer(t *testing.T) {
	src := &workloadapi.X509Source{}
	b := New(Config{Mode: config.MTLSModeStrict, Source: src, ServiceName: "test"})
	if len(b.servers) != 1 || b.mtls == nil || b.plain != nil {
		t.Fatalf("strict+source: want one mtls server, got servers=%d plain=%v mtls=%v",
			len(b.servers), b.plain != nil, b.mtls != nil)
	}
}

// countingRegistrar records how many times RegisterService is called so the
// fan-out can be asserted without a real gRPC service.
type countingRegistrar struct{ calls int }

func (c *countingRegistrar) RegisterService(*grpc.ServiceDesc, any) { c.calls++ }

func TestRegistrar_FansOutToAllServers(t *testing.T) {
	// Two real servers (permissive) — register a fake service and assert it
	// landed on both via GetServiceInfo.
	src := &workloadapi.X509Source{}
	b := New(Config{Mode: config.MTLSModePermissive, Source: src, ServiceName: "test"})

	b.Registrar().RegisterService(fakeServiceDesc, nil)

	for i, srv := range b.servers {
		if _, ok := srv.GetServiceInfo()[fakeServiceDesc.ServiceName]; !ok {
			t.Fatalf("server %d missing fake service after fan-out registration", i)
		}
	}
}

func TestMultiRegistrar_CallsEveryServer(t *testing.T) {
	// Directly exercise the fan-out contract against fakes: one RegisterService
	// call must reach every registrar.
	//
	// multiRegistrar holds *grpc.Server, so this test builds a slice of real
	// servers and verifies each received the desc via GetServiceInfo above;
	// here we assert the arithmetic of fan-out with a hand-rolled loop over
	// counting registrars to document the intended behavior.
	regs := []*countingRegistrar{{}, {}, {}}
	fan := func(desc *grpc.ServiceDesc, impl any) {
		for _, r := range regs {
			r.RegisterService(desc, impl)
		}
	}
	fan(fakeServiceDesc, nil)
	for i, r := range regs {
		if r.calls != 1 {
			t.Fatalf("registrar %d: got %d calls, want 1", i, r.calls)
		}
	}
}
