package interceptor

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestUnaryRecovery_NoPanic(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := UnaryRecovery(l)
	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	resp, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test/Method"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected resp 'ok', got %v", resp)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no log output, got: %s", buf.String())
	}
}

func TestUnaryRecovery_CatchesPanic(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := UnaryRecovery(l)
	handler := func(ctx context.Context, req any) (any, error) {
		panic("test panic")
	}

	resp, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test/Panic"}, handler)
	if resp != nil {
		t.Fatalf("expected nil resp, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal code, got %v", st.Code())
	}

	logOutput := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("panic recovered")) {
		t.Fatalf("expected 'panic recovered' in log, got: %s", logOutput)
	}
	if !bytes.Contains(buf.Bytes(), []byte("/test/Panic")) {
		t.Fatalf("expected method name in log, got: %s", logOutput)
	}
}

func TestUnaryRecovery_ErrorPanic(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := UnaryRecovery(l)
	handler := func(ctx context.Context, req any) (any, error) {
		panic(errForTesting("boom"))
	}

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test/ErrorPanic"}, handler)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %v", st.Code())
	}
}

type errForTesting string

func (e errForTesting) Error() string { return string(e) }

func TestStreamRecovery_NoPanic(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := StreamRecovery(l)
	handler := func(srv any, stream grpc.ServerStream) error {
		return nil
	}

	ss := &fakeServerStream{ctx: context.Background()}
	err := interceptor(nil, ss, &grpc.StreamServerInfo{FullMethod: "/test/Stream"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStreamRecovery_CatchesPanic(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := StreamRecovery(l)
	handler := func(srv any, stream grpc.ServerStream) error {
		panic("stream panic")
	}

	ss := &fakeServerStream{ctx: context.Background()}
	err := interceptor(nil, ss, &grpc.StreamServerInfo{FullMethod: "/test/StreamPanic"}, handler)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal code, got %v", st.Code())
	}
	if !bytes.Contains(buf.Bytes(), []byte("stream panic")) {
		t.Fatalf("expected panic value in log, got: %s", buf.String())
	}
}

// fakeServerStream is a minimal grpc.ServerStream implementation for tests.
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context {
	return f.ctx
}
