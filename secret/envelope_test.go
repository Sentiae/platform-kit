package secret

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// fakeKEK records whether Decrypt was invoked so tests can assert the
// oracle-free property (a denied caller must not reach the KEK).
type fakeKEK struct {
	pt    []byte
	err   error
	calls int
}

func (f *fakeKEK) Decrypt(_ context.Context, _ uuid.UUID, _ string) ([]byte, error) {
	f.calls++
	return f.pt, f.err
}

func TestAuthorizeRef(t *testing.T) {
	refA := TenantRef(orgA, "prod/app", "db_password")

	tests := []struct {
		name      string
		ref       string
		principal Principal
		wantErr   error
		wantOrg   uuid.UUID
		wantPath  string
		wantField string
	}{
		{
			name:      "valid same-org",
			ref:       refA,
			principal: Principal{Service: "delivery", OrgID: orgA.String()},
			wantOrg:   orgA,
			wantPath:  "tenants/c883c1d0-249a-4262-bf9c-f4c30f0850b6/prod/app",
			wantField: "db_password",
		},
		{
			name:      "unscoped ref",
			ref:       "sentiae/prod/app#db_password",
			principal: Principal{Service: "delivery", OrgID: orgA.String()},
			wantErr:   ErrUnscopedSecretRef,
		},
		{
			name:      "cross-org denied",
			ref:       refA,
			principal: Principal{Service: "delivery", OrgID: orgB.String()},
			wantErr:   ErrCrossTenantSecret,
		},
		{
			name:      "empty principal org",
			ref:       refA,
			principal: Principal{Service: "delivery"},
			wantErr:   ErrCrossTenantSecret,
		},
		{
			name:      "unparseable principal org",
			ref:       refA,
			principal: Principal{Service: "delivery", OrgID: "not-a-uuid"},
			wantErr:   ErrCrossTenantSecret,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			org, path, field, err := authorizeRef(context.Background(), tt.ref, tt.principal)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if org != tt.wantOrg {
				t.Fatalf("org = %v, want %v", org, tt.wantOrg)
			}
			if path != tt.wantPath || field != tt.wantField {
				t.Fatalf("path/field = %q/%q, want %q/%q", path, field, tt.wantPath, tt.wantField)
			}
		})
	}
}

func TestEnvelopeVaultResolver_Resolve(t *testing.T) {
	refA := TenantRef(orgA, "prod/app", "db_password")

	t.Run("same-org unseals blob to plaintext", func(t *testing.T) {
		kv := &fakeGetter{val: "vault:v1:opaque-blob"}
		kek := &fakeKEK{pt: []byte("s3cr3t")}
		r := NewEnvelopeVaultResolver(kv, kek)

		v, err := r.Resolve(context.Background(), refA, Principal{Service: "delivery", OrgID: orgA.String()})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if v.Reveal() != "s3cr3t" {
			t.Fatalf("Reveal = %q", v.Reveal())
		}
		if kv.gotPath != "tenants/c883c1d0-249a-4262-bf9c-f4c30f0850b6/prod/app" || kv.gotKey != "db_password" {
			t.Fatalf("kv got path=%q key=%q", kv.gotPath, kv.gotKey)
		}
		if kv.calls != 1 || kek.calls != 1 {
			t.Fatalf("calls kv=%d kek=%d, want 1/1", kv.calls, kek.calls)
		}
	})

	// I28 oracle-free: a cross-tenant caller must reach NEITHER kv NOR kek.
	t.Run("cross-org denied oracle-free (neither kv nor kek called)", func(t *testing.T) {
		kv := &fakeGetter{val: "vault:v1:opaque-blob"}
		kek := &fakeKEK{pt: []byte("s3cr3t")}
		r := NewEnvelopeVaultResolver(kv, kek)

		_, err := r.Resolve(context.Background(), refA, Principal{Service: "delivery", OrgID: orgB.String()})
		if !errors.Is(err, ErrCrossTenantSecret) {
			t.Fatalf("want ErrCrossTenantSecret, got %v", err)
		}
		if kv.calls != 0 {
			t.Fatalf("kv called %d times on cross-tenant probe — existence oracle leak", kv.calls)
		}
		if kek.calls != 0 {
			t.Fatalf("kek called %d times on cross-tenant probe — existence oracle leak", kek.calls)
		}
	})

	t.Run("unscoped ref rejected, neither kv nor kek called", func(t *testing.T) {
		kv := &fakeGetter{val: "blob"}
		kek := &fakeKEK{pt: []byte("s3cr3t")}
		r := NewEnvelopeVaultResolver(kv, kek)

		_, err := r.Resolve(context.Background(), "sentiae/prod/app#db_password", Principal{Service: "delivery", OrgID: orgA.String()})
		if !errors.Is(err, ErrUnscopedSecretRef) {
			t.Fatalf("want ErrUnscopedSecretRef, got %v", err)
		}
		if kv.calls != 0 || kek.calls != 0 {
			t.Fatalf("calls kv=%d kek=%d, want 0/0", kv.calls, kek.calls)
		}
	})

	t.Run("kv not-found maps to ErrSecretNotFound, kek not called", func(t *testing.T) {
		kv := &fakeGetter{err: errors.New("secret not found at path")}
		kek := &fakeKEK{pt: []byte("s3cr3t")}
		r := NewEnvelopeVaultResolver(kv, kek)

		_, err := r.Resolve(context.Background(), refA, Principal{Service: "delivery", OrgID: orgA.String()})
		if !errors.Is(err, ErrSecretNotFound) {
			t.Fatalf("want ErrSecretNotFound, got %v", err)
		}
		if kek.calls != 0 {
			t.Fatalf("kek called %d times after kv miss", kek.calls)
		}
	})

	t.Run("kek decrypt error surfaces, no value", func(t *testing.T) {
		kv := &fakeGetter{val: "vault:v1:foreign-blob"}
		kek := &fakeKEK{err: errors.New("cipher: message authentication failed")}
		r := NewEnvelopeVaultResolver(kv, kek)

		_, err := r.Resolve(context.Background(), refA, Principal{Service: "delivery", OrgID: orgA.String()})
		if err == nil || errors.Is(err, ErrSecretNotFound) {
			t.Fatalf("want a wrapped unseal error, got %v", err)
		}
	})
}
