package interceptor

import (
	"bytes"
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUnaryLogging_Success(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := UnaryLogging(l)
	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	resp, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test/Success"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected 'ok', got %v", resp)
	}

	logOutput := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("rpc completed")) {
		t.Fatalf("expected 'rpc completed', got: %s", logOutput)
	}
	if !bytes.Contains(buf.Bytes(), []byte("/test/Success")) {
		t.Fatalf("expected method in log, got: %s", logOutput)
	}
	if !bytes.Contains(buf.Bytes(), []byte("OK")) {
		t.Fatalf("expected status OK in log, got: %s", logOutput)
	}
	if !bytes.Contains(buf.Bytes(), []byte("duration")) {
		t.Fatalf("expected duration in log, got: %s", logOutput)
	}
}

func TestUnaryLogging_Error(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := UnaryLogging(l)
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, status.Error(codes.NotFound, "not found")
	}

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test/NotFound"}, handler)
	if err == nil {
		t.Fatal("expected error")
	}

	logOutput := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("NotFound")) {
		t.Fatalf("expected NotFound code in log, got: %s", logOutput)
	}
}

func TestUnaryLogging_PropagatesError(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := UnaryLogging(l)
	expected := status.Error(codes.InvalidArgument, "bad request")
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, expected
	}

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test/Err"}, handler)
	if err != expected {
		t.Fatalf("expected error to propagate, got %v", err)
	}
}

func TestStreamLogging_Success(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := StreamLogging(l)
	handler := func(srv any, stream grpc.ServerStream) error {
		return nil
	}

	ss := &fakeServerStream{ctx: context.Background()}
	err := interceptor(nil, ss, &grpc.StreamServerInfo{FullMethod: "/test/StreamSuccess"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logOutput := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("rpc completed")) {
		t.Fatalf("expected 'rpc completed', got: %s", logOutput)
	}
	if !bytes.Contains(buf.Bytes(), []byte("/test/StreamSuccess")) {
		t.Fatalf("expected method in log, got: %s", logOutput)
	}
}

func TestStreamLogging_Error(t *testing.T) {
	var buf bytes.Buffer
	l := testLogger(&buf)

	interceptor := StreamLogging(l)
	handler := func(srv any, stream grpc.ServerStream) error {
		return status.Error(codes.PermissionDenied, "denied")
	}

	ss := &fakeServerStream{ctx: context.Background()}
	err := interceptor(nil, ss, &grpc.StreamServerInfo{FullMethod: "/test/StreamErr"}, handler)
	if err == nil {
		t.Fatal("expected error")
	}

	if !bytes.Contains(buf.Bytes(), []byte("PermissionDenied")) {
		t.Fatalf("expected PermissionDenied in log, got: %s", buf.String())
	}
}
