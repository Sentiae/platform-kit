package interceptor

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// MetricsCollector is the interface that a concrete metrics implementation must satisfy.
// This allows services to plug in their own metrics backend (Prometheus, OTel, etc.).
type MetricsCollector interface {
	// RecordRPC is called after each RPC completes with the method name,
	// gRPC status code string, and call duration.
	RecordRPC(ctx context.Context, method string, code string, duration time.Duration)
}

// UnaryMetrics returns a unary server interceptor that records request count
// and latency via the provided MetricsCollector.
func UnaryMetrics(collector MetricsCollector) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		collector.RecordRPC(ctx, info.FullMethod, status.Code(err).String(), time.Since(start))
		return resp, err
	}
}

// StreamMetrics returns a stream server interceptor that records request count
// and latency via the provided MetricsCollector.
func StreamMetrics(collector MetricsCollector) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		collector.RecordRPC(ss.Context(), info.FullMethod, status.Code(err).String(), time.Since(start))
		return err
	}
}

// InMemoryMetrics is a simple in-memory MetricsCollector for testing and development.
// It tracks RPC counts and durations by method and status code.
type InMemoryMetrics struct {
	mu      sync.Mutex
	records []MetricRecord
}

// MetricRecord holds a single RPC metric observation.
type MetricRecord struct {
	Method   string
	Code     string
	Duration time.Duration
}

// RecordRPC stores a metric record.
func (m *InMemoryMetrics) RecordRPC(_ context.Context, method string, code string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, MetricRecord{
		Method:   method,
		Code:     code,
		Duration: duration,
	})
}

// Records returns a copy of all recorded metrics.
func (m *InMemoryMetrics) Records() []MetricRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MetricRecord, len(m.records))
	copy(out, m.records)
	return out
}

// Reset clears all recorded metrics.
func (m *InMemoryMetrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = nil
}
