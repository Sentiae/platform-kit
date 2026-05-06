// Package embedding provides shared abstractions for embedding
// storage + tiering: Cache interface (pgvector / Redis /
// content-hash dedup), VectorStore (cold-tier object store),
// TierClassifier (hot/warm/cold by recency), and HashKey for
// cache-key consistency.
//
// Generation itself moved to llm-gateway-service — call the
// gateway's /v1/embeddings endpoint, then wrap the call with
// Cache.Get/Set + the tier classifier so cost stays bounded.
package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Vector is the canonical embedding shape. 384 floats matches
// BGE-small-en-v1.5; the gateway can route to other models, but
// stored cache rows must agree on dimensionality per (model, key).
type Vector []float32

// Cache stores Vectors keyed by content hash so identical chunks
// across forks / branches share one row. Implementations must
// return (nil, nil) on miss — only return error for transient
// failures.
type Cache interface {
	Get(ctx context.Context, key string) (Vector, error)
	Set(ctx context.Context, key string, vec Vector) error
}

// HashKey returns the canonical cache key for a (model, text)
// tuple. The model is part of the key so a model upgrade
// invalidates the cache automatically.
func HashKey(model, text string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(strings.TrimSpace(model)))
	_, _ = h.Write([]byte{0x1f}) // unit separator
	_, _ = h.Write([]byte(strings.TrimSpace(text)))
	return "emb:" + hex.EncodeToString(h.Sum(nil))
}
