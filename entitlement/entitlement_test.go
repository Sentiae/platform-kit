package entitlement

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHashSet_NormalizesAndDedupes(t *testing.T) {
	a := HashSet([]string{"search.semantic", "Search.Semantic", "  search.semantic  ", "eve.full"})
	b := HashSet([]string{"eve.full", "search.semantic"})
	if a != b {
		t.Fatalf("expected equal hashes after normalization\n  a=%s\n  b=%s", a, b)
	}
	if a == HashSet(nil) {
		t.Fatalf("non-empty set should not equal empty hash")
	}
}

func TestHashSet_OrderIndependent(t *testing.T) {
	a := HashSet([]string{"a", "b", "c"})
	b := HashSet([]string{"c", "a", "b"})
	if a != b {
		t.Fatalf("hash must be order-independent: %s vs %s", a, b)
	}
}

func TestSet_Has(t *testing.T) {
	s := NewSet([]string{string(SearchInverted), string(EveBasic)})
	if !s.Has(SearchInverted) {
		t.Fatal("expected search.inverted")
	}
	if s.Has(SearchSemantic) {
		t.Fatal("did not expect search.semantic")
	}
}

func TestSet_NilSafe(t *testing.T) {
	var s *Set
	if s.Has(SearchSemantic) {
		t.Fatal("nil set must report false")
	}
	if !s.Empty() {
		t.Fatal("nil set must be empty")
	}
	if got := s.All(); got != nil {
		t.Fatalf("expected nil All() for nil set, got %v", got)
	}
}

func TestFromContext_EmptyByDefault(t *testing.T) {
	if HasEntitlement(context.Background(), SearchSemantic) {
		t.Fatal("background ctx must not have entitlements")
	}
}

func TestFromContext_WithSet(t *testing.T) {
	ctx := WithSet(context.Background(), NewSet([]string{string(EveFull)}))
	if !HasEntitlement(ctx, EveFull) {
		t.Fatal("expected eve.full")
	}
	if HasEntitlement(ctx, GenesisEngine) {
		t.Fatal("did not expect genesis_engine")
	}
}

func TestHashContext_RoundTrip(t *testing.T) {
	ctx := WithHash(context.Background(), "abc123")
	if HashFromContext(ctx) != "abc123" {
		t.Fatal("hash round-trip failed")
	}
	if HashFromContext(WithHash(context.Background(), "")) != "" {
		t.Fatal("empty hash should remain empty")
	}
}

// --- Resolver tests --------------------------------------------------

type stubResolver struct {
	set    *Set
	calls  int
	err    error
	freeze chan struct{}
}

func (s *stubResolver) Resolve(_ context.Context, _ string) (*Set, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if s.freeze != nil {
		<-s.freeze
	}
	return s.set, nil
}

func TestMemoResolver_CachesAndExpires(t *testing.T) {
	stub := &stubResolver{set: NewSet([]string{string(EveBasic)})}
	now := time.Unix(0, 0)
	r := &MemoResolver{
		upstream: stub,
		ttl:      10 * time.Second,
		now:      func() time.Time { return now },
		cache:    map[string]memoEntry{},
	}

	// first call → upstream
	if _, err := r.Resolve(context.Background(), "h1"); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", stub.calls)
	}

	// second call → cache hit
	if _, err := r.Resolve(context.Background(), "h1"); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 1 {
		t.Fatalf("expected cache hit, got %d", stub.calls)
	}

	// advance past TTL → upstream again
	now = now.Add(11 * time.Second)
	if _, err := r.Resolve(context.Background(), "h1"); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 2 {
		t.Fatalf("expected expiry refetch, got %d", stub.calls)
	}
}

func TestMemoResolver_PropagatesError(t *testing.T) {
	stub := &stubResolver{err: errors.New("boom")}
	r := NewMemoResolver(stub, time.Second)
	_, err := r.Resolve(context.Background(), "h2")
	if err == nil {
		t.Fatal("expected error from upstream")
	}
}

func TestMemoResolver_EmptyHashShortCircuits(t *testing.T) {
	stub := &stubResolver{set: NewSet([]string{string(EveBasic)})}
	r := NewMemoResolver(stub, time.Second)
	set, err := r.Resolve(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !set.Empty() {
		t.Fatal("empty hash should yield empty set")
	}
	if stub.calls != 0 {
		t.Fatalf("upstream should not be called for empty hash, got %d", stub.calls)
	}
}

func TestStaticResolver(t *testing.T) {
	r := NewStaticResolver([]string{string(SearchSemantic)})
	set, err := r.Resolve(context.Background(), "any-hash")
	if err != nil {
		t.Fatal(err)
	}
	if !set.Has(SearchSemantic) {
		t.Fatal("expected static set to contain search.semantic")
	}
}

// --- HTTP middleware -------------------------------------------------

func TestRequireHTTP_AllowedAndDenied(t *testing.T) {
	resolver := NewStaticResolver([]string{string(SearchInverted)})
	mw := RequireHTTP(resolver, SearchInverted)

	called := false
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if !HasEntitlement(r.Context(), SearchInverted) {
			t.Fatal("expected resolved set on context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Request with hash on ctx → resolved → allowed
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(WithHash(req.Context(), "any-hash"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("expected 200 + handler called, got %d called=%v", rec.Code, called)
	}

	// Request requiring missing entitlement → 403 upgrade payload
	mwSemantic := RequireHTTP(resolver, SearchSemantic)
	denyHandler := mwSemantic(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run when entitlement missing")
	}))
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2 = req2.WithContext(WithHash(req2.Context(), "any-hash"))
	denyHandler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	if rec2.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected application/json, got %s", rec2.Header().Get("Content-Type"))
	}
}

// --- gRPC interceptor ------------------------------------------------

func TestRequireUnary_AllowedAndDenied(t *testing.T) {
	resolver := NewStaticResolver([]string{string(EveFull)})

	allowed := RequireUnary(resolver, EveFull)
	called := false
	_, err := allowed(WithHash(context.Background(), "h"), nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, req any) (any, error) {
		called = true
		if !HasEntitlement(ctx, EveFull) {
			t.Fatal("expected eve.full on context")
		}
		return "ok", nil
	})
	if err != nil || !called {
		t.Fatalf("allowed path: err=%v called=%v", err, called)
	}

	denied := RequireUnary(resolver, GenesisEngine)
	_, err = denied(WithHash(context.Background(), "h"), nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, req any) (any, error) {
		t.Fatal("handler should not run")
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected PermissionDenied error")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}
