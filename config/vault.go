package config

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	vault "github.com/hashicorp/vault/api"
	authk8s "github.com/hashicorp/vault/api/auth/kubernetes"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"

	"github.com/sentiae/platform-kit/logger"
	"github.com/sentiae/platform-kit/spiffe"
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
	// VaultAuthSVID uses a SPIFFE JWT-SVID (audience "vault") presented to
	// Vault's jwt auth backend — no stored secret (satisfies I33). Requires
	// VAULT_SVID_ROLE and a running SPIFFE Workload API (SPIFFE_ENDPOINT_SOCKET).
	VaultAuthSVID VaultAuthMode = "svid"
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
	SVIDRole   string        // Vault jwt-backend role name (used when AuthMode == VaultAuthSVID).
	MountPath  string        // KV v2 mount path (default: "secret").
	Timeout    time.Duration // per-request timeout (default: 10s).
	// AutoRenew keeps the login lease alive in the background for approle,
	// kubernetes and svid modes (a renewable lease). NewFromEnv defaults it to
	// true for those modes and false for token mode (token mode has no lease).
	// It is a no-op for token mode and for non-renewable leases.
	AutoRenew bool

	// x509Src and jwtSrc are the SPIFFE Workload API source handles for svid
	// mode. NewVaultClient creates them; authenticate reads jwtSrc to fetch a
	// fresh JWT-SVID on every (re-)login; Close closes both. They are nil for
	// all non-svid modes.
	x509Src *workloadapi.X509Source
	jwtSrc  *workloadapi.JWTSource
}

// VaultClient is a thin wrapper over hashicorp/vault/api that focuses on the
// most common Sentiae-service use case: fetching secrets from a KV v2 mount.
type VaultClient struct {
	client    *vault.Client
	mountPath string
	cfg       VaultConfig

	// loginSecret holds the *api.Secret returned by an approle/kubernetes
	// login (the lease with ClientToken/Renewable/LeaseDuration). It is nil in
	// token mode.
	loginSecret *vault.Secret

	// renewer coordination. renewCancel is nil when no renewer is running.
	renewMu     sync.Mutex
	renewCancel context.CancelFunc
	watcher     *vault.LifetimeWatcher
	closeOnce   sync.Once
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

	// svid mode: bring up the SPIFFE Workload API sources before the api client
	// so the client can verify Vault's SPIFFE server cert on https. The sources
	// are stored on cfg (authenticate reads jwtSrc; Close closes both).
	if cfg.AuthMode == VaultAuthSVID {
		x509Src, err := spiffe.NewSource(ctx)
		if err != nil {
			return nil, fmt.Errorf("vault: create spiffe x509 source: %w", err)
		}
		jwtSrc, err := spiffe.NewJWTSource(ctx)
		if err != nil {
			_ = x509Src.Close()
			return nil, fmt.Errorf("vault: create spiffe jwt source: %w", err)
		}
		cfg.x509Src = x509Src
		cfg.jwtSrc = jwtSrc
		if strings.HasPrefix(strings.ToLower(cfg.Address), "https://") {
			tr, ok := vcfg.HttpClient.Transport.(*http.Transport)
			if !ok {
				_ = x509Src.Close()
				_ = jwtSrc.Close()
				return nil, errors.New("vault: cannot wire spiffe server tls: unexpected http transport type")
			}
			tr.TLSClientConfig = spiffe.VaultServerTLS(x509Src)
		}
	}

	client, err := vault.NewClient(vcfg)
	if err != nil {
		if cfg.x509Src != nil {
			_ = cfg.x509Src.Close()
		}
		if cfg.jwtSrc != nil {
			_ = cfg.jwtSrc.Close()
		}
		return nil, fmt.Errorf("vault: create client: %w", err)
	}
	if cfg.Namespace != "" {
		client.SetNamespace(cfg.Namespace)
	}

	loginSecret, err := authenticate(ctx, client, cfg)
	if err != nil {
		if cfg.x509Src != nil {
			_ = cfg.x509Src.Close()
		}
		if cfg.jwtSrc != nil {
			_ = cfg.jwtSrc.Close()
		}
		return nil, err
	}

	mount := cfg.MountPath
	if mount == "" {
		mount = "secret"
	}

	c := &VaultClient{
		client:      client,
		mountPath:   mount,
		cfg:         cfg,
		loginSecret: loginSecret,
	}

	if cfg.AutoRenew {
		c.startRenewer(ctx)
	}

	return c, nil
}

