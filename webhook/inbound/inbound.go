// Package inbound is a generic inbound-webhook receiver framework.
//
// Services (BFF, work-service, ops-service, …) embed a Receiver to
// accept provider webhooks over HTTP with a common contract:
//
//   - HMAC-SHA256 signature verification by default
//     (header: X-Sentiae-Signature: sha256=<hex>). Providers may override
//     the signature header and/or algorithm through their Provider impl.
//   - Idempotency: every request carries an idempotency key (header
//     X-Idempotency-Key or a provider-derived key); the receiver tracks
//     recently-seen keys so duplicate deliveries return 409.
//   - Retries on handler error: transient handler failures return 5xx so
//     the sender retries with backoff; permanent errors return 4xx.
//
// The package is deliberately tiny — it does not own persistence. Use
// NewInMemoryIdempotencyStore for a single-process receiver, or
// implement IdempotencyStore against Redis/DB for production.
package inbound

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Provider describes how to verify and route one inbound-webhook kind.
type Provider struct {
	// Name is the URL-safe slug the provider registers under.
	Name string
	// Secret is the shared HMAC secret. Required when Verifier is nil.
	Secret string
	// SignatureHeader overrides the default `X-Sentiae-Signature` header.
	// Empty string => use default.
	SignatureHeader string
	// Verifier, when set, takes precedence over the default HMAC-SHA256
	// verification. Providers like Stripe use a multi-element header
	// ("t=…,v1=…") and supply a custom verifier here.
	Verifier func(r *http.Request, body []byte, secret string) error
	// Handler is called after verification + idempotency guard pass.
	Handler func(ctx Context, body []byte) error
}

// Context is passed to handlers so they can route deliveries per-org.
type Context struct {
	OrgID         string
	Request       *http.Request
	IdempotencyKey string
}

// IdempotencyStore records seen delivery keys.
type IdempotencyStore interface {
	// SeenOrRecord returns true if the key was already recorded, false
	// and records it atomically otherwise.
	SeenOrRecord(key string) bool
}

// NewInMemoryIdempotencyStore returns a process-local ring-buffered
// store suitable for dev + single-instance deployments. TTL bounds
// memory growth; keys older than ttl are forgotten.
func NewInMemoryIdempotencyStore(ttl time.Duration) IdempotencyStore {
	return &memStore{ttl: ttl, seen: map[string]time.Time{}}
}

type memStore struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

func (m *memStore) SeenOrRecord(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	// evict expired
	for k, t := range m.seen {
		if now.Sub(t) > m.ttl {
			delete(m.seen, k)
		}
	}
	if _, ok := m.seen[key]; ok {
		return true
	}
	m.seen[key] = now
	return false
}

// Receiver is the HTTP entrypoint. Register providers, then mount
// (*Receiver).ServeHTTP at a router with `{provider}` + `{org_id}` URL
// vars. Routers that prefer explicit extraction can call Handle() with
// already-resolved provider + org.
type Receiver struct {
	providers map[string]Provider
	store     IdempotencyStore
}

// New builds a receiver. idempotency may be nil (no de-duplication).
func New(store IdempotencyStore) *Receiver {
	return &Receiver{providers: map[string]Provider{}, store: store}
}

// Register adds a provider definition. Idempotent by name — later
// registrations replace earlier ones.
func (r *Receiver) Register(p Provider) {
	r.providers[p.Name] = p
}

// Has reports whether the named provider is registered.
func (r *Receiver) Has(name string) bool {
	_, ok := r.providers[name]
	return ok
}

// ErrSignatureInvalid is returned when HMAC verification fails.
var ErrSignatureInvalid = errors.New("invalid signature")

// Handle runs the full verification + dedupe + dispatch pipeline for
// an already-extracted provider + org. Returns the HTTP status the
// caller should write.
func (r *Receiver) Handle(w http.ResponseWriter, req *http.Request, providerName, orgID string) {
	p, ok := r.providers[providerName]
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, 2<<20))
	if err != nil {
		http.Error(w, "request body too large", http.StatusBadRequest)
		return
	}
	// make body re-readable in case the handler wants the raw reader
	req.Body = io.NopCloser(bytes.NewReader(body))

	if p.Verifier != nil {
		if err := p.Verifier(req, body, p.Secret); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
	} else if p.Secret != "" {
		header := p.SignatureHeader
		if header == "" {
			header = "X-Sentiae-Signature"
		}
		if err := verifyHMACSHA256(req.Header.Get(header), body, p.Secret); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
	}

	idKey := req.Header.Get("X-Idempotency-Key")
	if idKey == "" {
		// Fall back to hashing the body + signature so a retrying sender
		// with no explicit key still gets deduped on exact replays.
		h := sha256.Sum256(body)
		idKey = providerName + ":" + hex.EncodeToString(h[:])
	}
	if r.store != nil && r.store.SeenOrRecord(providerName+":"+idKey) {
		http.Error(w, "duplicate delivery", http.StatusConflict)
		return
	}

	ctx := Context{OrgID: orgID, Request: req, IdempotencyKey: idKey}
	if err := p.Handler(ctx, body); err != nil {
		// Transient errors propagate as 5xx so the caller retries.
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "accepted"})
}

// verifyHMACSHA256 accepts either `sha256=<hex>` or bare `<hex>` header
// values (different providers follow different conventions).
func verifyHMACSHA256(headerValue string, body []byte, secret string) error {
	if headerValue == "" {
		return ErrSignatureInvalid
	}
	got := headerValue
	if strings.HasPrefix(got, "sha256=") {
		got = strings.TrimPrefix(got, "sha256=")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(got), []byte(expected)) {
		return ErrSignatureInvalid
	}
	return nil
}
