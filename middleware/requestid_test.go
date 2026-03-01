package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sentiae/platform-kit/logger"
)

func TestRequestID_GeneratesNew(t *testing.T) {
	var ctxID string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxID, _ = r.Context().Value(logger.RequestIDKey).(string)
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

	if ctxID == "" {
		t.Fatal("expected request ID in context, got empty")
	}
	if len(ctxID) != 32 { // 16 bytes = 32 hex chars
		t.Fatalf("expected 32-char hex ID, got %q (len=%d)", ctxID, len(ctxID))
	}

	respID := rr.Header().Get("X-Request-Id")
	if respID != ctxID {
		t.Fatalf("response header %q != context ID %q", respID, ctxID)
	}
}

func TestRequestID_UsesExisting(t *testing.T) {
	existing := "my-custom-request-id"
	var ctxID string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxID, _ = r.Context().Value(logger.RequestIDKey).(string)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-Id", existing)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if ctxID != existing {
		t.Fatalf("expected context ID %q, got %q", existing, ctxID)
	}
	if rr.Header().Get("X-Request-Id") != existing {
		t.Fatalf("expected response header %q, got %q", existing, rr.Header().Get("X-Request-Id"))
	}
}

func TestGetRequestID(t *testing.T) {
	var gotID string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = GetRequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

	if gotID == "" {
		t.Fatal("GetRequestID returned empty")
	}
}
