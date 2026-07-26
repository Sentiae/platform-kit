package otel

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sentiae/platform-kit/logger"
	otelapi "go.opentelemetry.io/otel"
)

func TestInit_NoEndpointIsNoop(t *testing.T) {
	shutdown, err := Init(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatalf("Init with empty endpoint: unexpected err %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown: unexpected err %v", err)
	}
}

func TestSampler(t *testing.T) {
	tests := []struct {
		name     string
		ratio    float64
		wantDesc string // substring of Sampler.Description()
	}{
		{"zero -> always", 0, "AlwaysOnSampler"},
		{"negative -> always", -1, "AlwaysOnSampler"},
		{"one -> always", 1, "AlwaysOnSampler"},
		{"above one -> always", 2, "AlwaysOnSampler"},
		{"half -> ratio", 0.5, "TraceIDRatioBased{0.5}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sampler(tt.ratio).Description()
			if !strings.Contains(got, tt.wantDesc) {
				t.Fatalf("sampler(%v).Description() = %q, want substring %q", tt.ratio, got, tt.wantDesc)
			}
		})
	}
}

func TestNewResource(t *testing.T) {
	tests := []struct {
		name       string
		cfg        Config
		wantName   string
		wantExtras map[string]string // attr key -> value that must be present
		absentKeys []string
	}{
		{
			name:       "name only",
			cfg:        Config{ServiceName: "identity-service"},
			wantName:   "identity-service",
			absentKeys: []string{"service.version", "deployment.environment"},
		},
		{
			name:       "empty name defaults",
			cfg:        Config{},
			wantName:   "unknown-service",
			absentKeys: []string{"service.version", "deployment.environment"},
		},
		{
			name:       "full",
			cfg:        Config{ServiceName: "codegen-service", ServiceVersion: "1.2.3", Environment: "dev"},
			wantName:   "codegen-service",
			wantExtras: map[string]string{"service.version": "1.2.3", "deployment.environment": "dev"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := newResource(context.Background(), tt.cfg)
			if err != nil {
				t.Fatalf("newResource: %v", err)
			}
			attrs := map[string]string{}
			for _, kv := range res.Attributes() {
				attrs[string(kv.Key)] = kv.Value.AsString()
			}
			if attrs["service.name"] != tt.wantName {
				t.Fatalf("service.name = %q, want %q", attrs["service.name"], tt.wantName)
			}
			for k, v := range tt.wantExtras {
				if attrs[k] != v {
					t.Fatalf("attr %q = %q, want %q", k, attrs[k], v)
				}
			}
			for _, k := range tt.absentKeys {
				if _, ok := attrs[k]; ok {
					t.Fatalf("attr %q should be absent, got %q", k, attrs[k])
				}
			}
		})
	}
}

func TestSlogHandler_NeverNil(t *testing.T) {
	if SlogHandler("") == nil || SlogHandler("svc") == nil {
		t.Fatal("SlogHandler returned nil")
	}
}

// --- resource identity (service.instance.id + host.name) ---

func TestNewResource_InstanceIdentity(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Skipf("hostname unavailable: %v", err)
	}

	t.Run("derived from hostname", func(t *testing.T) {
		attrs := resourceAttrs(t, Config{ServiceName: "svc"})
		if attrs["service.instance.id"] != host {
			t.Fatalf("service.instance.id = %q, want %q", attrs["service.instance.id"], host)
		}
		if attrs["host.name"] != host {
			t.Fatalf("host.name = %q, want %q", attrs["host.name"], host)
		}
	})

	t.Run("config overrides", func(t *testing.T) {
		// The fleet host supplies its durable host uuid (APP_FLEET_HOST_ID).
		const fleetHostID = "6f1d5a2e-0000-4c1a-9a3f-abcdef123456"
		attrs := resourceAttrs(t, Config{ServiceName: "svc", InstanceID: "  " + fleetHostID + "  "})
		if attrs["service.instance.id"] != fleetHostID {
			t.Fatalf("service.instance.id = %q, want %q", attrs["service.instance.id"], fleetHostID)
		}
	})

	t.Run("stable across two inits in one process", func(t *testing.T) {
		first := resourceAttrs(t, Config{ServiceName: "svc"})["service.instance.id"]
		second := resourceAttrs(t, Config{ServiceName: "svc"})["service.instance.id"]
		if first == "" {
			t.Fatal("service.instance.id is absent — telemetry from two processes of one service collapses onto the same series")
		}
		if first != second {
			t.Fatalf("service.instance.id changed between inits: %q then %q (every restart would fork a new time series)", first, second)
		}
	})
}

