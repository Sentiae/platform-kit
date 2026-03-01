package interceptor

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUnaryMetrics_RecordsSuccess(t *testing.T) {
	m := &InMemoryMetrics{}

	interceptor := UnaryMetrics(m)
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

	records := m.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Method != "/test/Success" {
		t.Fatalf("expected method '/test/Success', got %q", records[0].Method)
	}
	if records[0].Code != "OK" {
		t.Fatalf("expected code 'OK', got %q", records[0].Code)
	}
	if records[0].Duration < 0 {
		t.Fatal("expected non-negative duration")
	}
}

func TestUnaryMetrics_RecordsError(t *testing.T) {
	m := &InMemoryMetrics{}

	interceptor := UnaryMetrics(m)
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, status.Error(codes.NotFound, "not found")
	}

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test/NotFound"}, handler)
	if err == nil {
		t.Fatal("expected error")
	}

	records := m.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Code != "NotFound" {
		t.Fatalf("expected code 'NotFound', got %q", records[0].Code)
	}
}

func TestStreamMetrics_RecordsSuccess(t *testing.T) {
	m := &InMemoryMetrics{}

	interceptor := StreamMetrics(m)
	handler := func(srv any, stream grpc.ServerStream) error {
		return nil
	}

	ss := &fakeServerStream{ctx: context.Background()}
	err := interceptor(nil, ss, &grpc.StreamServerInfo{FullMethod: "/test/StreamSuccess"}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	records := m.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Method != "/test/StreamSuccess" {
		t.Fatalf("expected method '/test/StreamSuccess', got %q", records[0].Method)
	}
}

func TestStreamMetrics_RecordsError(t *testing.T) {
	m := &InMemoryMetrics{}

	interceptor := StreamMetrics(m)
	handler := func(srv any, stream grpc.ServerStream) error {
		return status.Error(codes.Internal, "oops")
	}

	ss := &fakeServerStream{ctx: context.Background()}
	err := interceptor(nil, ss, &grpc.StreamServerInfo{FullMethod: "/test/StreamErr"}, handler)
	if err == nil {
		t.Fatal("expected error")
	}

	records := m.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Code != "Internal" {
		t.Fatalf("expected code 'Internal', got %q", records[0].Code)
	}
}

func TestInMemoryMetrics_MultipleRecords(t *testing.T) {
	m := &InMemoryMetrics{}
	m.RecordRPC(context.Background(), "/a", "OK", time.Millisecond)
	m.RecordRPC(context.Background(), "/b", "NotFound", 2*time.Millisecond)

	records := m.Records()
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
}

func TestInMemoryMetrics_Reset(t *testing.T) {
	m := &InMemoryMetrics{}
	m.RecordRPC(context.Background(), "/a", "OK", time.Millisecond)
	m.Reset()

	records := m.Records()
	if len(records) != 0 {
		t.Fatalf("expected 0 records after reset, got %d", len(records))
	}
}
