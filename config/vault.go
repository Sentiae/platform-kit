package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	vault "github.com/hashicorp/vault/api"
	authk8s "github.com/hashicorp/vault/api/auth/kubernetes"
)

// VaultAuthMode controls how the VaultClient authenticates to the server.
type VaultAuthMode string

const (
	// VaultAuthToken uses a raw token (dev or CI). Read from VAULT_TOKEN.
	VaultAuthToken VaultAuthMode = "token"
	// VaultAuthAppRole uses the AppRole auth backend (prod rotation-friendly).
	// Requires VAULT_APPROLE_ROLE_ID and VAULT_APPROLE_SECRET_ID.
	VaultAuthAppRole VaultAuthMode = "approle"
	// VaultAuthKubernetes uses a Kubernetes service-account JWT (in-cluster).
	// Requires VAULT_K8S_ROLE and (optionally) VAULT_K8S_TOKEN_PATH.
	VaultAuthKubernetes VaultAuthMode = "kubernetes"
)

// VaultConfig holds connection + auth settings for a VaultClient.
type VaultConfig struct {
	Address    string        // e.g. "http://localhost:8200" — required.
	Namespace  string        // Vault Enterprise namespace (optional).
	AuthMode   VaultAuthMode // one of VaultAuth* (default: VaultAuthToken).
	Token      string        // used when AuthMode == VaultAuthToken.
	RoleID     string        // AppRole role_id.
	SecretID   string        // AppRole secret_id.
	K8sRole    string        // Kubernetes role name.
	K8sJWTPath string        // path to the projected SA token (default: /var/run/secrets/kubernetes.io/serviceaccount/token).
	MountPath  string        // KV v2 mount path (default: "secret").
	Timeout    time.Duration // per-request timeout (default: 10s).
}

// VaultClient is a thin wrapper over hashicorp/vault/api that focuses on the
// most common Sentiae-service use case: fetching secrets from a KV v2 mount.
type VaultClient struct {
	client    *vault.Client
	mountPath string
}

// NewVaultClient connects to Vault using the supplied configuration. It
// authenticates immediately; callers can reuse the returned client for
// subsequent GetSecret calls.
func NewVaultClient(ctx context.Context, cfg VaultConfig) (*VaultClient, error) {
	if cfg.Address == "" {
		return nil, errors.New("vault: address is required")
	}

	vcfg := vault.DefaultConfig()
	vcfg.Address = cfg.Address
	if cfg.Timeout > 0 {
		vcfg.Timeout = cfg.Timeout
	} else {
		vcfg.Timeout = 10 * time.Second
	}

	client, err := vault.NewClient(vcfg)
	if err != nil {
		return nil, fmt.Errorf("vault: create client: %w", err)
	}
	if cfg.Namespace != "" {
		client.SetNamespace(cfg.Namespace)
	}

	if err := authenticate(ctx, client, cfg); err != nil {
		return nil, err
	}

	mount := cfg.MountPath
	if mount == "" {
		mount = "secret"
	}

	return &VaultClient{client: client, mountPath: mount}, nil
}

// authenticate performs the initial login based on cfg.AuthMode. When the
// mode is empty it defaults to token auth.
func authenticate(ctx context.Context, client *vault.Client, cfg VaultConfig) error {
	mode := cfg.AuthMode
	if mode == "" {
		mode = VaultAuthToken
	}
	switch mode {
	case VaultAuthToken:
		if cfg.Token == "" {
			return errors.New("vault: token auth requires a token")
		}
		client.SetToken(cfg.Token)
		return nil

	case VaultAuthAppRole:
		if cfg.RoleID == "" || cfg.SecretID == "" {
			return errors.New("vault: approle auth requires role_id and secret_id")
		}
		secret, err := client.Logical().WriteWithContext(ctx, "auth/approle/login", map[string]any{
			"role_id":   cfg.RoleID,
			"secret_id": cfg.SecretID,
		})
		if err != nil {
			return fmt.Errorf("vault: approle login: %w", err)
		}
		if secret == nil || secret.Auth == nil {
			return errors.New("vault: approle login returned no auth info")
		}
		client.SetToken(secret.Auth.ClientToken)
		return nil

	case VaultAuthKubernetes:
		if cfg.K8sRole == "" {
			return errors.New("vault: kubernetes auth requires a role")
		}
		var opts []authk8s.LoginOption
		if cfg.K8sJWTPath != "" {
			opts = append(opts, authk8s.WithServiceAccountTokenPath(cfg.K8sJWTPath))
		}
		auth, err := authk8s.NewKubernetesAuth(cfg.K8sRole, opts...)
		if err != nil {
			return fmt.Errorf("vault: build k8s auth: %w", err)
		}
		secret, err := client.Auth().Login(ctx, auth)
		if err != nil {
			return fmt.Errorf("vault: k8s login: %w", err)
		}
		if secret == nil || secret.Auth == nil {
			return errors.New("vault: k8s login returned no auth info")
		}
		return nil

	default:
		return fmt.Errorf("vault: unsupported auth mode %q", mode)
	}
}

