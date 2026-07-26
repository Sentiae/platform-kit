// Package otel is the one shared OpenTelemetry bootstrap every backend
// service instruments through (invariant I11 — a single owner for the
// telemetry seam; CLAUDE.md §22). It replaces the per-service copies of
// pkg/telemetry that had drifted across the fleet.
//
// It sets up the three signals as vendor-neutral OTLP exporters pointed at
// the OTel Collector — the ONLY backend-aware component in the stack
// (platform-foundations pillar §6). The Collector fans out: traces + logs
// to ClickHouse, metrics to VictoriaMetrics. Swapping a backend never
// touches a service.
//
// Usage (see cmd/server/main.go of a migrated service):
//
//	shutdown, err := otel.Init(ctx, otel.Config{
//	    ServiceName: "identity-service", ServiceVersion: version, Environment: cfg.Env,
//	    Endpoint: cfg.OTLPEndpoint, Insecure: true,
//	})
//	defer shutdown(context.Background())
//	l := logger.New(logger.Config{Level: ..., Format: ..., ExtraHandlers: []slog.Handler{otel.SlogHandler("identity-service")}})
//
// Init sets the GLOBAL trace, meter, and logger providers + propagators, so
// otelhttp / otelgrpc auto-instrumentation and otelslog pick them up with no
// further wiring. It never fails a service on a dead collector: exporters are
// lazy and batched, so telemetry loss is a nuisance, never a request failure.
package otel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/sentiae/platform-kit/logger"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	prometheusbridge "go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	logglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// Config controls the OTLP bootstrap. Endpoint is the only field required to
// turn telemetry on; empty Endpoint makes Init a no-op (local unit runs, or a
// service deliberately not wired to the collector).
type Config struct {
	// ServiceName is the resource service.name; defaults to "unknown-service".
	ServiceName string
	// ServiceVersion (optional) is recorded as service.version.
	ServiceVersion string
	// Environment (optional) is recorded as deployment.environment (dev|staging|prod).
	Environment string
	// Endpoint is the collector's OTLP gRPC endpoint host:port, e.g.
	// "otel-collector:4317". Empty => Init is a no-op.
	Endpoint string
	// Insecure disables TLS on the OTLP gRPC connection. True on the homelab
	// LAN (plain h2c to the in-cluster collector).
	Insecure bool
	// SampleRatio is the head trace sample ratio in [0,1]. <=0 or >=1 samples
	// every trace (the default for a young platform — full fidelity).
	SampleRatio float64
	// InstanceID (optional) is the resource service.instance.id — what
	// distinguishes two processes of the SAME service so they don't collapse
	// onto one series. It MUST be stable across a process restart (a fresh uuid
	// per start makes every restart a new time series and breaks every rate),
	// and distinct per machine/container. Empty => derived from the hostname.
	//
	// The fleet host passes its DURABLE host uuid here (APP_FLEET_HOST_ID), not
	// a hostname, so its telemetry can be joined to placement records.
	InstanceID string
	// ErrorWindow (optional) throttles the Error-level report of OTLP export
	// failures: first failure logs immediately, further identical failures are
	// counted and re-reported at most once per window. <=0 => DefaultErrorWindow.
	ErrorWindow time.Duration
}

// Shutdown flushes and stops every provider. Call it on graceful shutdown
// with a bounded context (CLAUDE.md §21 step 8).
type Shutdown func(context.Context) error

