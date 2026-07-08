package secret

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

type fakeGetter struct {
	val string
	err error
	// captured
	gotPath, gotKey string
	calls           int
}

func (f *fakeGetter) GetSecret(_ context.Context, path, key string) (string, error) {
	f.calls++
	f.gotPath, f.gotKey = path, key
	return f.val, f.err
}

// Fixed test orgs.
var (
	orgA = uuid.MustParse("c883c1d0-249a-4262-bf9c-f4c30f0850b6")
	orgB = uuid.MustParse("11111111-2222-3333-4444-555555555555")
)

func TestSecretValue_Redacts(t *testing.T) {
	v := SecretValue{value: "hunter2"}
	if v.Reveal() != "hunter2" {
		t.Fatalf("Reveal() = %q, want hunter2", v.Reveal())
	}
	// Every leak surface must redact.
	if got := v.String(); got != "[REDACTED]" {
		t.Fatalf("String() = %q", got)
	}
	if got := fmt.Sprintf("%s / %v", v, v); got != "[REDACTED] / [REDACTED]" {
		t.Fatalf("fmt = %q", got)
	}
	b, err := json.Marshal(struct{ S SecretValue }{v})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"S":"[REDACTED]"}` {
		t.Fatalf("json = %s", b)
	}
	if v.LogValue().String() != "[REDACTED]" {
		t.Fatalf("LogValue = %q", v.LogValue().String())
	}
}

func TestParseRef(t *testing.T) {
	tests := []struct {
		ref              string
		wantPath, wantFn string
		ok               bool
	}{
		{"sentiae/prod/app#db_password", "sentiae/prod/app", "db_password", true},
		{"a#b", "a", "b", true},
		{"a/b/c#field", "a/b/c", "field", true},
		{"nofield", "", "", false},
		{"#field", "", "", false},
		{"path#", "", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			p, f, ok := parseRef(tt.ref)
			if ok != tt.ok || p != tt.wantPath || f != tt.wantFn {
				t.Fatalf("parseRef(%q) = (%q,%q,%v), want (%q,%q,%v)", tt.ref, p, f, ok, tt.wantPath, tt.wantFn, tt.ok)
			}
		})
	}
}

func TestTenantRef_RoundTrips(t *testing.T) {
	ref := TenantRef(orgA, "prod/app", "db_password")
	want := "tenants/c883c1d0-249a-4262-bf9c-f4c30f0850b6/prod/app#db_password"
	if ref != want {
		t.Fatalf("TenantRef = %q, want %q", ref, want)
	}
	org, path, field, ok := parseTenantRef(ref)
	if !ok {
		t.Fatalf("parseTenantRef(%q) not ok", ref)
	}
	if org != orgA {
		t.Fatalf("org = %v, want %v", org, orgA)
	}
	if path != "tenants/c883c1d0-249a-4262-bf9c-f4c30f0850b6/prod/app" {
		t.Fatalf("path = %q", path)
	}
	if field != "db_password" {
		t.Fatalf("field = %q", field)
	}
}

func TestParseTenantRef(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		ok   bool
	}{
		{"valid", "tenants/c883c1d0-249a-4262-bf9c-f4c30f0850b6/prod/app#db_password", true},
		{"valid deep subpath", "tenants/c883c1d0-249a-4262-bf9c-f4c30f0850b6/a/b/c#f", true},
		{"non-namespaced", "sentiae/prod/app#db_password", false},
		{"no field", "tenants/c883c1d0-249a-4262-bf9c-f4c30f0850b6/prod/app", false},
		{"bad uuid", "tenants/not-a-uuid/prod/app#f", false},
		{"empty subpath", "tenants/c883c1d0-249a-4262-bf9c-f4c30f0850b6/#f", false},
		{"missing subpath slash", "tenants/c883c1d0-249a-4262-bf9c-f4c30f0850b6#f", false},
		{"empty org", "tenants//prod/app#f", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, ok := parseTenantRef(tt.ref)
			if ok != tt.ok {
				t.Fatalf("parseTenantRef(%q) ok = %v, want %v", tt.ref, ok, tt.ok)
			}
		})
	}
}

func TestVaultResolver_Resolve(t *testing.T) {
	refA := TenantRef(orgA, "prod/app", "db_password")

	t.Run("same-org resolves + hits the full path/field", func(t *testing.T) {
		fg := &fakeGetter{val: "s3cr3t"}
		r := NewVaultResolver(fg)
		v, err := r.Resolve(context.Background(), refA, Principal{Service: "delivery", OrgID: orgA.String()})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if v.Reveal() != "s3cr3t" {
			t.Fatalf("Reveal = %q", v.Reveal())
		}
		wantPath := "tenants/c883c1d0-249a-4262-bf9c-f4c30f0850b6/prod/app"
		if fg.gotPath != wantPath || fg.gotKey != "db_password" {
			t.Fatalf("vault got path=%q key=%q", fg.gotPath, fg.gotKey)
		}
		if fg.calls != 1 {
			t.Fatalf("vault calls = %d, want 1", fg.calls)
		}
	})

	// The critical I28 property: a cross-tenant caller is denied WITHOUT any
	// Vault call, so resolution cannot be used as an existence oracle.
	t.Run("cross-org denied oracle-free (vault NEVER called)", func(t *testing.T) {
		fg := &fakeGetter{val: "s3cr3t"}
		r := NewVaultResolver(fg)
		// ref belongs to orgA; caller's verified org is orgB.
		_, err := r.Resolve(context.Background(), refA, Principal{Service: "delivery", OrgID: orgB.String()})
		if !errors.Is(err, ErrCrossTenantSecret) {
			t.Fatalf("want ErrCrossTenantSecret, got %v", err)
		}
		if fg.calls != 0 {
			t.Fatalf("vault called %d times on cross-tenant probe — existence oracle leak", fg.calls)
		}
	})

	t.Run("empty principal org denied, vault not called", func(t *testing.T) {
		fg := &fakeGetter{val: "s3cr3t"}
		r := NewVaultResolver(fg)
		_, err := r.Resolve(context.Background(), refA, Principal{Service: "delivery"})
		if !errors.Is(err, ErrCrossTenantSecret) {
			t.Fatalf("want ErrCrossTenantSecret, got %v", err)
		}
		if fg.calls != 0 {
			t.Fatalf("vault calls = %d, want 0", fg.calls)
		}
	})

	t.Run("unparseable principal org denied, vault not called", func(t *testing.T) {
		fg := &fakeGetter{val: "s3cr3t"}
		r := NewVaultResolver(fg)
		_, err := r.Resolve(context.Background(), refA, Principal{Service: "delivery", OrgID: "not-a-uuid"})
		if !errors.Is(err, ErrCrossTenantSecret) {
			t.Fatalf("want ErrCrossTenantSecret, got %v", err)
		}
		if fg.calls != 0 {
			t.Fatalf("vault calls = %d, want 0", fg.calls)
		}
	})

	t.Run("non-namespaced ref rejected, vault not called", func(t *testing.T) {
		fg := &fakeGetter{val: "s3cr3t"}
		r := NewVaultResolver(fg)
		_, err := r.Resolve(context.Background(), "sentiae/prod/app#db_password", Principal{Service: "delivery", OrgID: orgA.String()})
		if !errors.Is(err, ErrUnscopedSecretRef) {
			t.Fatalf("want ErrUnscopedSecretRef, got %v", err)
		}
		if fg.calls != 0 {
			t.Fatalf("vault calls = %d, want 0", fg.calls)
		}
	})

	t.Run("malformed refs rejected as unscoped", func(t *testing.T) {
		for _, ref := range []string{"no-hash", "tenants/not-a-uuid/app#f", "tenants/c883c1d0-249a-4262-bf9c-f4c30f0850b6#f"} {
			fg := &fakeGetter{}
			r := NewVaultResolver(fg)
			_, err := r.Resolve(context.Background(), ref, Principal{Service: "delivery", OrgID: orgA.String()})
			if !errors.Is(err, ErrUnscopedSecretRef) {
				t.Fatalf("ref %q: want ErrUnscopedSecretRef, got %v", ref, err)
			}
			if fg.calls != 0 {
				t.Fatalf("ref %q: vault calls = %d, want 0", ref, fg.calls)
			}
		}
	})

	t.Run("not found maps to ErrSecretNotFound", func(t *testing.T) {
		r := NewVaultResolver(&fakeGetter{err: errors.New("secret not found at path")})
		_, err := r.Resolve(context.Background(), refA, Principal{Service: "runtime-fleet", OrgID: orgA.String()})
		if !errors.Is(err, ErrSecretNotFound) {
			t.Fatalf("want ErrSecretNotFound, got %v", err)
		}
	})

	t.Run("other error wrapped, ref included, no value leak", func(t *testing.T) {
		r := NewVaultResolver(&fakeGetter{err: errors.New("connection refused")})
		_, err := r.Resolve(context.Background(), refA, Principal{Service: "node-service", OrgID: orgA.String()})
		if err == nil || errors.Is(err, ErrSecretNotFound) {
			t.Fatalf("want a wrapped non-notfound error, got %v", err)
		}
	})
}