func resourceAttrs(t *testing.T, cfg Config) map[string]string {
	t.Helper()
	res, err := newResource(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newResource: %v", err)
	}
	attrs := map[string]string{}
	for _, kv := range res.Attributes() {
		attrs[string(kv.Key)] = kv.Value.AsString()
	}
	return attrs
}

// --- export-failure reporting ---

// captureHandler records every slog record it is handed.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *captureHandler) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r.Clone())
	return nil
}
func (c *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *captureHandler) WithGroup(string) slog.Handler      { return c }

func (c *captureHandler) snapshot() []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]slog.Record(nil), c.records...)
}

func (c *captureHandler) attr(t *testing.T, i int, key string) (slog.Value, bool) {
	t.Helper()
	var (
		val   slog.Value
		found bool
	)
	c.snapshot()[i].Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			val, found = a.Value, true
			return false
		}
		return true
	})
	return val, found
}

// TestInit_InstallsErrorLevelErrorHandler proves the fleet-wide bug is closed:
// an OTLP export failure must reach Error level, not INFO (the SDK default
// handler is log.Print, which slog routes at Info).
func TestInit_InstallsErrorLevelErrorHandler(t *testing.T) {
	cap := &captureHandler{}
	ctx := logger.NewContext(context.Background(), slog.New(cap))

	// Port is deliberately dead: OTLP gRPC exporters connect lazily, so Init
	// succeeds and nothing here talks to the network.
	shutdown, err := Init(ctx, Config{ServiceName: "svc", Endpoint: "127.0.0.1:1", Insecure: true})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() {
		// Bounded: the collector is unreachable, so an unbounded shutdown would
		// sit in the exporter's retry loop.
		sctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = shutdown(sctx)
	}()

	otelapi.GetErrorHandler().Handle(errors.New("boom: collector unreachable"))

	recs := cap.snapshot()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want 1: %+v", len(recs), recs)
	}
	if recs[0].Level != slog.LevelError {
		t.Fatalf("export failure logged at %v, want %v", recs[0].Level, slog.LevelError)
	}
	if !strings.Contains(recs[0].Message, "otel export failed") {
		t.Fatalf("message = %q", recs[0].Message)
	}
}

// --- throttle ---

type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time      { return f.t }
func (f *fakeClock) add(d time.Duration) { f.t = f.t.Add(d) }

func newTestThrottle(window time.Duration) (*errorThrottle, *fakeClock, *captureHandler) {
	clk := &fakeClock{t: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)}
	cap := &captureHandler{}
	l := slog.New(cap)
	th := newErrorThrottle(window, clk.now, func(level slog.Level, msg string, args ...any) {
		l.Log(context.Background(), level, msg, args...)
	})
	return th, clk, cap
}

func TestErrorThrottle_FirstLoudThenSuppressedThenCounted(t *testing.T) {
	th, clk, cap := newTestThrottle(5 * time.Minute)
	err := errors.New("export failed: connection refused")

	th.Handle(err)
	if got := len(cap.snapshot()); got != 1 {
		t.Fatalf("first failure produced %d records, want 1 (the first failure must be loud)", got)
	}
	if lvl := cap.snapshot()[0].Level; lvl != slog.LevelError {
		t.Fatalf("first failure level = %v, want Error", lvl)
	}
	if v, ok := cap.attr(t, 0, "suppressed"); !ok || v.Int64() != 0 {
		t.Fatalf("first failure suppressed = %v (ok=%v), want 0", v, ok)
	}

	// Inside the window: counted, never logged.
	for i := 0; i < 20; i++ {
		clk.add(10 * time.Second)
		th.Handle(err)
	}
	if got := len(cap.snapshot()); got != 1 {
		t.Fatalf("in-window failures produced %d records, want 1 (a per-interval line is a log flood)", got)
	}

	// Past the window: one line carrying the suppressed count.
	clk.add(5 * time.Minute)
	th.Handle(err)
	recs := cap.snapshot()
	if len(recs) != 2 {
		t.Fatalf("got %d records after the window, want 2", len(recs))
	}
	if recs[1].Level != slog.LevelError {
		t.Fatalf("re-report level = %v, want Error", recs[1].Level)
	}
	if v, ok := cap.attr(t, 1, "suppressed"); !ok || v.Int64() != 20 {
		t.Fatalf("re-report suppressed = %v (ok=%v), want 20", v, ok)
	}
}

