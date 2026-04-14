package debug

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnabled_OffByDefault(t *testing.T) {
	t.Setenv("SENTIAE_DEBUG_PPROF", "")
	if Enabled() {
		t.Fatal("should be off by default")
	}
}

func TestEnabled_Variants(t *testing.T) {
	for _, v := range []string{"1", "true", "yes", "TRUE", " 1 "} {
		t.Setenv("SENTIAE_DEBUG_PPROF", v)
		if !Enabled() {
			t.Fatalf("want enabled for %q", v)
		}
	}
}

func TestMountHTTPHandler_Noop(t *testing.T) {
	t.Setenv("SENTIAE_DEBUG_PPROF", "")
	mux := http.NewServeMux()
	MountHTTPHandler(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("should 404 when disabled, got %d", rec.Code)
	}
}

func TestMountHTTPHandler_On(t *testing.T) {
	t.Setenv("SENTIAE_DEBUG_PPROF", "1")
	mux := http.NewServeMux()
	MountHTTPHandler(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/cmdline", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}
