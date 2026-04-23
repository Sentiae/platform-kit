package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClient_EmptyBaseURLFailsOpen(t *testing.T) {
	c := New(Config{})
	allowed, err := c.Check(context.Background(), "org-123", ActionAddRepo)
	if err != nil {
		t.Fatalf("expected nil error for empty base URL, got %v", err)
	}
	if !allowed {
		t.Fatalf("expected fail-open (allowed=true) when no URL configured")
	}
}

func TestClient_MissingOrgIDErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server should not be called when org id missing")
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL})

	_, err := c.Check(context.Background(), "", ActionDeploy)
	if err == nil {
		t.Fatalf("expected error for empty orgID")
	}
}

func TestClient_AllowedEnveloped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/quota/check") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("action"); got != string(ActionAddRepo) {
			t.Fatalf("expected action=add_repo, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"organization_id":"org-123","action":"add_repo","allowed":true}}`))
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL})

	allowed, err := c.Check(context.Background(), "org-123", ActionAddRepo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatalf("expected allowed=true")
	}
}

func TestClient_DeniedFlat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Flat (non-enveloped) body — client must fall back to direct decode.
		_, _ = w.Write([]byte(`{"organization_id":"org-7","action":"deploy","allowed":false}`))
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL})

	allowed, err := c.Check(context.Background(), "org-7", ActionDeploy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Fatalf("expected allowed=false")
	}
}

func TestClient_Non2xxErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL})

	_, err := c.Check(context.Background(), "org-x", ActionDeploy)
	if err == nil {
		t.Fatalf("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected status code in error, got %v", err)
	}
}

func TestClient_TimeoutDefault(t *testing.T) {
	c := New(Config{BaseURL: "http://example.invalid"})
	if c.http.Timeout != 5*time.Second {
		t.Fatalf("expected default 5s timeout, got %s", c.http.Timeout)
	}
}
