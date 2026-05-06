package entitlement

import (
	"context"
	"sync"
	"time"
)

// Resolver translates an ents_hash claim into a concrete entitlement
// Set. Implementations typically combine an in-process LRU with a
// Redis-backed lookup (populated by identity-service) and a gRPC
// fallback to identity-service for cache misses.
type Resolver interface {
	Resolve(ctx context.Context, hash string) (*Set, error)
}

// MemoResolver wraps another Resolver with a process-local LRU-ish
// cache (here a small map with TTL) so the hot path stays in-process
// once the cache warms. Designed for the gRPC interceptor and HTTP
// middleware to avoid Redis fan-out per request.
//
// The cache is keyed by hash. TTL defaults to 5 minutes — short
// enough that entitlement changes propagate quickly without making
// every request hit Redis.
type MemoResolver struct {
	upstream Resolver
	ttl      time.Duration
	now      func() time.Time

	mu    sync.RWMutex
	cache map[string]memoEntry
}

type memoEntry struct {
	set       *Set
	expiresAt time.Time
}

// NewMemoResolver builds an in-process cache wrapping upstream.
// ttl=0 picks the 5-minute default. now=nil uses time.Now.
func NewMemoResolver(upstream Resolver, ttl time.Duration) *MemoResolver {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &MemoResolver{
		upstream: upstream,
		ttl:      ttl,
		now:      time.Now,
		cache:    make(map[string]memoEntry),
	}
}

// Resolve returns the cached Set, fetching from upstream on miss or
// expiry. A nil Set from upstream is cached for the same TTL to
// avoid hammering identity-service when a hash is genuinely unknown.
func (r *MemoResolver) Resolve(ctx context.Context, hash string) (*Set, error) {
	if hash == "" {
		return NewSet(nil), nil
	}
	now := r.now()

	r.mu.RLock()
	entry, ok := r.cache[hash]
	r.mu.RUnlock()
	if ok && entry.expiresAt.After(now) {
		return entry.set, nil
	}

	set, err := r.upstream.Resolve(ctx, hash)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.cache[hash] = memoEntry{set: set, expiresAt: now.Add(r.ttl)}
	r.mu.Unlock()
	return set, nil
}

// Invalidate drops the cache entry for the given hash. Identity-
// service can call this (via a separate signal channel) when a plan
// override changes; for now we rely on TTL expiry.
func (r *MemoResolver) Invalidate(hash string) {
	r.mu.Lock()
	delete(r.cache, hash)
	r.mu.Unlock()
}

// staticResolver is a Resolver that always returns the same set —
// useful for tests and dev-mode where entitlements come from config.
type staticResolver struct {
	set *Set
}

// NewStaticResolver returns a Resolver that returns set for every
// hash. Use only in tests or local dev.
func NewStaticResolver(ents []string) Resolver {
	return &staticResolver{set: NewSet(ents)}
}

func (s *staticResolver) Resolve(_ context.Context, _ string) (*Set, error) {
	return s.set, nil
}
