// Cold-tier vector storage. (Paradigm Shift §C.8)
//
// Hot vectors live in pgvector (via Cache); cold vectors land in
// S3-compatible object storage keyed by <org>/<repo>/<file>/<symbol>.vec.
// On query, the search path lazy-fetches the bytes, decodes the
// vector, and seeds the in-memory cache so subsequent hits stay fast.
//
// One adapter ships in this package: an in-memory store useful for
// dev + tests. Production wires an S3 (minio.New + GetObject /
// PutObject) adapter implementing VectorStore — the surface stays
// identical.
package embedding

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
)

// VectorStore is the cold-tier object-store port. Keys follow the
// `<organization_id>/<repository_id>/<file_path>/<symbol>.vec`
// convention; KeyForSymbol formats them so callers don't reinvent.
type VectorStore interface {
	// Put stores a vector under the supplied key. Overwrites are
	// allowed — same content yields the same bytes so this is
	// effectively idempotent.
	Put(ctx context.Context, key string, vec Vector) error
	// Get loads a vector. Returns ErrVectorNotFound when the key
	// is absent so callers can fall through to live re-embed.
	Get(ctx context.Context, key string) (Vector, error)
}

// ErrVectorNotFound signals a cold-store miss.
var ErrVectorNotFound = errors.New("cold_vector_store: vector not found")

// KeyForSymbol formats the canonical cold-tier key.
func KeyForSymbol(orgID, repoID, filePath, symbol string) string {
	return fmt.Sprintf("%s/%s/%s/%s.vec", orgID, repoID, filePath, symbol)
}

// EncodeVector serializes a Vector into the on-wire byte format used
// by S3 puts: little-endian float32 stream. Compact, deterministic,
// no schema versioning — vectors are content-addressed by upstream.
func EncodeVector(v Vector) []byte {
	out := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(f))
	}
	return out
}

// DecodeVector reverses EncodeVector. Length validates against the
// expected dim — caller passes 0 to accept any dim.
func DecodeVector(b []byte, expectDim int) (Vector, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("decode vector: byte length %d not multiple of 4", len(b))
	}
	dim := len(b) / 4
	if expectDim > 0 && dim != expectDim {
		return nil, fmt.Errorf("decode vector: dim %d, want %d", dim, expectDim)
	}
	out := make(Vector, dim)
	for i := 0; i < dim; i++ {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out, nil
}

// MemoryVectorStore is an in-memory VectorStore for dev + tests.
type MemoryVectorStore struct {
	mu sync.RWMutex
	kv map[string][]byte
}

// NewMemoryVectorStore builds the store.
func NewMemoryVectorStore() *MemoryVectorStore {
	return &MemoryVectorStore{kv: map[string][]byte{}}
}

// Put encodes + stores.
func (s *MemoryVectorStore) Put(_ context.Context, key string, v Vector) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kv[key] = EncodeVector(v)
	return nil
}

// Get decodes + returns.
func (s *MemoryVectorStore) Get(_ context.Context, key string) (Vector, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.kv[key]
	if !ok {
		return nil, ErrVectorNotFound
	}
	return DecodeVector(b, 0)
}

// ColdKey returns the default cold-store key for a (model, text)
// pair when the caller doesn't have org/repo/file/symbol context.
// Pair this with VectorStore.Get/Put around an llm-gateway-service
// /v1/embeddings call to implement the §C.8 lazy cold path.
func ColdKey(model, text string) string {
	return HashKey(model, text) + ".vec"
}
