package health

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestGRPCServer_Check_EmptyService_NoChecks(t *testing.T) {
	srv := NewGRPCServer()
	resp, err := srv.Check(context.Background(), &healthpb.HealthCheckRequest{Service: ""})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("expected SERVING, got %v", resp.Status)
	}
}

func TestGRPCServer_Check_EmptyService_AllHealthy(t *testing.T) {
	srv := NewGRPCServer(
		DatabaseChecker(&mockPinger{}),
		RedisChecker(&mockRedisPinger{}),
	)
	resp, err := srv.Check(context.Background(), &healthpb.HealthCheckRequest{Service: ""})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("expected SERVING, got %v", resp.Status)
	}
}

func TestGRPCServer_Check_EmptyService_OneDown(t *testing.T) {
	srv := NewGRPCServer(
		DatabaseChecker(&mockPinger{}),
		RedisChecker(&mockRedisPinger{err: errors.New("down")}),
	)
	resp, err := srv.Check(context.Background(), &healthpb.HealthCheckRequest{Service: ""})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected NOT_SERVING, got %v", resp.Status)
	}
}

func TestGRPCServer_Check_NamedService_Found(t *testing.T) {
	srv := NewGRPCServer()
	srv.SetServingStatus("identity", healthpb.HealthCheckResponse_SERVING)

	resp, err := srv.Check(context.Background(), &healthpb.HealthCheckRequest{Service: "identity"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("expected SERVING, got %v", resp.Status)
	}
}

func TestGRPCServer_Check_NamedService_NotServing(t *testing.T) {
	srv := NewGRPCServer()
	srv.SetServingStatus("identity", healthpb.HealthCheckResponse_NOT_SERVING)

	resp, err := srv.Check(context.Background(), &healthpb.HealthCheckRequest{Service: "identity"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected NOT_SERVING, got %v", resp.Status)
	}
}

func TestGRPCServer_Check_NamedService_NotFound(t *testing.T) {
	srv := NewGRPCServer()
	_, err := srv.Check(context.Background(), &healthpb.HealthCheckRequest{Service: "unknown"})
	if err == nil {
		t.Fatal("expected error for unknown service")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NOT_FOUND, got %v", st.Code())
	}
}

func TestGRPCServer_SetServingStatus_Override(t *testing.T) {
	srv := NewGRPCServer()
	srv.SetServingStatus("svc", healthpb.HealthCheckResponse_SERVING)
	srv.SetServingStatus("svc", healthpb.HealthCheckResponse_NOT_SERVING)

	resp, err := srv.Check(context.Background(), &healthpb.HealthCheckRequest{Service: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected NOT_SERVING after override, got %v", resp.Status)
	}
}
