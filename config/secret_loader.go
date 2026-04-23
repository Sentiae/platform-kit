package config

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
)

// SecretLoader is a thin abstraction over "fetch a named secret" that lets
// services transparently swap between:
//   - plain environment variables (local dev),
//   - a HashiCorp Vault KV v2 mount (staging / prod),
//   - any caller-supplied source implementing the interface (tests / mocks).
//
// Service config code should never reach into `os.Getenv` directly for
// sensitive values (DB passwords, API tokens, OAuth secrets, JWT signing
// keys). Route through a SecretLoader instead so the same binary runs against
// either source with no code changes — this closes the Sentiae MEDIUM C1
// "migrate secrets to Vault" item.
//
// Contract:
//   - Get returns ("", nil) when the secret does not exist. A non-nil error
//     is reserved for transport failures (Vault unreachable, auth denied).
//   - Implementations MUST be safe for concurrent use.
type SecretLoader interface {
	Get(ctx context.Context, key string) (string, error)
}

// EnvSecretLoader reads secrets from process environment variables. Keys are
// uppercased and dots replaced with underscores to match the APP_* pattern
// used across sentiae services (same rule Viper applies).
type EnvSecretLoader struct {
	Prefix string // optional prefix (e.g. "APP"). Applied with an underscore.
}

// Get returns the env var corresponding to key. "database.password" with
// prefix "APP" resolves to APP_DATABASE_PASSWORD.
func (l *EnvSecretLoader) Get(_ context.Context, key string) (string, error) {
	envKey := strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
	if l.Prefix != "" {
		envKey = strings.ToUpper(l.Prefix) + "_" + envKey
	}
	return os.Getenv(envKey), nil
}

// VaultSecretLoader wraps a VaultClient so the caller-side API matches
// EnvSecretLoader. A key shaped as "path/to/secret#field" pulls the named
// field out of a KV v2 secret; "simple_key" is looked up under a configured
// default path (cfg.DefaultPath).
type VaultSecretLoader struct {
	Client      *VaultClient
	DefaultPath string // e.g. "services/identity" — used when key has no "#" separator.

	// cache avoids hammering Vault on every config re-read. It's keyed by the
	// logical path + field and invalidated only by process restart (secret
	// rotation should ship a restart in the deployment).
	mu    sync.RWMutex
	cache map[string]string
}

// NewVaultSecretLoader wraps an authenticated client. Returns nil when the
// client is nil so callers can write `NewVaultSecretLoader(nil) == nil`
// fallback chains.
func NewVaultSecretLoader(client *VaultClient, defaultPath string) *VaultSecretLoader {
	if client == nil {
		return nil
	}
	return &VaultSecretLoader{
		Client:      client,
		DefaultPath: defaultPath,
		cache:       make(map[string]string),
	}
}

// Get fetches the secret. Cache hits never touch Vault.
func (l *VaultSecretLoader) Get(ctx context.Context, key string) (string, error) {
	if l == nil || l.Client == nil {
		return "", nil
	}
	path, field := splitVaultKey(key, l.DefaultPath)
	cacheKey := path + "#" + field

	l.mu.RLock()
	if v, ok := l.cache[cacheKey]; ok {
		l.mu.RUnlock()
		return v, nil
	}
	l.mu.RUnlock()

	v, err := l.Client.GetSecret(ctx, path, field)
	if err != nil {
		return "", err
	}
	l.mu.Lock()
	l.cache[cacheKey] = v
	l.mu.Unlock()
	return v, nil
}

// splitVaultKey resolves "a/b#field" → ("a/b", "field"); bare keys fall back
// to the default path, with the key as the field name.
func splitVaultKey(key, defaultPath string) (path, field string) {
	if idx := strings.LastIndex(key, "#"); idx > 0 {
		return key[:idx], key[idx+1:]
	}
	return defaultPath, key
}

// ChainSecretLoader tries loaders in order and returns the first non-empty
// value. Useful for "Vault if configured, else env" fallback chains.
type ChainSecretLoader struct {
	Loaders []SecretLoader
}

// Get walks the chain.
func (c *ChainSecretLoader) Get(ctx context.Context, key string) (string, error) {
	for _, l := range c.Loaders {
		if l == nil {
			continue
		}
		v, err := l.Get(ctx, key)
		if err != nil {
			// Keep trying other loaders but log the failure so ops can see it.
			log.Printf("secret-loader: %T failed on %q: %v", l, key, err)
			continue
		}
		if v != "" {
			return v, nil
		}
	}
	return "", nil
}

// StaticSecretLoader is a test helper: a map-backed loader.
type StaticSecretLoader map[string]string

// Get returns s[key].
func (s StaticSecretLoader) Get(_ context.Context, key string) (string, error) {
	return s[key], nil
}

// NewDefaultSecretLoader is the convenience factory services call from their
// pkg/config loader. The logic is:
//   - If VAULT_ADDR is set, try to authenticate and wrap a VaultSecretLoader
//     with env as fallback.
//   - Otherwise return an env-only loader.
//
// defaultPath is the KV v2 path used when callers pass bare keys without a
// "#field" separator (e.g. "services/identity").
//
// Errors during Vault bring-up are logged, not returned, so local dev never
// fails open on a missing Vault.
func NewDefaultSecretLoader(ctx context.Context, envPrefix, defaultVaultPath string) SecretLoader {
	envLoader := &EnvSecretLoader{Prefix: envPrefix}
	if os.Getenv("VAULT_ADDR") == "" {
		return envLoader
	}
	client, err := NewFromEnv(ctx)
	if err != nil {
		log.Printf("secret-loader: VAULT_ADDR set but auth failed: %v (falling back to env-only)", err)
		return envLoader
	}
	log.Printf("secret-loader: authenticated to Vault at %s (kv mount=%s, default path=%s)",
		os.Getenv("VAULT_ADDR"), orDefault(os.Getenv("VAULT_KV_MOUNT"), "secret"), defaultVaultPath)
	return &ChainSecretLoader{
		Loaders: []SecretLoader{
			NewVaultSecretLoader(client, defaultVaultPath),
			envLoader,
		},
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
