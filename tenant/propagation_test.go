package tenant

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/sentiae/platform-kit/logger"
	"github.com/sentiae/platform-kit/middleware"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// userClaims builds a user principal whose JWT proves membership of exactly the
// given orgs (org:<uuid> scopes) with the given subject.
func userClaims(subject string, orgs ...uuid.UUID) Principal {
	scopes := make([]string, 0, len(orgs))
	for _, o := range orgs {
		scopes = append(scopes, "org:"+o.String())
	}
	return Principal{Claims: &middleware.Claims{Subject: subject, Scopes: scopes}}
}

func outgoingMD(t *testing.T, ctx context.Context) metadata.MD {
	t.Helper()
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return metadata.MD{}
	}
	return md
}

// TestPropagateOutgoing_Org drives the org outcomes of the outbound fill.
func TestPropagateOutgoing_Org(t *testing.T) {
	uid := uuid.New()
	tests := []struct {
		name    string
		ctx     func() context.Context
		wantOrg string // "" = expect no x-organization-id appended
	}{
		{
			name:    "active org propagated",
			ctx:     func() context.Context { return WithActiveOrg(context.Background(), orgA) },
			wantOrg: orgA.String(),
		},
		{
			name:    "system org propagated",
			ctx:     func() context.Context { return WithSystemOrg(context.Background(), orgB) },
			wantOrg: orgB.String(),
		},
		{
			name: "single claims org propagated",
			ctx: func() context.Context {
				return ContextWithPrincipal(context.Background(), userClaims(uid.String(), orgA))
			},
			wantOrg: orgA.String(),
		},
		{
			name: "multi claims org absent",
			ctx: func() context.Context {
				return ContextWithPrincipal(context.Background(), userClaims(uid.String(), orgA, orgB))
			},
			wantOrg: "",
		},
		{
			name:    "none absent",
			ctx:     func() context.Context { return context.Background() },
			wantOrg: "",
		},
		{
			name: "system context withholds org even when active set",
			ctx: func() context.Context {
				return WithSystemContext(WithActiveOrg(context.Background(), orgA))
			},
			wantOrg: "",
		},
		{
			name: "existing org metadata inherited not clobbered",
			ctx: func() context.Context {
				ctx := WithActiveOrg(context.Background(), orgA)
				return metadata.AppendToOutgoingContext(ctx, MDOrganizationID, orgB.String())
			},
			wantOrg: orgB.String(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md := outgoingMD(t, propagateOutgoing(tt.ctx()))
			got := md.Get(MDOrganizationID)
			if tt.wantOrg == "" {
				if len(got) != 0 {
					t.Fatalf("expected no org, got %v", got)
				}
				return
			}
			if len(got) != 1 || got[0] != tt.wantOrg {
				t.Fatalf("org = %v, want %s", got, tt.wantOrg)
			}
		})
	}
}

// TestPropagateOutgoing_UserRequestAgent covers the non-org keys + the
// never-clobber guarantee for auth-bearing metadata.
func TestPropagateOutgoing_UserRequestAgent(t *testing.T) {
	uid := uuid.New()

	t.Run("verified subject propagated", func(t *testing.T) {
		ctx := ContextWithPrincipal(context.Background(), userClaims(uid.String(), orgA))
		md := outgoingMD(t, propagateOutgoing(ctx))
		if got := md.Get(MDUserID); len(got) != 1 || got[0] != uid.String() {
			t.Fatalf("user id = %v, want %s", got, uid)
		}
	})

	t.Run("propagated user id relayed multi-hop for service principal", func(t *testing.T) {
		ctx := ContextWithPrincipal(context.Background(), Principal{ServiceSVID: "spiffe://sentiae.io/svc/foundry"})
		ctx = context.WithValue(ctx, propagatedUserCtxKey{}, uid)
		md := outgoingMD(t, propagateOutgoing(ctx))
		if got := md.Get(MDUserID); len(got) != 1 || got[0] != uid.String() {
			t.Fatalf("relayed user id = %v, want %s", got, uid)
		}
	})

	t.Run("request id agent approver appended", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), logger.RequestIDKey, "req-9")
		ctx = WithAgentID(ctx, "agent-7")
		ctx = WithApproverID(ctx, "approver-3")
		md := outgoingMD(t, propagateOutgoing(ctx))
		if got := md.Get(MDRequestID); len(got) != 1 || got[0] != "req-9" {
			t.Fatalf("request id = %v", got)
		}
		if got := md.Get(MDAgentID); len(got) != 1 || got[0] != "agent-7" {
			t.Fatalf("agent id = %v", got)
		}
		if got := md.Get(MDApproverID); len(got) != 1 || got[0] != "approver-3" {
			t.Fatalf("approver id = %v", got)
		}
	})

	t.Run("existing authorization and api-key never dropped", func(t *testing.T) {
		ctx := metadata.AppendToOutgoingContext(context.Background(),
			"authorization", "Bearer tok", "x-api-key", "secret")
		ctx = WithActiveOrg(ctx, orgA)
		md := outgoingMD(t, propagateOutgoing(ctx))
		if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer tok" {
			t.Fatalf("authorization = %v; must be preserved", got)
		}
		if got := md.Get("x-api-key"); len(got) != 1 || got[0] != "secret" {
			t.Fatalf("x-api-key = %v; must be preserved", got)
		}
		if got := md.Get(MDOrganizationID); len(got) != 1 || got[0] != orgA.String() {
			t.Fatalf("org = %v, want %s", got, orgA)
		}
	})
}

