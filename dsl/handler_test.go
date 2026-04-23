package dsl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestHandler_UnknownActionReturns400(t *testing.T) {
	h := NewHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dsl/execute", strings.NewReader(`{"action":"missing","flow_id":"`+uuid.NewString()+`","step_name":"s1"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body Response
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body.Error, "missing") {
		t.Fatalf("expected error to name action, got %q", body.Error)
	}
}

func TestHandler_MissingActionReturns400(t *testing.T) {
	h := NewHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dsl/execute", strings.NewReader(`{"flow_id":"`+uuid.NewString()+`","step_name":"s1"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandler_InvalidJSONReturns400(t *testing.T) {
	h := NewHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dsl/execute", strings.NewReader(`{not json`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandler_NonPOSTReturns405(t *testing.T) {
	h := NewHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dsl/execute", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandler_HappyPathReturnsOutput(t *testing.T) {
	h := NewHandler()
	h.Register("echo", func(_ context.Context, req Request) (map[string]any, error) {
		return map[string]any{"seen_action": req.Action, "seen_step": req.StepName}, nil
	})

	flowID := uuid.NewString()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dsl/execute",
		strings.NewReader(`{"action":"echo","flow_id":"`+flowID+`","step_name":"s1"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body Response
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Completed {
		t.Fatalf("expected completed=true")
	}
	if body.Output["seen_action"] != "echo" || body.Output["seen_step"] != "s1" {
		t.Fatalf("unexpected output: %+v", body.Output)
	}
}

func TestHandler_ActionErrorReturns200WithError(t *testing.T) {
	// ActionError is a structured dispatch failure — the foundry worker
	// should record the step as failed but treat it as a normal step
	// completion, not a transport error. Contract: 200 OK + error in body.
	h := NewHandler()
	h.Register("fail", func(_ context.Context, _ Request) (map[string]any, error) {
		return nil, &ActionError{Msg: "spec already exists"}
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dsl/execute",
		strings.NewReader(`{"action":"fail","flow_id":"`+uuid.NewString()+`","step_name":"s1"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for ActionError, got %d", rec.Code)
	}
	var body Response
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error != "spec already exists" {
		t.Fatalf("expected ActionError message in body, got %q", body.Error)
	}
	if body.Completed {
		t.Fatalf("ActionError should not mark step completed")
	}
}

func TestHandler_GenericErrorReturns500(t *testing.T) {
	h := NewHandler()
	h.Register("boom", func(_ context.Context, _ Request) (map[string]any, error) {
		return nil, errors.New("downstream broke")
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dsl/execute",
		strings.NewReader(`{"action":"boom","flow_id":"`+uuid.NewString()+`","step_name":"s1"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	var body Response
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !strings.Contains(body.Error, "downstream broke") {
		t.Fatalf("expected error body, got %q", body.Error)
	}
}

func TestHandler_RegisterOverwrites(t *testing.T) {
	h := NewHandler()
	h.Register("x", func(_ context.Context, _ Request) (map[string]any, error) {
		return map[string]any{"v": 1}, nil
	})
	h.Register("x", func(_ context.Context, _ Request) (map[string]any, error) {
		return map[string]any{"v": 2}, nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dsl/execute",
		strings.NewReader(`{"action":"x","flow_id":"`+uuid.NewString()+`","step_name":"s1"}`))
	h.ServeHTTP(rec, req)

	var body Response
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	// JSON numbers decode as float64
	if v, _ := body.Output["v"].(float64); v != 2 {
		t.Fatalf("expected v=2 (second registration wins), got %+v", body.Output)
	}
}

func TestActionError_NilError(t *testing.T) {
	var e *ActionError
	if got := e.Error(); got != "" {
		t.Fatalf("nil ActionError should stringify empty, got %q", got)
	}
}
