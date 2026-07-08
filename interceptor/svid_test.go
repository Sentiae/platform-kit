package interceptor

import (
	"context"
	"testing"

	"google.golang.org/grpc"
)

// TestUnarySVID_PlaintextNoOp proves the SVID interceptor is a safe no-op on a
// plaintext call: no peer SVID is stashed, no error is returned, and the
// handler runs normally.
func TestUnarySVID_PlaintextNoOp(t *testing.T) {
	interceptor := UnarySVID()

	called := false
	handler := func(ctx context.Context, _ any) (any, error) {
		called = true
		if _, ok := SVIDFromContext(ctx); ok {
			t.Fatal("plaintext call must not carry a peer SVID")
		}
		return "ok", nil
	}

	resp, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/M"}, handler)
	if err != nil {
		t.Fatalf("unexpected error on plaintext call: %v", err)
	}
	if !called {
		t.Fatal("handler was not invoked")
	}
	if resp != "ok" {
		t.Fatalf("resp = %v, want ok", resp)
	}
}

// TestStreamSVID_PlaintextNoOp mirrors the unary case for streams.
func TestStreamSVID_PlaintextNoOp(t *testing.T) {
	interceptor := StreamSVID()

	called := false
	handler := func(_ any, ss grpc.ServerStream) error {
		called = true
		if _, ok := SVIDFromContext(ss.Context()); ok {
			t.Fatal("plaintext stream must not carry a peer SVID")
		}
		return nil
	}

	ss := &fakeServerStream{ctx: context.Background()}
	if err := interceptor(nil, ss, &grpc.StreamServerInfo{FullMethod: "/svc/S"}, handler); err != nil {
		t.Fatalf("unexpected error on plaintext stream: %v", err)
	}
	if !called {
		t.Fatal("stream handler was not invoked")
	}
}

// TestSVIDFromContext_Empty confirms the getter reports absence on a bare ctx.
func TestSVIDFromContext_Empty(t *testing.T) {
	if _, ok := SVIDFromContext(context.Background()); ok {
		t.Fatal("bare context must not carry a peer SVID")
	}
}