// TestUnaryClientPropagation_SkipsHealth proves infra methods carry no identity,
// and a normal method does fill the org.
func TestUnaryClientPropagation_SkipsHealth(t *testing.T) {
	intercept := UnaryClientPropagation()
	cases := []struct {
		name    string
		method  string
		wantOrg bool
	}{
		{"health skipped", "/grpc.health.v1.Health/Check", false},
		{"reflection skipped", "/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", false},
		{"normal method filled", "/work.v1.TaskService/CreateTask", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen metadata.MD
			invoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
				seen, _ = metadata.FromOutgoingContext(ctx)
				return nil
			}
			if err := intercept(WithActiveOrg(context.Background(), orgA), tc.method, nil, nil, nil, invoker); err != nil {
				t.Fatalf("err = %v", err)
			}
			gotOrg := len(seen.Get(MDOrganizationID)) == 1
			if gotOrg != tc.wantOrg {
				t.Fatalf("org present = %v, want %v (md=%v)", gotOrg, tc.wantOrg, seen)
			}
		})
	}
}

// inboundParse is a small helper running the inbound logic against an incoming
// metadata set + an optional principal.
func inboundParse(t *testing.T, p *Principal, md metadata.MD) (context.Context, error) {
	t.Helper()
	ctx := context.Background()
	if p != nil {
		ctx = ContextWithPrincipal(ctx, *p)
	}
	ctx = metadata.NewIncomingContext(ctx, md)
	return inboundPropagation(ctx, "/work.v1.TaskService/CreateTask")
}

