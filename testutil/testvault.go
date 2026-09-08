package testutil

import (
	"context"
	"fmt"
	"testing"
	"time"

	vault "github.com/hashicorp/vault/api"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const vaultRootToken = "testutil-vault-root-token"

// PolicyTokenSelfManage is an ACL policy granting a token exactly the two
// capabilities it needs to manage its own lifetime — renew-self and
// revoke-self — and nothing else (notably not lookup-self).
//
// It is provided as a constant because "does a minted child actually hold
// this?" is the question a Vault token-role fixture exists to answer, and a
// hand-retyped policy body is one typo away from answering it wrongly.
const PolicyTokenSelfManage = `path "auth/token/renew-self" {
  capabilities = ["update"]
}

path "auth/token/revoke-self" {
  capabilities = ["update"]
}
`

// TokenRole is the subset of Vault's token-store role endpoint
// (auth/token/roles/<name>) that decides which policies a token minted through
// the role actually carries. Every field maps verbatim onto a real field of
// that endpoint.
//
// Only real fields are represented, deliberately: the endpoint accepts an
// unknown field with HTTP 2xx and silently drops it, so a role written with a
// field that merely looks right (token_policies, say, which belongs to auth
// methods and not to the token store) reads back as configured while minting
// powerless children.
type TokenRole struct {
	// AllowedPoliciesGlob is allowed_policies_glob — patterns a requested
	// policy may match.
	AllowedPoliciesGlob []string

	// AllowedPolicies is allowed_policies — the exact-match allowlist. A
	// policy matching neither this nor the glob is refused at mint time
	// rather than dropped from the token.
	AllowedPolicies []string

	// NoDefaultPolicy is token_no_default_policy. It must be true for any
	// negative capability assertion to mean anything: Vault's built-in
	// default policy itself grants renew-self and revoke-self, so a child
	// that keeps default revokes itself successfully however the allowlist
	// is configured, and a "cannot revoke itself" check that leaves default
	// in place can never fail.
	NoDefaultPolicy bool
}

// VaultResult holds connection details for a test Vault container.
type VaultResult struct {
	// Address is the HTTP API address (e.g. "http://localhost:12345").
	Address string
	// Token is the dev-mode root token.
	Token string
	// Client is a ready-to-use API client authenticated as root.
	Client *vault.Client
}

// NewTestVault starts a temporary dev-mode Vault container and returns
// connection details including a root-authenticated client. The container is
// terminated via t.Cleanup.
//
// Dev mode is unsealed, in-memory and HTTP-only — it is a disposable fixture
// for proving what Vault really does with a given configuration, never a model
// of a production deployment.
//
// Example — prove a token role mints children that can revoke themselves:
//
//	v := testutil.NewTestVault(t)
//	v.WritePolicy(t, "token-self-manage", testutil.PolicyTokenSelfManage)
//	v.WriteTokenRole(t, "deployment-tenant", testutil.TokenRole{
//	    AllowedPoliciesGlob: []string{"secret-tenant-*"},
//	    AllowedPolicies:     []string{"token-self-manage"},
//	    NoDefaultPolicy:     true,
//	})
//	child := v.CreateTokenWithRole(t, "deployment-tenant",
//	    []string{"secret-tenant-acme", "token-self-manage"})
//	err := v.ClientWithToken(t, child).Auth().Token().RevokeSelfWithContext(ctx, "")
func NewTestVault(t *testing.T) VaultResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	req := testcontainers.ContainerRequest{
		Image:        "hashicorp/vault:1.17",
		ExposedPorts: []string{"8200/tcp"},
		Env: map[string]string{
			"VAULT_DEV_ROOT_TOKEN_ID":  vaultRootToken,
			"VAULT_DEV_LISTEN_ADDRESS": "0.0.0.0:8200",
		},
		Cmd: []string{"server", "-dev"},
		WaitingFor: wait.ForHTTP("/v1/sys/health").WithPort("8200/tcp").
			WithStatusCodeMatcher(func(status int) bool { return status == 200 }).
			WithStartupTimeout(60 * time.Second),
	}

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("testutil.NewTestVault: start vault container: %v", err)
	}

	t.Cleanup(func() {
		if err := ctr.Terminate(context.Background()); err != nil {
			t.Logf("testutil.NewTestVault: terminate vault container: %v", err)
		}
	})

	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("testutil.NewTestVault: get host: %v", err)
	}

	mappedPort, err := ctr.MappedPort(ctx, "8200/tcp")
	if err != nil {
		t.Fatalf("testutil.NewTestVault: get mapped port: %v", err)
	}

	address := fmt.Sprintf("http://%s:%s", host, mappedPort.Port())

	client, err := newVaultClient(address, vaultRootToken)
	if err != nil {
		t.Fatalf("testutil.NewTestVault: create vault client: %v", err)
	}

	return VaultResult{
		Address: address,
		Token:   vaultRootToken,
		Client:  client,
	}
}