func TestErrorThrottle_DistinctSignaturesEachLogOnce(t *testing.T) {
	th, _, cap := newTestThrottle(5 * time.Minute)
	th.Handle(errors.New("metric export failed"))
	th.Handle(errors.New("trace export failed"))
	// Same signature as the first modulo digits.
	th.Handle(errors.New("metric export failed"))
	if got := len(cap.snapshot()); got != 2 {
		t.Fatalf("got %d records, want 2 (one per distinct failure mode)", got)
	}
}

func TestErrorThrottle_SignatureCollapsesVaryingNumbers(t *testing.T) {
	th, _, cap := newTestThrottle(5 * time.Minute)
	th.Handle(errors.New("retry after 3s: 127.0.0.1:4317 unreachable"))
	th.Handle(errors.New("retry after 91s: 127.0.0.1:4317 unreachable"))
	if got := len(cap.snapshot()); got != 1 {
		t.Fatalf("got %d records, want 1 (numbers must not fork the signature)", got)
	}
}

func TestErrorThrottle_SignatureCardinalityBounded(t *testing.T) {
	th, _, _ := newTestThrottle(5 * time.Minute)
	for i := 0; i < maxErrorSignatures*3; i++ {
		th.Handle(errors.New(string(rune('a'+i%26)) + strings.Repeat("x", i%7) + "-unique-" + time.Duration(i).String()))
	}
	th.mu.Lock()
	n := len(th.state)
	th.mu.Unlock()
	// cap distinct signatures + the single overflow bucket.
	if n > maxErrorSignatures+1 {
		t.Fatalf("throttle holds %d signatures, want <= %d", n, maxErrorSignatures+1)
	}
}

func TestErrorThrottle_RecoveryIsVisibleWithCount(t *testing.T) {
	th, clk, cap := newTestThrottle(5 * time.Minute)
	err := errors.New("export failed: connection refused")

	th.Handle(err)
	for i := 0; i < 4; i++ {
		clk.add(time.Minute)
		th.Handle(err)
	}
	// Still failing => no recovery line.
	th.sweep()
	if got := len(cap.snapshot()); got != 1 {
		t.Fatalf("got %d records while still failing, want 1", got)
	}

	// A full window of quiet => recovery.
	clk.add(6 * time.Minute)
	th.sweep()
	recs := cap.snapshot()
	if len(recs) != 2 {
		t.Fatalf("got %d records after quiet window, want 2 (recovery must be visible)", len(recs))
	}
	if recs[1].Level != slog.LevelInfo {
		t.Fatalf("recovery level = %v, want Info", recs[1].Level)
	}
	if !strings.Contains(recs[1].Message, "otel export recovered") {
		t.Fatalf("recovery message = %q", recs[1].Message)
	}
	if v, ok := cap.attr(t, 1, "failures"); !ok || v.Int64() != 5 {
		t.Fatalf("recovery failures = %v (ok=%v), want 5", v, ok)
	}

	// State was dropped, so the next failure is loud again.
	th.Handle(err)
	if got := len(cap.snapshot()); got != 3 {
		t.Fatalf("got %d records, want 3 (a failure after recovery must be loud again)", got)
	}
}

func TestErrorThrottle_NilErrorIgnored(t *testing.T) {
	th, _, cap := newTestThrottle(time.Minute)
	th.Handle(nil)
	if got := len(cap.snapshot()); got != 0 {
		t.Fatalf("got %d records for a nil error, want 0", got)
	}
}