func TestInboundPropagation_Org(t *testing.T) {
	uid := uuid.New()

	// Grants + strict for the headless SVID cases.
	const grantedSVID = "spiffe://sentiae.io/svc/foundry"
	const ungrantedSVID = "spiffe://sentiae.io/svc/portal"

	t.Run("no metadata passes through", func(t *testing.T) {
		ctx, err := inboundPropagation(context.Background(), "/x/Y")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if _, ok := ActiveOrgFromContext(ctx); ok {
			t.Fatal("no active org expected")
		}
	})

	t.Run("no propagation keys passes through", func(t *testing.T) {
		_, err := inboundParse(t, nil, metadata.Pairs("other-key", "v"))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("user asserts own org stamped", func(t *testing.T) {
		p := userClaims(uid.String(), orgA)
		ctx, err := inboundParse(t, &p, metadata.Pairs(MDOrganizationID, orgA.String()))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got, ok := ActiveOrgFromContext(ctx); !ok || got != orgA {
			t.Fatalf("active org = %v %v, want %s stamped", got, ok, orgA)
		}
	})

	t.Run("user asserts foreign org rejected", func(t *testing.T) {
		p := userClaims(uid.String(), orgB)
		_, err := inboundParse(t, &p, metadata.Pairs(MDOrganizationID, orgA.String()))
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("err = %v, want PermissionDenied", err)
		}
	})

	t.Run("SVID grant + strict stamped", func(t *testing.T) {
		prevGrants := defaultServiceGrants
		SetServiceGrants(NewServiceGrants(map[string]ServiceGrant{grantedSVID: {CrossOrg: true}}))
		t.Cleanup(func() { SetServiceGrants(prevGrants) })
		prevStrict := meshSVIDAuthzStrict
		SetMeshSVIDAuthzStrict(true)
		t.Cleanup(func() { SetMeshSVIDAuthzStrict(prevStrict) })

		p := Principal{ServiceSVID: grantedSVID}
		ctx, err := inboundParse(t, &p, metadata.Pairs(MDOrganizationID, orgA.String()))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got, ok := ActiveOrgFromContext(ctx); !ok || got != orgA {
			t.Fatalf("active org = %v %v, want stamped", got, ok)
		}
	})

	t.Run("SVID without grant + strict rejected", func(t *testing.T) {
		prevGrants := defaultServiceGrants
		SetServiceGrants(NewServiceGrants(map[string]ServiceGrant{grantedSVID: {CrossOrg: true}}))
		t.Cleanup(func() { SetServiceGrants(prevGrants) })
		prevStrict := meshSVIDAuthzStrict
		SetMeshSVIDAuthzStrict(true)
		t.Cleanup(func() { SetMeshSVIDAuthzStrict(prevStrict) })

		p := Principal{ServiceSVID: ungrantedSVID}
		_, err := inboundParse(t, &p, metadata.Pairs(MDOrganizationID, orgA.String()))
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("err = %v, want PermissionDenied", err)
		}
	})

	t.Run("pre-strict service stamped", func(t *testing.T) {
		prevStrict := meshSVIDAuthzStrict
		SetMeshSVIDAuthzStrict(false)
		t.Cleanup(func() { SetMeshSVIDAuthzStrict(prevStrict) })

		p := Principal{ServiceSVID: ungrantedSVID}
		ctx, err := inboundParse(t, &p, metadata.Pairs(MDOrganizationID, orgA.String()))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got, ok := ActiveOrgFromContext(ctx); !ok || got != orgA {
			t.Fatalf("active org = %v %v, want stamped pre-strict", got, ok)
		}
	})

	t.Run("no principal org asserted passes unstamped", func(t *testing.T) {
		ctx, err := inboundParse(t, nil, metadata.Pairs(MDOrganizationID, orgA.String()))
		if err != nil {
			t.Fatalf("err = %v, want nil (unstamped pass)", err)
		}
		if _, ok := ActiveOrgFromContext(ctx); ok {
			t.Fatal("unverified org must NOT be stamped")
		}
	})

	t.Run("duplicate org invalid argument", func(t *testing.T) {
		md := metadata.MD{MDOrganizationID: []string{orgA.String(), orgB.String()}}
		p := userClaims(uid.String(), orgA)
		_, err := inboundParse(t, &p, md)
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("err = %v, want InvalidArgument", err)
		}
	})

	t.Run("bad uuid invalid argument", func(t *testing.T) {
		p := userClaims(uid.String(), orgA)
		_, err := inboundParse(t, &p, metadata.Pairs(MDOrganizationID, "not-a-uuid"))
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("err = %v, want InvalidArgument", err)
		}
	})

	t.Run("already scoped precedence not overridden", func(t *testing.T) {
		// Active org already resolved to orgA by a prior interceptor; incoming
		// asserts orgB. The prior scope wins and no rejection occurs.
		p := userClaims(uid.String(), orgA)
		ctx := ContextWithPrincipal(WithActiveOrg(context.Background(), orgA), p)
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(MDOrganizationID, orgB.String()))
		out, err := inboundPropagation(ctx, "/x/Y")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got, _ := ActiveOrgFromContext(out); got != orgA {
			t.Fatalf("active org = %s, want orgA (unchanged)", got)
		}
	})
}

func TestInboundPropagation_UserAndPassthrough(t *testing.T) {
	claimSub := uuid.New()
	wireUser := uuid.New()

	t.Run("x-user-id ignored when claims present", func(t *testing.T) {
		p := userClaims(claimSub.String(), orgA)
		ctx, err := inboundParse(t, &p, metadata.Pairs(MDUserID, wireUser.String()))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if _, ok := PropagatedUserID(ctx); ok {
			t.Fatal("x-user-id must be ignored when verified claims present")
		}
	})

	t.Run("x-user-id stored for service principal", func(t *testing.T) {
		p := Principal{ServiceSVID: "spiffe://sentiae.io/svc/foundry"}
		ctx, err := inboundParse(t, &p, metadata.Pairs(MDUserID, wireUser.String()))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got, ok := PropagatedUserID(ctx); !ok || got != wireUser {
			t.Fatalf("propagated user = %v %v, want %s", got, ok, wireUser)
		}
	})

	t.Run("request id and agent approver passthrough", func(t *testing.T) {
		p := Principal{ServiceSVID: "spiffe://sentiae.io/svc/foundry"}
		md := metadata.Pairs(
			MDRequestID, "req-abc",
			MDAgentID, "agent-x",
			MDApproverID, "approver-y",
		)
		ctx, err := inboundParse(t, &p, md)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got, ok := ctx.Value(logger.RequestIDKey).(string); !ok || got != "req-abc" {
			t.Fatalf("request id = %v", got)
		}
		if got, ok := AgentIDFromContext(ctx); !ok || got != "agent-x" {
			t.Fatalf("agent id = %v", got)
		}
		if got, ok := ApproverIDFromContext(ctx); !ok || got != "approver-y" {
			t.Fatalf("approver id = %v", got)
		}
	})
}

