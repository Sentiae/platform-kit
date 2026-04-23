package inbound

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func hmacHeader(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestReceiver_Verify_Accept(t *testing.T) {
	rec := New(NewInMemoryIdempotencyStore(time.Minute))
	var called bool
	rec.Register(Provider{
		Name:   "generic",
		Secret: "s3cret",
		Handler: func(ctx Context, body []byte) error {
			called = true
			return nil
		},
	})

	body := `{"event":"ping"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Sentiae-Signature", hmacHeader("s3cret", body))
	req.Header.Set("X-Idempotency-Key", "k1")
	w := httptest.NewRecorder()

	rec.Handle(w, req, "generic", "org-123")
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("handler was not called")
	}
}

func TestReceiver_Verify_Reject(t *testing.T) {
	rec := New(NewInMemoryIdempotencyStore(time.Minute))
	rec.Register(Provider{
		Name: "generic", Secret: "s3cret",
		Handler: func(ctx Context, body []byte) error { return nil },
	})

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("X-Sentiae-Signature", "sha256=deadbeef")
	w := httptest.NewRecorder()

	rec.Handle(w, req, "generic", "org-1")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestReceiver_Idempotency(t *testing.T) {
	rec := New(NewInMemoryIdempotencyStore(time.Minute))
	rec.Register(Provider{
		Name: "generic", Secret: "",
		Handler: func(ctx Context, body []byte) error { return nil },
	})

	mk := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
		req.Header.Set("X-Idempotency-Key", "dup")
		w := httptest.NewRecorder()
		rec.Handle(w, req, "generic", "org-1")
		return w
	}

	if w := mk(); w.Code != http.StatusAccepted {
		t.Fatalf("first expected 202, got %d", w.Code)
	}
	if w := mk(); w.Code != http.StatusConflict {
		t.Fatalf("second expected 409, got %d", w.Code)
	}
}

func TestReceiver_UnknownProvider(t *testing.T) {
	rec := New(nil)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	rec.Handle(w, req, "nope", "org-1")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