// GetSecret returns a single key from the secret stored at path. The path is
// interpreted relative to the configured KV v2 mount — callers pass the
// logical path (e.g. "identity/jwt") not the full physical path.
func (c *VaultClient) GetSecret(ctx context.Context, path, key string) (string, error) {
	data, err := c.GetSecrets(ctx, path)
	if err != nil {
		return "", err
	}
	val, ok := data[key]
	if !ok {
		return "", fmt.Errorf("vault: key %q not found at %s/%s", key, c.mountPath, path)
	}
	return val, nil
}

// GetSecrets returns every string-valued key stored at path. Non-string
// values are skipped; callers that need typed access should drop down to the
// underlying client via Raw().
func (c *VaultClient) GetSecrets(ctx context.Context, path string) (map[string]string, error) {
	secret, err := c.client.KVv2(c.mountPath).Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("vault: read %s/%s: %w", c.mountPath, path, err)
	}
	if secret == nil || secret.Data == nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(secret.Data))
	for k, v := range secret.Data {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out, nil
}

// PutSecret stores a single key/value pair under path, preserving any other
// keys already present at that location.
func (c *VaultClient) PutSecret(ctx context.Context, path, key, value string) error {
	existing, _ := c.GetSecrets(ctx, path)
	if existing == nil {
		existing = map[string]string{}
	}
	existing[key] = value
	data := make(map[string]any, len(existing))
	for k, v := range existing {
		data[k] = v
	}
	if _, err := c.client.KVv2(c.mountPath).Put(ctx, path, data); err != nil {
		return fmt.Errorf("vault: write %s/%s: %w", c.mountPath, path, err)
	}
	return nil
}

// Raw exposes the underlying Vault API client for callers that need access
// to auth backends or the logical API directly.
func (c *VaultClient) Raw() *vault.Client { return c.client }

// Close is a no-op today; it exists so callers can swap in implementations
// (e.g. tests) that need cleanup without changing their call-sites.
func (c *VaultClient) Close() error { return nil }

// NewFromEnv constructs a VaultClient from standard environment variables.
// It inspects VAULT_AUTH_MODE to decide which credential env vars are
// required:
//
//	VAULT_ADDR            required — server address.
//	VAULT_NAMESPACE       optional — enterprise namespace.
//	VAULT_AUTH_MODE       one of: token (default), approle, kubernetes.
//	VAULT_TOKEN           token mode.
//	VAULT_APPROLE_ROLE_ID + VAULT_APPROLE_SECRET_ID   approle mode.
//	VAULT_K8S_ROLE (+ VAULT_K8S_TOKEN_PATH)            kubernetes mode.
//	VAULT_KV_MOUNT        optional — KV v2 mount path (default: "secret").
func NewFromEnv(ctx context.Context) (*VaultClient, error) {
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		return nil, errors.New("vault: VAULT_ADDR is required")
	}
	cfg := VaultConfig{
		Address:    addr,
		Namespace:  os.Getenv("VAULT_NAMESPACE"),
		AuthMode:   VaultAuthMode(strings.ToLower(os.Getenv("VAULT_AUTH_MODE"))),
		Token:      os.Getenv("VAULT_TOKEN"),
		RoleID:     os.Getenv("VAULT_APPROLE_ROLE_ID"),
		SecretID:   os.Getenv("VAULT_APPROLE_SECRET_ID"),
		K8sRole:    os.Getenv("VAULT_K8S_ROLE"),
		K8sJWTPath: os.Getenv("VAULT_K8S_TOKEN_PATH"),
		MountPath:  os.Getenv("VAULT_KV_MOUNT"),
	}
	return NewVaultClient(ctx, cfg)
}
