package grpcserver

import (
	"context"
	"net"
	"testing"

	"github.com/google/uuid"
	"github.com/sentiae/platform-kit/config"
	"github.com/sentiae/platform-kit/grpcclient"
	"github.com/sentiae/platform-kit/middleware"
	"github.com/sentiae/platform-kit/tenant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// echoActiveOrg is the /test.Prop/Echo unary handler: it returns the active org
// the propagation interceptor stamped on ctx (empty when none), letting the test
// observe whether org survived the client→server hop.
func echoActiveOrg(_ any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	handler := func(ctx context.Context, _ any) (any, error) {
		if org, ok := tenant.ActiveOrgFromContext(ctx); ok {
			return wrapperspb.String(org.String()), nil
		}
		return wrapperspb.String(""), nil
	}
	if interceptor == nil {
		return handler(ctx, in)
	}
	return interceptor(ctx, in, &grpc.UnaryServerInfo{FullMethod: "/test.Prop/Echo"}, handler)
}

var propServiceDesc = grpc.ServiceDesc{
	ServiceName: "test.Prop",
	HandlerType: (*any)(nil),
	Methods:     []grpc.MethodDesc{{MethodName: "Echo", Handler: echoActiveOrg}},
}

// injectPrincipal is a server interceptor standing in for the Auth interceptor:
// it establishes p as the request Principal so the propagation interceptor
// (appended innermost, after this) can re-verify an asserted org against it.
func injectPrincipal(p tenant.Principal) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(tenant.ContextWithPrincipal(ctx, p), req)
	}
}

// TestPropagation_RoundTrip proves a real grpcclient.Dial → grpcserver.New hop:
// an org set on the client ctx survives to the server as a stamped active org
// when the caller is authorized, and a foreign org is rejected PermissionDenied.
func TestPropagation_RoundTrip(t *testing.T) {
	orgA := uuid.New()
	orgB := uuid.New()

	// Server principal: a user who is a member of orgA ONLY.
	memberOfA := tenant.Principal{Claims: &middleware.Claims{
		Subject: uuid.NewString(),
		Scopes:  []string{"org:" + orgA.String()},
	}}

	lis := bufconn.Listen(1024 * 1024)
	b := New(
		Config{Mode: config.MTLSModeOff, ServiceName: "prop-test"},
		grpc.ChainUnaryInterceptor(injectPrincipal(memberOfA)),
	)
	b.Registrar().RegisterService(&propServiceDesc, struct{}{})
	go func() { _ = b.Serve(lis) }()
	t.Cleanup(b.GracefulStop)

	conn, err := grpcclient.Dial(
		context.Background(),
		grpcclient.Config{Endpoint: "passthrough:///bufnet", Mode: config.MTLSModeOff},
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Authorized hop: client acts in orgA (of which the server principal is a
	// member) → propagation stamps it and the handler echoes orgA back.
	t.Run("org survives the hop", func(t *testing.T) {
		reply := new(wrapperspb.StringValue)
		err := conn.Invoke(tenant.WithActiveOrg(context.Background(), orgA), "/test.Prop/Echo", &emptypb.Empty{}, reply)
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if reply.GetValue() != orgA.String() {
			t.Fatalf("echoed org = %q, want %q (org did not survive the hop)", reply.GetValue(), orgA)
		}
	})

	// Forged hop: client asserts orgB, but the server principal is not a member
	// of orgB → the inbound interceptor rejects with PermissionDenied.
	t.Run("forged org rejected", func(t *testing.T) {
		reply := new(wrapperspb.StringValue)
		err := conn.Invoke(tenant.WithActiveOrg(context.Background(), orgB), "/test.Prop/Echo", &emptypb.Empty{}, reply)
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("invoke err = %v, want PermissionDenied for a forged org", err)
		}
	})
}