// authenticate performs the initial login based on cfg.AuthMode. When the
// mode is empty it defaults to token auth. It returns the *api.Secret carrying
// the login lease for approle/kubernetes modes (nil for token mode, which has
// no lease).
func authenticate(ctx context.Context, client *vault.Client, cfg VaultConfig) (*vault.Secret, error) {
	mode := cfg.AuthMode
	if mode == "" {
		mode = VaultAuthToken
	}
	switch mode {
	case VaultAuthToken:
		if cfg.Token == "" {
			return nil, errors.New("vault: token auth requires a token")
		}
		client.SetToken(cfg.Token)
		return nil, nil

	case VaultAuthAppRole:
		if cfg.RoleID == "" || cfg.SecretID == "" {
			return nil, errors.New("vault: approle auth requires role_id and secret_id")
		}
		secret, err := client.Logical().WriteWithContext(ctx, "auth/approle/login", map[string]any{
			"role_id":   cfg.RoleID,
			"secret_id": cfg.SecretID,
		})
		if err != nil {
			return nil, fmt.Errorf("vault: approle login: %w", err)
		}
		if secret == nil || secret.Auth == nil {
			return nil, errors.New("vault: approle login returned no auth info")
		}
		client.SetToken(secret.Auth.ClientToken)
		return secret, nil

	case VaultAuthKubernetes:
		if cfg.K8sRole == "" {
			return nil, errors.New("vault: kubernetes auth requires a role")
		}
		var opts []authk8s.LoginOption
		if cfg.K8sJWTPath != "" {
			opts = append(opts, authk8s.WithServiceAccountTokenPath(cfg.K8sJWTPath))
		}
		auth, err := authk8s.NewKubernetesAuth(cfg.K8sRole, opts...)
		if err != nil {
			return nil, fmt.Errorf("vault: build k8s auth: %w", err)
		}
		secret, err := client.Auth().Login(ctx, auth)
		if err != nil {
			return nil, fmt.Errorf("vault: k8s login: %w", err)
		}
		if secret == nil || secret.Auth == nil {
			return nil, errors.New("vault: k8s login returned no auth info")
		}
		return secret, nil

	case VaultAuthSVID:
		if cfg.SVIDRole == "" {
			return nil, errors.New("vault: svid auth requires a role")
		}
		if cfg.jwtSrc == nil {
			return nil, errors.New("vault: svid auth requires a jwt source")
		}
		svid, err := cfg.jwtSrc.FetchJWTSVID(ctx, jwtsvid.Params{Audience: "vault"})
		if err != nil {
			return nil, fmt.Errorf("vault: fetch jwt-svid: %w", err)
		}
		secret, err := client.Logical().WriteWithContext(ctx, "auth/jwt/login", map[string]any{
			"role": cfg.SVIDRole,
			"jwt":  svid.Marshal(),
		})
		if err != nil {
			return nil, fmt.Errorf("vault: svid login: %w", err)
		}
		if secret == nil || secret.Auth == nil {
			return nil, errors.New("vault: svid login returned no auth info")
		}
		client.SetToken(secret.Auth.ClientToken)
		return secret, nil

	default:
		return nil, fmt.Errorf("vault: unsupported auth mode %q", mode)
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

// startRenewer launches the background lease renewer for a renewable
// approle/kubernetes login. It is a no-op when there is no renewable lease
// (token mode, or a lease the server marked non-renewable). The renewer runs
// on a background context so it survives past the caller's boot context; Close
// cancels it. The inbound ctx supplies only the logger.
func (c *VaultClient) startRenewer(ctx context.Context) {
	if c.loginSecret == nil || c.loginSecret.Auth == nil || !c.loginSecret.Auth.Renewable {
		return
	}
	l := logger.FromContext(ctx)
	renewCtx, cancel := context.WithCancel(context.Background())
	renewCtx = logger.NewContext(renewCtx, l)

	c.renewMu.Lock()
	c.renewCancel = cancel
	c.renewMu.Unlock()

	go c.renewLoop(renewCtx)
}

// renewLoop keeps the login lease alive. For AppRole with a server-side
// token_period, renewal resets to the period indefinitely, so DoneCh is the
// exceptional path; when it fires the loop attempts a full re-login and
// restarts the watcher. It never panics and never crashes the process.
func (c *VaultClient) renewLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			logger.FromContext(ctx).Error("vault: token renewer panicked", "recover", r)
		}
	}()

	for {
		c.renewMu.Lock()
		secret := c.loginSecret
		c.renewMu.Unlock()
		if secret == nil || secret.Auth == nil || !secret.Auth.Renewable {
			logger.FromContext(ctx).Warn("vault: login lease not renewable; auto-renewal stopping")
			return
		}

		watcher, err := c.client.NewLifetimeWatcher(&vault.LifetimeWatcherInput{Secret: secret})
		if err != nil {
			logger.FromContext(ctx).Error("vault: create lifetime watcher failed; attempting re-login", "err", err)
			if !c.reauth(ctx) {
				return
			}
			continue
		}

		c.renewMu.Lock()
		c.watcher = watcher
		c.renewMu.Unlock()

		go watcher.Start()
		logger.FromContext(ctx).Info("vault: token auto-renewal started", "auth_mode", string(c.cfg.AuthMode))

		reauth := c.consumeRenewals(ctx, watcher)
		watcher.Stop()
		if !reauth {
			// Context cancelled via Close — stop for good.
			return
		}
		logger.FromContext(ctx).Warn("vault: token renewal ended; attempting re-login")
		if !c.reauth(ctx) {
			return
		}
	}
}

