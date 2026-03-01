package health

import (
	"context"
	"sync"

	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// GRPCServer implements the gRPC Health/v1 service backed by the same [Checker]
// instances used for HTTP readiness.
//
// Register it with:
//
//	healthpb.RegisterHealthServer(grpcServer, health.NewGRPCServer(checks...))
type GRPCServer struct {
	healthpb.UnimplementedHealthServer

	mu      sync.RWMutex
	checks  []Checker
	serving map[string]healthpb.HealthCheckResponse_ServingStatus
}

// NewGRPCServer creates a gRPC health server.
// Provide the same checkers used for HTTP readiness.
func NewGRPCServer(checks ...Checker) *GRPCServer {
	return &GRPCServer{
		checks:  checks,
		serving: make(map[string]healthpb.HealthCheckResponse_ServingStatus),
	}
}

// SetServingStatus manually overrides the serving status for a named service.
// This is useful for graceful shutdown (set NOT_SERVING) or startup gating.
func (s *GRPCServer) SetServingStatus(service string, status healthpb.HealthCheckResponse_ServingStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serving[service] = status
}

// Check implements grpc.health.v1.Health/Check.
//
// When service is empty (""), it runs all registered checkers and returns
// SERVING if all pass, NOT_SERVING if any fail.
// When service is non-empty, it returns the manually-set status via [SetServingStatus],
// or NOT_FOUND if no status has been set for that service.
func (s *GRPCServer) Check(ctx context.Context, req *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	if req.Service == "" {
		return s.checkOverall(ctx)
	}

	s.mu.RLock()
	st, ok := s.serving[req.Service]
	s.mu.RUnlock()
	if !ok {
		return nil, status.Errorf(codes.NotFound, "unknown service %q", req.Service)
	}
	return &healthpb.HealthCheckResponse{Status: st}, nil
}

func (s *GRPCServer) checkOverall(ctx context.Context) (*healthpb.HealthCheckResponse, error) {
	if len(s.checks) == 0 {
		return &healthpb.HealthCheckResponse{
			Status: healthpb.HealthCheckResponse_SERVING,
		}, nil
	}

	results := make([]ComponentStatus, len(s.checks))
	var wg sync.WaitGroup
	wg.Add(len(s.checks))
	for i, check := range s.checks {
		go func(idx int, c Checker) {
			defer wg.Done()
			results[idx] = c.Check(ctx)
		}(i, check)
	}
	wg.Wait()

	for _, r := range results {
		if r.Status == StatusDown {
			return &healthpb.HealthCheckResponse{
				Status: healthpb.HealthCheckResponse_NOT_SERVING,
			}, nil
		}
	}
	return &healthpb.HealthCheckResponse{
		Status: healthpb.HealthCheckResponse_SERVING,
	}, nil
}
