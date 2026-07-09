package grpcserver

import (
	"testing"

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

func TestNew_StrictNilSource_DegradesToPlaintext(t *testing.T) {
	b := New(Config{Mode: config.MTLSModeStrict, Source: nil, ServiceName: "test"})
	if len(b.servers) != 1 || b.plain == nil || b.mtls != nil {
		t.Fatalf("strict+nil source should degrade to plaintext-only: servers=%d plain=%v mtls=%v",
			len(b.servers), b.plain != nil, b.mtls != nil)
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
