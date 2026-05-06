package embedding

import (
	"context"
	"sync"
)

// NoopCache satisfies Cache with no storage — every Get is a miss,
// every Set discards. Useful in tests and dev environments.
type NoopCache struct{}

func (NoopCache) Get(_ context.Context, _ string) (Vector, error) { return nil, nil }
func (NoopCache) Set(_ context.Context, _ string, _ Vector) error { return nil }

// MemoCache is an in-process map-backed Cache. Bounded by the
// MaxEntries field; on overflow it evicts a random entry (good
// enough for test/dev; production should use Redis).
type MemoCache struct {
	mu         sync.Mutex
	data       map[string]Vector
	MaxEntries int
}

// NewMemoCache builds a cache with the given upper bound. Pass 0
// for unbounded (don't do this in production).
func NewMemoCache(maxEntries int) *MemoCache {
	return &MemoCache{data: map[string]Vector{}, MaxEntries: maxEntries}
}

// Get returns (nil, nil) on miss.
func (c *MemoCache) Get(_ context.Context, key string) (Vector, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.data[key]
	if !ok {
		return nil, nil
	}
	out := make(Vector, len(v))
	copy(out, v)
	return out, nil
}

// Set stores a copy so callers can mutate their slice afterwards.
func (c *MemoCache) Set(_ context.Context, key string, v Vector) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.MaxEntries > 0 && len(c.data) >= c.MaxEntries {
		// Random eviction — Go's map iteration order is randomized.
		for k := range c.data {
			delete(c.data, k)
			break
		}
	}
	cp := make(Vector, len(v))
	copy(cp, v)
	c.data[key] = cp
	return nil
}

// Len returns the current entry count (test helper).
func (c *MemoCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.data)
}