// TestStreamInboundPropagation_Mirror proves the stream interceptor applies the
// same stamping via the wrapped stream context.
func TestStreamInboundPropagation_Mirror(t *testing.T) {
	uid := uuid.New()
	p := userClaims(uid.String(), orgA)
	ctx := ContextWithPrincipal(context.Background(), p)
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(MDOrganizationID, orgA.String()))

	intercept := StreamInboundPropagation()
	var stampedOrg uuid.UUID
	var stampedOK bool
	info := &grpc.StreamServerInfo{FullMethod: "/work.v1.TaskService/StreamTasks"}
	err := intercept(nil, fakeServerStream{ctx: ctx}, info, func(srv any, ss grpc.ServerStream) error {
		stampedOrg, stampedOK = ActiveOrgFromContext(ss.Context())
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !stampedOK || stampedOrg != orgA {
		t.Fatalf("stream active org = %v %v, want %s stamped", stampedOrg, stampedOK, orgA)
	}
}

// fakeServerStream is a minimal grpc.ServerStream carrying a fixed context.
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f fakeServerStream) Context() context.Context { return f.ctx }

// TestInboundPropagation_SkipsInfraMethods is the D-411 guard: the SERVER
// interceptors must apply the same skipPropagation the client interceptors
// declare, so gRPC reflection and health never carry tenant identity. It is
// driven under the exact production shape that broke — strict SVID authz plus a
// METHOD-SCOPED grant, with an org header present — where reaching Rule 5 with
// the reflection full-method is a guaranteed PermissionDenied because no grant
// names a transport method.
//
// The reflection row deliberately uses the v1alpha package to prove the
// "/grpc.reflection." PREFIX match rather than an exact string, and reflection
// is driven through the STREAM interceptor too because ServerReflectionInfo is
// a bidi stream — that is the path `grpcurl list` actually takes.
//
// The last two rows are the over-broadness control in the other direction: a
// business RPC with an org header must still reach Rule 5 and be stamped when
// granted / denied when not, exactly as before.
func TestInboundPropagation_SkipsInfraMethods(t *testing.T) {
	const svid = "spiffe://sentiae.io/svc/codegen"
	const grantedMethod = "/work.v1.TaskService/CreateTask"
	const ungrantedMethod = "/work.v1.TaskService/DeleteTask"

	prevGrants := defaultServiceGrants
	SetServiceGrants(NewServiceGrants(map[string]ServiceGrant{
		svid: {CrossOrg: true, Methods: map[string]struct{}{grantedMethod: {}}},
	}))
	t.Cleanup(func() { SetServiceGrants(prevGrants) })
	prevStrict := meshSVIDAuthzStrict
	SetMeshSVIDAuthzStrict(true)
	t.Cleanup(func() { SetMeshSVIDAuthzStrict(prevStrict) })

	cases := []struct {
		name        string
		method      string
		wantCode    codes.Code
		wantStamped bool
	}{
		{"reflection v1alpha passes unstamped", "/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo", codes.OK, false},
		{"reflection v1 passes unstamped", "/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", codes.OK, false},
		{"health check passes unstamped", "/grpc.health.v1.Health/Check", codes.OK, false},
		{"granted business method still stamped", grantedMethod, codes.OK, true},
		{"ungranted business method still denied", ungrantedMethod, codes.PermissionDenied, false},
	}

	for _, tc := range cases {
		// The server-owned full-method is what CanActInOrg checks (Principal.Method
		// comes from grpc.Method(ctx) in production).
		p := Principal{ServiceSVID: svid, Method: tc.method}
		md := metadata.Pairs(MDOrganizationID, orgA.String())

		t.Run("unary/"+tc.name, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(ContextWithPrincipal(context.Background(), p), md)
			var stamped bool
			_, err := UnaryInboundPropagation()(ctx, nil,
				&grpc.UnaryServerInfo{FullMethod: tc.method},
				func(hctx context.Context, _ any) (any, error) {
					_, stamped = ActiveOrgFromContext(hctx)
					return nil, nil
				})
			if status.Code(err) != tc.wantCode {
				t.Fatalf("code = %v (err=%v), want %v", status.Code(err), err, tc.wantCode)
			}
			if stamped != tc.wantStamped {
				t.Fatalf("active org stamped = %v, want %v", stamped, tc.wantStamped)
			}
		})

		t.Run("stream/"+tc.name, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(ContextWithPrincipal(context.Background(), p), md)
			var stamped bool
			err := StreamInboundPropagation()(nil, fakeServerStream{ctx: ctx},
				&grpc.StreamServerInfo{FullMethod: tc.method},
				func(_ any, ss grpc.ServerStream) error {
					_, stamped = ActiveOrgFromContext(ss.Context())
					return nil
				})
			if status.Code(err) != tc.wantCode {
				t.Fatalf("code = %v (err=%v), want %v", status.Code(err), err, tc.wantCode)
			}
			if stamped != tc.wantStamped {
				t.Fatalf("active org stamped = %v, want %v", stamped, tc.wantStamped)
			}
		})
	}
}
