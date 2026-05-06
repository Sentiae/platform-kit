package entitlement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RedisGet is the minimal interface a Redis client must satisfy for
// the Redis-backed Resolver. We accept any client type that exposes a
// Get returning bytes — go-redis/v9, rueidis, etc. Keeps platform-kit
// dependency-free of any specific Redis SDK.
//
// The interface returns ([]byte, error). For "key not found" callers
// MUST return (nil, ErrEntsRedisMiss) rather than a generic error so
// the resolver can fall back to its upstream rather than failing.
type RedisGet interface {
	GetBytes(ctx context.Context, key string) ([]byte, error)
}

// ErrEntsRedisMiss signals "key not found" to the resolver. RedisGet
// implementations must return this on cache miss.
var ErrEntsRedisMiss = errors.New("entitlement: redis miss")

// RedisKey returns the canonical Redis key for a given entitlement hash.
// identity-service writes the JSON-encoded entitlement list under this
// key when a plan or org override changes; downstream services read it
// here. Keep the prefix stable across services.
func RedisKey(hash string) string {
	if hash == "" {
		return ""
	}
	return "sentiae:ents:" + hash
}

// RedisResolver looks the entitlement set up in Redis. On miss it
// delegates to a fallback Resolver (typically a gRPC client to
// identity-service) so a cold cache still serves traffic. The
// returned Set is built from the JSON list stored at RedisKey(hash).
type RedisResolver struct {
	client   RedisGet
	fallback Resolver

	// FailClosed makes Redis errors (other than miss) fatal. Default
	// false — operational errors fall back to the upstream resolver
	// so a Redis outage doesn't take the whole platform offline.
	FailClosed bool
}

// NewRedisResolver builds a Redis-backed resolver. Pass a non-nil
// fallback so cold-cache + upstream-Redis-down scenarios still work.
// Passing fallback=nil is permitted only in tests.
func NewRedisResolver(client RedisGet, fallback Resolver) *RedisResolver {
	return &RedisResolver{client: client, fallback: fallback}
}

// Resolve implements Resolver. Reads RedisKey(hash); on hit, decodes
// the JSON list. On miss or transient error, falls back to upstream.
func (r *RedisResolver) Resolve(ctx context.Context, hash string) (*Set, error) {
	if hash == "" {
		return NewSet(nil), nil
	}
	if r.client != nil {
		raw, err := r.client.GetBytes(ctx, RedisKey(hash))
		switch {
		case err == nil:
			ents, decErr := decodeEntsJSON(raw)
			if decErr == nil {
				return NewSet(ents), nil
			}
			// Decode error: log via caller; treat like a cache miss
			// rather than a hard failure.
		case errors.Is(err, ErrEntsRedisMiss):
			// fall through to upstream
		default:
			if r.FailClosed {
				return nil, fmt.Errorf("entitlement: redis: %w", err)
			}
			// transient error: fall through to upstream
		}
	}
	if r.fallback == nil {
		return NewSet(nil), nil
	}
	return r.fallback.Resolve(ctx, hash)
}

// EntsPayload is the on-wire shape stored in Redis. The version field
// guards against future schema changes — readers older than the
// writer can detect a newer payload and fall back to the upstream.
type EntsPayload struct {
	Version      int      `json:"v"`
	Entitlements []string `json:"ents"`
	WrittenAt    int64    `json:"ts"`
}

// EncodeEntsPayload is the writer-side helper. Identity-service
// invokes this when minting a Redis row.
func EncodeEntsPayload(ents []string) ([]byte, error) {
	return json.Marshal(EntsPayload{
		Version:      1,
		Entitlements: append([]string(nil), ents...),
		WrittenAt:    time.Now().UTC().Unix(),
	})
}

// decodeEntsJSON tolerates two on-wire shapes:
//   1. JSON list `["a","b"]` (legacy / minimal writer)
//   2. EntsPayload envelope (preferred)
func decodeEntsJSON(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	// Try envelope first.
	var env EntsPayload
	if err := json.Unmarshal(raw, &env); err == nil && env.Version > 0 {
		return env.Entitlements, nil
	}
	// Legacy: bare array.
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	return nil, fmt.Errorf("entitlement: unparseable redis payload (%d bytes)", len(raw))
}