// consumeRenewals drains a watcher's channels until either the context is
// cancelled (returns false) or renewal permanently ends on DoneCh (returns
// true, signalling the caller to re-login).
func (c *VaultClient) consumeRenewals(ctx context.Context, w *vault.LifetimeWatcher) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case err := <-w.DoneCh():
			if err != nil {
				logger.FromContext(ctx).Warn("vault: lease renewal stopped with error", "err", err)
			}
			return true
		case <-w.RenewCh():
			logger.FromContext(ctx).Debug("vault: lease renewed")
		}
	}
}

// reauth performs a full re-login and replaces the stored lease. It retries
// with exponential backoff (capped) until it succeeds or the context is
// cancelled. Returns false only when the context is cancelled.
func (c *VaultClient) reauth(ctx context.Context) bool {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return false
		}
		secret, err := authenticate(ctx, c.client, c.cfg)
		if err == nil && secret != nil && secret.Auth != nil {
			c.renewMu.Lock()
			c.loginSecret = secret
			c.renewMu.Unlock()
			logger.FromContext(ctx).Info("vault: re-login succeeded; resuming auto-renewal")
			return true
		}
		logger.FromContext(ctx).Error("vault: re-login failed; will retry", "err", err)
		if !sleepCtx(ctx, backoff) {
			return false
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// sleepCtx waits for d or until ctx is cancelled. Returns false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Raw exposes the underlying Vault API client for callers that need access
// to auth backends or the logical API directly.
func (c *VaultClient) Raw() *vault.Client { return c.client }

// Close cancels the background token renewer (if any) and stops its watcher.
// It is idempotent and safe to call on a token-mode client (no renewer).
func (c *VaultClient) Close() error {
	c.closeOnce.Do(func() {
		c.renewMu.Lock()
		cancel := c.renewCancel
		w := c.watcher
		c.renewMu.Unlock()
		if cancel != nil {
			cancel()
		}
		if w != nil {
			w.Stop()
		}
		if c.cfg.jwtSrc != nil {
			_ = c.cfg.jwtSrc.Close()
		}
		if c.cfg.x509Src != nil {
			_ = c.cfg.x509Src.Close()
		}
	})
	return nil
}

// envOrFile returns the value of the environment variable named name when it
// is non-empty. Otherwise, if the companion variable name+"_FILE" points at a
// readable file, it returns that file's trimmed contents (the Docker-secrets
// convention). If neither yields a value it returns "".
func envOrFile(name string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	if path := os.Getenv(name + "_FILE"); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

// NewFromEnv constructs a VaultClient from standard environment variables.
// It inspects VAULT_AUTH_MODE to decide which credential env vars are
// required:
//
//	VAULT_ADDR            required — server address.
//	VAULT_NAMESPACE       optional — enterprise namespace.
//	VAULT_AUTH_MODE       one of: token (default), approle, kubernetes, svid.
//	VAULT_TOKEN           token mode.
//	VAULT_APPROLE_ROLE_ID + VAULT_APPROLE_SECRET_ID   approle mode.
//	VAULT_K8S_ROLE (+ VAULT_K8S_TOKEN_PATH)            kubernetes mode.
//	VAULT_SVID_ROLE       svid mode (Vault jwt-backend role; needs a SPIFFE socket).
//	VAULT_KV_MOUNT        optional — KV v2 mount path (default: "secret").
//
// The credential vars VAULT_TOKEN, VAULT_APPROLE_ROLE_ID and
// VAULT_APPROLE_SECRET_ID each also accept a "<NAME>_FILE" variant that names
// a file whose trimmed contents supply the value (the Docker-secrets
// convention). The direct env var wins when both are set.
func NewFromEnv(ctx context.Context) (*VaultClient, error) {
	cfg, err := configFromEnv()
	if err != nil {
		return nil, err
	}
	return NewVaultClient(ctx, cfg)
}

// configFromEnv reads the VAULT_* environment variables into a VaultConfig
// without connecting. It is the pure, side-effect-free half of NewFromEnv,
// split out so the env-parsing + mode-selection logic is unit-testable without
// a live SPIRE/Vault.
func configFromEnv() (VaultConfig, error) {
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		return VaultConfig{}, errors.New("vault: VAULT_ADDR is required")
	}
	mode := VaultAuthMode(strings.ToLower(os.Getenv("VAULT_AUTH_MODE")))
	cfg := VaultConfig{
		Address:    addr,
		Namespace:  os.Getenv("VAULT_NAMESPACE"),
		AuthMode:   mode,
		Token:      envOrFile("VAULT_TOKEN"),
		RoleID:     envOrFile("VAULT_APPROLE_ROLE_ID"),
		SecretID:   envOrFile("VAULT_APPROLE_SECRET_ID"),
		K8sRole:    os.Getenv("VAULT_K8S_ROLE"),
		K8sJWTPath: os.Getenv("VAULT_K8S_TOKEN_PATH"),
		SVIDRole:   os.Getenv("VAULT_SVID_ROLE"),
		MountPath:  os.Getenv("VAULT_KV_MOUNT"),
		// Default: renew the lease for approle/kubernetes/svid; token mode has
		// none. VAULT_AUTO_RENEW ("true"/"false") overrides the default.
		AutoRenew: mode == VaultAuthAppRole || mode == VaultAuthKubernetes || mode == VaultAuthSVID,
	}
	if v := os.Getenv("VAULT_AUTO_RENEW"); v != "" {
		cfg.AutoRenew = strings.EqualFold(v, "true") || v == "1"
	}
	return cfg, nil
}