// Init wires the trace, meter, and logger providers to the collector over
// OTLP gRPC, sets them as the process globals, and installs the W3C
// TraceContext + Baggage propagators. The returned Shutdown flushes all three.
func Init(ctx context.Context, cfg Config) (Shutdown, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		// No collector configured: leave the OTel globals as no-ops so
		// instrumentation calls are cheap and harmless.
		return func(context.Context) error { return nil }, nil
	}

	res, err := newResource(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("otel: build resource: %w", err)
	}

	// --- traces ---
	traceOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
	}
	traceExp, err := otlptracegrpc.New(ctx, traceOpts...)
	if err != nil {
		return nil, fmt.Errorf("otel: trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithSampler(sampler(cfg.SampleRatio)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	// --- metrics ---
	metricOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		metricOpts = append(metricOpts, otlpmetricgrpc.WithInsecure())
	}
	metricExp, err := otlpmetricgrpc.New(ctx, metricOpts...)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("otel: metric exporter: %w", err), tp.Shutdown(ctx))
	}
	// Bridge the default Prometheus registry (promauto metrics — e.g. the kafka
	// DLQ counter, interceptor request metrics) into the OTLP export path, so
	// every promauto metric rides the SAME pipeline as OTel-native metrics with
	// no second scrape plane. WithProducer registers the bridge as an additional
	// metric source on the periodic reader.
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithProducer(prometheusbridge.NewMetricProducer()),
		)),
	)
	otel.SetMeterProvider(mp)

	// --- logs ---
	logOpts := []otlploggrpc.Option{otlploggrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		logOpts = append(logOpts, otlploggrpc.WithInsecure())
	}
	logExp, err := otlploggrpc.New(ctx, logOpts...)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("otel: log exporter: %w", err), mp.Shutdown(ctx), tp.Shutdown(ctx))
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
	)
	logglobal.SetLoggerProvider(lp)

	// --- export-failure reporting ---
	// Every provider above exports in the background, where a failure is
	// reported ONLY through the global error handler. The SDK default is
	// log.Print, which Go 1.21+ routes through slog at INFO, so a service that
	// has stopped exporting is invisible to error-level alerting. Install a
	// handler that reports at Error level, throttled (see errorThrottle).
	throttle := newErrorThrottle(cfg.ErrorWindow, time.Now, loggerFn(ctx))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(throttle.Handle))
	sweepCtx, stopSweeper := context.WithCancel(context.WithoutCancel(ctx))
	go throttle.runSweeper(sweepCtx)

	return func(sctx context.Context) error {
		stopSweeper()
		// Reverse order; join errors so one failure doesn't hide another.
		return errors.Join(lp.Shutdown(sctx), mp.Shutdown(sctx), tp.Shutdown(sctx))
	}, nil
}

// loggerFn adapts the caller's context logger to the throttle's log sink. The
// context is detached from cancellation so shutdown-time export failures are
// still reported, and it keeps the ctx values (the service logger, trace ids)
// that logger.FromContext and the context handler rely on.
func loggerFn(ctx context.Context) func(slog.Level, string, ...any) {
	l := logger.FromContext(ctx)
	lctx := context.WithoutCancel(ctx)
	return func(level slog.Level, msg string, args ...any) {
		l.Log(lctx, level, msg, args...)
	}
}

// SlogHandler returns an slog.Handler that emits every record as a
// trace-correlated OTLP log record via the global logger provider Init set.
// Pass it in logger.Config.ExtraHandlers so stdout and OTLP see the same line.
// If Init has not run (or ran no-op), this handler harmlessly drops records to
// the no-op global provider.
func SlogHandler(scope string) slog.Handler {
	if scope == "" {
		scope = "sentiae"
	}
	return otelslog.NewHandler(scope)
}

func newResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	name := cfg.ServiceName
	if name == "" {
		name = "unknown-service"
	}
	attrs := []attribute.KeyValue{semconv.ServiceName(name)}
	if cfg.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.ServiceVersion))
	}
	if cfg.Environment != "" {
		// Kept as a literal key (stable across semconv versions).
		attrs = append(attrs, attribute.String("deployment.environment", cfg.Environment))
	}
	// service.instance.id + host.name: without them two processes of the same
	// service (e.g. the bare fleet host and a container) land on the SAME series
	// and are indistinguishable — telemetry that is delivered but unverifiable.
	if id := instanceID(cfg); id != "" {
		attrs = append(attrs, semconv.ServiceInstanceID(id))
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		attrs = append(attrs, semconv.HostName(h))
	}
	return resource.New(ctx, resource.WithAttributes(attrs...))
}

// instanceID resolves service.instance.id. Config wins (the fleet host supplies
// its durable host uuid), otherwise the hostname — which is stable across a
// process restart, unlike a per-start random uuid, and distinct per
// machine/container. Never generated: an unstable instance id would silently
// fork a new time series on every restart.
func instanceID(cfg Config) string {
	if id := strings.TrimSpace(cfg.InstanceID); id != "" {
		return id
	}
	if h, err := os.Hostname(); err == nil {
		return strings.TrimSpace(h)
	}
	return ""
}

func sampler(ratio float64) sdktrace.Sampler {
	if ratio <= 0 || ratio >= 1 {
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	}
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}
