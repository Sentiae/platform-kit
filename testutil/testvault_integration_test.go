//go:build integration

package testutil

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	vault "github.com/hashicorp/vault/api"
)

// TestVaultTokenRoleSelfRevocation drives NewTestVault through both directions
// of the capability question a token role decides: can a child minted through
// this role revoke itself?
//
// The role is shaped like a real per-tenant deployment role — a glob over the
// tenant secret policies plus an explicit allowlist entry for the self-manage
// policy. The tenant policy itself is never written, and that is not an
// oversight: Vault mints a token bearing a policy that does not exist, the
// policy simply grants nothing. Only the allowlist decides what a child may
// ask for.
func TestVaultTokenRoleSelfRevocation(t *testing.T) {
	const (
		roleName         = "deployment-tenant"
		selfManagePolicy = "token-self-manage"
		tenantPolicyGlob = "secret-tenant-*"
		tenantPolicy     = "secret-tenant-acme"
	)

	ctx := context.Background()
	v := NewTestVault(t)
	v.WritePolicy(t, selfManagePolicy, PolicyTokenSelfManage)

	t.Run("self-manage allowlisted: the child revokes itself", func(t *testing.T) {
		v.WriteTokenRole(t, roleName, TokenRole{
			AllowedPoliciesGlob: []string{tenantPolicyGlob},
			AllowedPolicies:     []string{selfManagePolicy},
			NoDefaultPolicy:     true,
		})

		child := v.CreateTokenWithRole(t, roleName, []string{tenantPolicy, selfManagePolicy})
		got := lookupPolicies(ctx, t, v.Client, child)
		if !slices.Contains(got, selfManagePolicy) {
			t.Fatalf("child policies = %v, want %q among them", got, selfManagePolicy)
		}
		t.Logf("child policies = %v", got)

		if err := v.ClientWithToken(t, child).Auth().Token().RevokeSelfWithContext(ctx, ""); err != nil {
			t.Fatalf("revoke-self: want success, got %v", err)
		}

		// A permitted call that did nothing would be indistinguishable from a
		// revocation, so confirm from the root side that the token is gone.
		if _, err := v.Client.Auth().Token().LookupWithContext(ctx, child); err == nil {
			t.Fatal("root lookup of the child still succeeds — revoke-self returned success but did not revoke")
		}
		t.Log("revoke-self succeeded and the child no longer resolves")
	})

	t.Run("self-manage NOT allowlisted: the child cannot revoke itself", func(t *testing.T) {
		v.WriteTokenRole(t, roleName, TokenRole{
			AllowedPoliciesGlob: []string{tenantPolicyGlob},
			AllowedPolicies:     nil,
			NoDefaultPolicy:     true,
		})

		// The mint still succeeds — this is the whole shape of the defect. The
		// role looks configured, children come back with HTTP 200, and only a
		// capability check finds that they are powerless.
		child := v.CreateTokenWithRole(t, roleName, []string{tenantPolicy})
		got := lookupPolicies(ctx, t, v.Client, child)
		if slices.Contains(got, selfManagePolicy) {
			t.Fatalf("child policies = %v, want %q absent", got, selfManagePolicy)
		}
		t.Logf("child policies = %v", got)

		err := v.ClientWithToken(t, child).Auth().Token().RevokeSelfWithContext(ctx, "")
		if err == nil {
			t.Fatal("revoke-self: want permission denied, got success")
		}
		var respErr *vault.ResponseError
		if !errors.As(err, &respErr) || respErr.StatusCode != http.StatusForbidden {
			t.Fatalf("revoke-self: want HTTP 403, got %v", err)
		}
		t.Logf("revoke-self refused as expected: HTTP %d %v", respErr.StatusCode, respErr.Errors)
	})

	t.Run("default policy kept: the negative cannot fail", func(t *testing.T) {
		// Not a capability the fixture offers — the trap it must not walk into.
		// Vault's built-in default policy grants renew-self and revoke-self on
		// its own, so with NoDefaultPolicy left false a child revokes itself
		// however the allowlist is configured, and the subtest above would pass
		// while proving nothing.
		v.WriteTokenRole(t, roleName, TokenRole{
			AllowedPoliciesGlob: []string{tenantPolicyGlob},
			AllowedPolicies:     nil,
			NoDefaultPolicy:     false,
		})

		child := v.CreateTokenWithRole(t, roleName, []string{tenantPolicy})
		got := lookupPolicies(ctx, t, v.Client, child)
		if !slices.Contains(got, "default") {
			t.Fatalf("child policies = %v, want \"default\" among them", got)
		}

		if err := v.ClientWithToken(t, child).Auth().Token().RevokeSelfWithContext(ctx, ""); err != nil {
			t.Fatalf("revoke-self via the default policy: want success, got %v", err)
		}
		t.Logf("child policies = %v — revoke-self succeeded via \"default\" despite an empty allowlist", got)
	})
}

// lookupPolicies reads a token's real policy set from the root side. It cannot
// be asked of the token itself: the self-manage policy grants renew-self and
// revoke-self only, so a child holding it has no lookup-self.
func lookupPolicies(ctx context.Context, t *testing.T, root *vault.Client, token string) []string {
	t.Helper()

	secret, err := root.Auth().Token().LookupWithContext(ctx, token)
	if err != nil {
		t.Fatalf("lookup child token: %v", err)
	}
	raw, ok := secret.Data["policies"].([]any)
	if !ok {
		t.Fatalf("lookup child token: policies = %#v, want a list", secret.Data["policies"])
	}
	policies := make([]string, 0, len(raw))
	for _, p := range raw {
		s, ok := p.(string)
		if !ok {
			t.Fatalf("lookup child token: policy %#v is not a string", p)
		}
		policies = append(policies, s)
	}

	return policies
}
