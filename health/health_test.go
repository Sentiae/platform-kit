package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// --- mock checkers ---

type mockPinger struct{ err error }

func (m *mockPinger) PingContext(_ context.Context) error { return m.err }

type mockRedisPinger struct{ err error }

func (m *mockRedisPinger) Ping(_ context.Context) error { return m.err }

type mockKafkaDialer struct{ err error }

func (m *mockKafkaDialer) Dial(_ context.Context) error { return m.err }

// --- HTTP handler tests ---

func TestLiveness_ReturnsOK(t *testing.T) {
	h := NewHandler(Config{})
	rec := httptest.NewRecorder()
	h.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != StatusUp {
		t.Fatalf("expected status %q, got %q", StatusUp, resp.Status)
	}
	if len(resp.Components) != 0 {
		t.Fatalf("expected no components, got %d", len(resp.Components))
	}
}

func TestReadiness_NoChecks_ReturnsOK(t *testing.T) {
	h := NewHandler(Config{})
	rec := httptest.NewRecorder()
	h.handleReady(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != StatusUp {
		t.Fatalf("expected status %q, got %q", StatusUp, resp.Status)
	}
}

func TestReadiness_AllHealthy(t *testing.T) {
	h := NewHandler(Config{
		Checks: []Checker{
			DatabaseChecker(&mockPinger{}),
			RedisChecker(&mockRedisPinger{}),
		},
	})
	rec := httptest.NewRecorder()
	h.handleReady(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != StatusUp {
		t.Fatalf("expected status %q, got %q", StatusUp, resp.Status)
	}
	if len(resp.Components) != 2 {
		t.Fatalf("expected 2 components, got %d", len(resp.Components))
	}
	assertComponent(t, resp.Components[0], "database", StatusUp, "")
	assertComponent(t, resp.Components[1], "redis", StatusUp, "")
}

func TestReadiness_DatabaseDown(t *testing.T) {
	h := NewHandler(Config{
		Checks: []Checker{
			DatabaseChecker(&mockPinger{err: errors.New("connection refused")}),
		},
	})
	rec := httptest.NewRecorder()
	h.handleReady(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	var resp Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != StatusDown {
		t.Fatalf("expected status %q, got %q", StatusDown, resp.Status)
	}
	assertComponent(t, resp.Components[0], "database", StatusDown, "connection refused")
}

func TestReadiness_RedisDown(t *testing.T) {
	h := NewHandler(Config{
		Checks: []Checker{
			DatabaseChecker(&mockPinger{}),
			RedisChecker(&mockRedisPinger{err: errors.New("redis: connection refused")}),
		},
	})
	rec := httptest.NewRecorder()
	h.handleReady(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	var resp Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != StatusDown {
		t.Fatalf("expected status %q, got %q", StatusDown, resp.Status)
	}
	// DB should be up, Redis down
	assertComponent(t, resp.Components[0], "database", StatusUp, "")
	assertComponent(t, resp.Components[1], "redis", StatusDown, "redis: connection refused")
}

func TestReadiness_KafkaDown(t *testing.T) {
	h := NewHandler(Config{
		Checks: []Checker{
			KafkaChecker(&mockKafkaDialer{err: errors.New("broker unreachable")}),
		},
	})
	rec := httptest.NewRecorder()
	h.handleReady(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	var resp Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	assertComponent(t, resp.Components[0], "kafka", StatusDown, "broker unreachable")
}

func TestServeHTTP_RoutesToCorrectHandler(t *testing.T) {
	h := NewHandler(Config{
		Checks: []Checker{
			DatabaseChecker(&mockPinger{err: errors.New("down")}),
		},
	})

	// /health should return 200 regardless of check status
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/health: expected 200, got %d", rec.Code)
	}

	// /ready should return 503 because DB is down
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ready: expected 503, got %d", rec.Code)
	}
}

func TestLivenessHandler_Func(t *testing.T) {
	h := NewHandler(Config{})
	fn := h.LivenessHandler()
	rec := httptest.NewRecorder()
	fn(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestReadinessHandler_Func(t *testing.T) {
	h := NewHandler(Config{
		Checks: []Checker{DatabaseChecker(&mockPinger{})},
	})
	fn := h.ReadinessHandler()
	rec := httptest.NewRecorder()
	fn(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestCustomTimeout(t *testing.T) {
	slow := CheckerFunc(func(ctx context.Context) ComponentStatus {
		select {
		case <-ctx.Done():
			return ComponentStatus{Name: "slow", Status: StatusDown, Message: "timeout"}
		case <-time.After(5 * time.Second):
			return ComponentStatus{Name: "slow", Status: StatusUp}
		}
	})

	h := NewHandler(Config{
		Checks:  []Checker{slow},
		Timeout: 50 * time.Millisecond,
	})
	rec := httptest.NewRecorder()
	h.handleReady(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (timeout), got %d", rec.Code)
	}
}

func TestCheckerFunc_Adapter(t *testing.T) {
	cf := CheckerFunc(func(_ context.Context) ComponentStatus {
		return ComponentStatus{Name: "custom", Status: StatusUp}
	})
	cs := cf.Check(context.Background())
	if cs.Name != "custom" || cs.Status != StatusUp {
		t.Fatalf("unexpected result: %+v", cs)
	}
}

func TestContentType_IsJSON(t *testing.T) {
	h := NewHandler(Config{})

	rec := httptest.NewRecorder()
	h.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}

	rec = httptest.NewRecorder()
	h.handleReady(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	ct = rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}
}

// --- helpers ---

func assertComponent(t *testing.T, cs ComponentStatus, name string, status Status, message string) {
	t.Helper()
	if cs.Name != name {
		t.Errorf("expected component name %q, got %q", name, cs.Name)
	}
	if cs.Status != status {
		t.Errorf("expected component %q status %q, got %q", name, status, cs.Status)
	}
	if message != "" && cs.Message != message {
		t.Errorf("expected component %q message %q, got %q", name, message, cs.Message)
	}
}
