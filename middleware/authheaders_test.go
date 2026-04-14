package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestExtractOrganizationID_PrefersXOrganizationID(t *testing.T) {
	org := uuid.New()
	tenant := uuid.New()
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Organization-ID", org.String())
	r.Header.Set("X-Tenant-ID", tenant.String())
	if got := ExtractOrganizationID(r); got != org {
		t.Fatalf("got %s, want %s", got, org)
	}
}

func TestExtractOrganizationID_FallsBackToTenant(t *testing.T) {
	tenant := uuid.New()
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Tenant-ID", tenant.String())
	if got := ExtractOrganizationID(r); got != tenant {
		t.Fatalf("got %s, want %s", got, tenant)
	}
}

func TestExtractOrganizationID_InvalidReturnsNil(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Organization-ID", "not-a-uuid")
	if got := ExtractOrganizationID(r); got != uuid.Nil {
		t.Fatalf("want uuid.Nil, got %s", got)
	}
}

func TestExtractBearerToken(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer abc.def")
	if got := ExtractBearerToken(r); got != "abc.def" {
		t.Fatalf("got %q, want abc.def", got)
	}
	r.Header.Set("Authorization", "Basic xxx")
	if got := ExtractBearerToken(r); got != "" {
		t.Fatalf("non-bearer should be empty, got %q", got)
	}
}