// WritePolicy writes an ACL policy, creating or replacing it.
func (v VaultResult) WritePolicy(t *testing.T, name, hcl string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := v.Client.Sys().PutPolicyWithContext(ctx, name, hcl); err != nil {
		t.Fatalf("testutil.VaultResult.WritePolicy %q: %v", name, err)
	}
}

// WriteTokenRole creates or replaces the named token-store role. Every field of
// TokenRole is sent on every call, so the write fully determines the role's
// policy configuration rather than merging with whatever was there before.
func (v VaultResult) WriteTokenRole(t *testing.T, name string, role TokenRole) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := v.Client.Logical().WriteWithContext(ctx, "auth/token/roles/"+name, map[string]any{
		"allowed_policies_glob":   nonNil(role.AllowedPoliciesGlob),
		"allowed_policies":        nonNil(role.AllowedPolicies),
		"token_no_default_policy": role.NoDefaultPolicy,
	})
	if err != nil {
		t.Fatalf("testutil.VaultResult.WriteTokenRole %q: %v", name, err)
	}
}

// CreateTokenWithRole mints a child token through the named role carrying the
// requested policies, and returns it.
//
// The mint is expected to succeed: a role that refuses the requested policies
// outright is a different failure from a role that mints a child lacking the
// capability it was supposed to have. Drive v.Client directly to assert on a
// refused mint.
func (v VaultResult) CreateTokenWithRole(t *testing.T, role string, policies []string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	secret, err := v.Client.Auth().Token().CreateWithRoleWithContext(ctx, &vault.TokenCreateRequest{
		Policies: policies,
	}, role)
	if err != nil {
		t.Fatalf("testutil.VaultResult.CreateTokenWithRole %q: %v", role, err)
	}
	token, err := secret.TokenID()
	if err != nil {
		t.Fatalf("testutil.VaultResult.CreateTokenWithRole %q: read token id: %v", role, err)
	}

	return token
}

// ClientWithToken returns a client for the same Vault authenticated as the
// given token, so a caller can exercise what that token can and cannot do.
func (v VaultResult) ClientWithToken(t *testing.T, token string) *vault.Client {
	t.Helper()

	client, err := newVaultClient(v.Address, token)
	if err != nil {
		t.Fatalf("testutil.VaultResult.ClientWithToken: %v", err)
	}

	return client
}

// newVaultClient builds a client pinned to address and token. The namespace is
// cleared explicitly because vault.NewClient adopts VAULT_NAMESPACE from the
// environment, which would silently redirect every request away from a
// dev-mode fixture on a developer machine that happens to export it.
func newVaultClient(address, token string) (*vault.Client, error) {
	cfg := vault.DefaultConfig()
	if cfg.Error != nil {
		return nil, fmt.Errorf("default vault config: %w", cfg.Error)
	}
	cfg.Address = address

	client, err := vault.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("new vault client: %w", err)
	}
	client.ClearNamespace()
	client.SetToken(token)

	return client, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
