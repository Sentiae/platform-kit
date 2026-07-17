package secret

import (
	"context"
	"errors"
	"fmt"
	"testing"

	vault "github.com/hashicorp/vault/api"
)

// TestResolverErrorsAreMatchableSentinels is the T-HARDEN Wave 0 guard
// (#secret-resolve-no-sentinel): EVERY failure a Resolver returns must be
// matchable with errors.Is THROUGH a caller's own %w wrap. The wrap matters —
// runtime-fleet's resolveBootSecrets returns
// fmt.Errorf("resolve secret %q: %w", ref, err), so a sentinel that only
// matched bare would still reach the handler as an unmatchable blob and
// collapse to codes.Internal.
//
// Before this guard, the HandedTokenEnvelopeResolver's two fail-closed legs
// were bare fmt.Errorf with no sentinel and no %w — a genuine cross-tenant
// denial was indistinguishable on the wire from a crash.
func TestResolverErrorsAreMatchableSentinels(t *testing.T) {
	refA := TenantRef(orgA, "prod/app", "db_password")

	// A resolver whose base client exists (so the nil-base leg is not hit) —
	// mirrors NewHandedTokenEnvelopeResolver's normal construction.
	baseClient, err := vault.NewClient(vault.DefaultConfig())
	if err != nil {
		t.Fatalf("build base vault client: %v", err)
	}

	tests := []struct {
		name      string
		resolver  Resolver
		ref       string
		principal Principal
		want      error
	}{
		{
			name:      "ref not tenant-namespaced -> ErrUnscopedSecretRef",
			resolver:  NewEnvelopeVaultResolver(&fakeGetter{val: "ct"}, &fakeKEK{pt: []byte("pt")}),
			ref:       "prod/app#db_password", // no tenants/<org>/ prefix
			principal: Principal{Service: "runtime-fleet", OrgID: orgA.String()},
			want:      ErrUnscopedSecretRef,
		},
		{
			name:      "caller org does not own ref -> ErrCrossTenantSecret",
			resolver:  NewEnvelopeVaultResolver(&fakeGetter{val: "ct"}, &fakeKEK{pt: []byte("pt")}),
			ref:       refA,
			principal: Principal{Service: "runtime-fleet", OrgID: orgB.String()},
			want:      ErrCrossTenantSecret,
		},
		{
			name:      "anonymous caller -> ErrCrossTenantSecret",
			resolver:  NewEnvelopeVaultResolver(&fakeGetter{val: "ct"}, &fakeKEK{pt: []byte("pt")}),
			ref:       refA,
			principal: Principal{Service: "runtime-fleet", OrgID: ""},
			want:      ErrCrossTenantSecret,
		},
		{
			name:      "vault miss -> ErrSecretNotFound",
			resolver:  NewEnvelopeVaultResolver(&fakeGetter{err: errors.New("vault: key not found")}, &fakeKEK{}),
			ref:       refA,
			principal: Principal{Service: "runtime-fleet", OrgID: orgA.String()},
			want:      ErrSecretNotFound,
		},
		{
			name:      "handed-token resolver with no base client -> ErrVaultUnavailable",
			resolver:  &HandedTokenEnvelopeResolver{base: nil, kvMount: "secret", transitMount: "transit-tenants"},
			ref:       refA,
			principal: Principal{Service: "runtime-fleet", OrgID: orgA.String(), Token: "s.sometoken"},
			want:      ErrVaultUnavailable,
		},
		{
			name:      "handed-token resolver with no handed token -> ErrNoHandedToken",
			resolver:  &HandedTokenEnvelopeResolver{base: baseClient, kvMount: "secret", transitMount: "transit-tenants"},
			ref:       refA,
			principal: Principal{Service: "runtime-fleet", OrgID: orgA.String(), Token: ""},
			want:      ErrNoHandedToken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.resolver.Resolve(context.Background(), tt.ref, tt.principal)
			if err == nil {
				t.Fatal("Resolve returned nil error — every case here must fail closed")
			}

			// Bare match.
			if !errors.Is(err, tt.want) {
				t.Fatalf("errors.Is(err, %v) = false; err = %v", tt.want, err)
			}

			// Through a caller's wrap — the shape runtime-fleet's
			// resolveBootSecrets actually produces.
			wrapped := fmt.Errorf("resolve secret %q: %w", tt.ref, err)
			if !errors.Is(wrapped, tt.want) {
				t.Fatalf("sentinel lost through caller wrap: errors.Is(wrapped, %v) = false; wrapped = %v", tt.want, wrapped)
			}
		})
	}
}

// TestNoHandedTokenNeverReachesVault pins the fail-closed ordering: the two new
// sentinels are returned BEFORE any network call, so a missing token or a dead
// client can never be distinguished from a live one by timing or side effect.
func TestNoHandedTokenNeverReachesVault(t *testing.T) {
	baseClient, err := vault.NewClient(vault.DefaultConfig())
	if err != nil {
		t.Fatalf("build base vault client: %v", err)
	}
	r := &HandedTokenEnvelopeResolver{base: baseClient, kvMount: "secret", transitMount: "transit-tenants"}

	// Cross-tenant is refused before the token is even considered (I28,
	// oracle-free): a foreign caller must not learn whether a token was handed.
	_, err = r.Resolve(context.Background(), TenantRef(orgA, "prod/app", "db_password"),
		Principal{Service: "runtime-fleet", OrgID: orgB.String(), Token: ""})
	if !errors.Is(err, ErrCrossTenantSecret) {
		t.Fatalf("cross-tenant must win over missing-token; got %v", err)
	}
}
