package entitlement

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type stubRedis struct {
	store map[string][]byte
	err   error
}

func (s *stubRedis) GetBytes(_ context.Context, key string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	v, ok := s.store[key]
	if !ok {
		return nil, ErrEntsRedisMiss
	}
	return v, nil
}

func TestRedisResolver_HitDecodesEnvelope(t *testing.T) {
	ents := []string{"search.inverted", "eve.basic"}
	payload, err := EncodeEntsPayload(ents)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRedisResolver(
		&stubRedis{store: map[string][]byte{RedisKey("h1"): payload}},
		NewStaticResolver(nil),
	)
	set, err := r.Resolve(context.Background(), "h1")
	if err != nil {
		t.Fatal(err)
	}
	if !set.Has(SearchInverted) || !set.Has(EveBasic) {
		t.Fatalf("expected hit ents, got %v", set.All())
	}
}

func TestRedisResolver_HitDecodesLegacyArray(t *testing.T) {
	raw, _ := json.Marshal([]string{"search.semantic"})
	r := NewRedisResolver(
		&stubRedis{store: map[string][]byte{RedisKey("h2"): raw}},
		nil,
	)
	set, err := r.Resolve(context.Background(), "h2")
	if err != nil {
		t.Fatal(err)
	}
	if !set.Has(SearchSemantic) {
		t.Fatalf("expected legacy array decode, got %v", set.All())
	}
}

func TestRedisResolver_MissFallsBackToUpstream(t *testing.T) {
	upstreamCalls := 0
	upstream := resolverFunc(func(_ context.Context, _ string) (*Set, error) {
		upstreamCalls++
		return NewSet([]string{string(EveFull)}), nil
	})
	r := NewRedisResolver(&stubRedis{store: map[string][]byte{}}, upstream)

	set, err := r.Resolve(context.Background(), "miss")
	if err != nil {
		t.Fatal(err)
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected upstream fallback, got %d calls", upstreamCalls)
	}
	if !set.Has(EveFull) {
		t.Fatalf("expected upstream set, got %v", set.All())
	}
}

func TestRedisResolver_TransientErrorFailsOpenByDefault(t *testing.T) {
	upstreamCalls := 0
	upstream := resolverFunc(func(_ context.Context, _ string) (*Set, error) {
		upstreamCalls++
		return NewSet(nil), nil
	})
	r := NewRedisResolver(&stubRedis{err: errors.New("redis down")}, upstream)
	if _, err := r.Resolve(context.Background(), "h"); err != nil {
		t.Fatalf("expected fail-open, got err %v", err)
	}
	if upstreamCalls != 1 {
		t.Fatal("upstream should be called on transient redis error")
	}
}

func TestRedisResolver_FailClosedSurfacesError(t *testing.T) {
	r := NewRedisResolver(&stubRedis{err: errors.New("redis down")}, NewStaticResolver(nil))
	r.FailClosed = true
	if _, err := r.Resolve(context.Background(), "h"); err == nil {
		t.Fatal("expected error in FailClosed mode")
	}
}

func TestRedisResolver_EmptyHashShortCircuits(t *testing.T) {
	upstream := resolverFunc(func(context.Context, string) (*Set, error) {
		t.Fatal("upstream should not be called for empty hash")
		return nil, nil
	})
	r := NewRedisResolver(&stubRedis{}, upstream)
	if _, err := r.Resolve(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
}

func TestRedisKey(t *testing.T) {
	if RedisKey("") != "" {
		t.Fatal("empty hash → empty key")
	}
	if got := RedisKey("abc"); got != "sentiae:ents:abc" {
		t.Fatalf("unexpected key %q", got)
	}
}

// resolverFunc is a tiny adapter so tests can pass a closure as a
// Resolver without writing a struct.
type resolverFunc func(ctx context.Context, hash string) (*Set, error)

func (f resolverFunc) Resolve(ctx context.Context, hash string) (*Set, error) {
	return f(ctx, hash)
}
