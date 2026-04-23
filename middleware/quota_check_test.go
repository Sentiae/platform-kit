package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sentiae/platform-kit/quota"
)

func TestQuotaCheckMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		serverBody     string
		serverStatus   int
		useEmptyURL    bool
		orgID          string
		wantStatus     int
		wantBodyFrag   string
		wantDownstream bool
	}{
		{
			name:           "allowed passes through",
			serverBody:     `{"data":{"allowed":true}}`,
			serverStatus:   http.StatusOK,
			orgID:          "org-1",
			wantStatus:     http.StatusOK,
			wantDownstream: true,
		},
		{
			name:           "denied returns 402 quota_exceeded",
			serverBody:     `{"data":{"allowed":false}}`,
			serverStatus:   http.StatusOK,
			orgID:          "org-2",
			wantStatus:     http.StatusPaymentRequired,
			wantBodyFrag:   "quota_exceeded",
			wantDownstream: false,
		},
		{
			name:           "server error fails open",
			serverStatus:   http.StatusInternalServerError,
			orgID:          "org-3",
			wantStatus:     http.StatusOK,
			wantDownstream: true,
		},
		{
			name:           "empty base URL fails open",
			useEmptyURL:    true,
			orgID:          "org-4",
			wantStatus:     http.StatusOK,
			wantDownstream: true,
		},
		{
			name:           "missing orgID skips check",
			orgID:          "",
			wantStatus:     http.StatusOK,
			wantDownstream: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.serverStatus != 0 {
					w.WriteHeader(tc.serverStatus)
				}
				if tc.serverBody != "" {
					_, _ = w.Write([]byte(tc.serverBody))
				}
			}))
			defer srv.Close()

			cfg := quota.Config{BaseURL: srv.URL}
			if tc.useEmptyURL {
				cfg = quota.Config{}
			}
			client := quota.New(cfg)

			downstreamHit := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				downstreamHit = true
				w.WriteHeader(http.StatusOK)
			})
			mw := NewCheckMiddleware(client, quota.ActionAddSpec, func(r *http.Request) string {
				return tc.orgID
			})
			h := mw(next)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/target", nil)
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status: want %d, got %d (body=%s)", tc.wantStatus, rec.Code, rec.Body.String())
			}
			if tc.wantBodyFrag != "" && !strings.Contains(rec.Body.String(), tc.wantBodyFrag) {
				t.Fatalf("body: want fragment %q, got %q", tc.wantBodyFrag, rec.Body.String())
			}
			if downstreamHit != tc.wantDownstream {
				t.Fatalf("downstream: want hit=%v, got %v", tc.wantDownstream, downstreamHit)
			}
		})
	}
}

func TestQuotaCheckMiddleware_NilClient(t *testing.T) {
	downstreamHit := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downstreamHit = true
		w.WriteHeader(http.StatusOK)
	})
	mw := NewCheckMiddleware(nil, quota.ActionAddSpec, func(r *http.Request) string { return "org-1" })
	h := mw(next)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/target", nil)
	h.ServeHTTP(rec, req)

	if !downstreamHit {
		t.Fatalf("nil client should pass through")
	}
}
