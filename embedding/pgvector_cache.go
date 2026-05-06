// Postgres + pgvector backed Cache implementation. Persistent
// alternative to MemoCache (the in-memory map).
//
// Schema (managed by the consuming service's migrations):
//
//   CREATE TABLE embedding_cache (
//     hash      TEXT PRIMARY KEY,
//     model     TEXT NOT NULL,
//     vector    VECTOR(384) NOT NULL,    -- adjust dim per model
//     last_hit  TIMESTAMPTZ NOT NULL DEFAULT now()
//   );
//
// Adapters install pgvector beforehand. Phase-2 swap: switch a
// service's `embedding.Cache` from MemoCache to PgvectorCache to
// share hits across replicas.
package embedding

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// PgvectorCache is a pgvector-backed Cache.
type PgvectorCache struct {
	db    *gorm.DB
	table string
}

// NewPgvectorCache builds the cache. table defaults to "embedding_cache".
func NewPgvectorCache(db *gorm.DB, table string) *PgvectorCache {
	if table == "" {
		table = "embedding_cache"
	}
	return &PgvectorCache{db: db, table: table}
}

// Get fetches a vector by hash. Returns nil + nil on miss so callers
// can fall through to live embedding.
func (c *PgvectorCache) Get(ctx context.Context, hash string) (Vector, error) {
	if c == nil || c.db == nil {
		return nil, nil
	}
	var literal string
	row := c.db.WithContext(ctx).
		Raw(fmt.Sprintf("SELECT vector::text FROM %s WHERE hash = ? LIMIT 1", c.table), hash).
		Row()
	if err := row.Scan(&literal); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		// Other scan errors → soft miss + log, never block live embed.
		return nil, nil
	}
	if literal == "" {
		return nil, nil
	}
	v, err := parseVectorLiteral(literal)
	if err != nil {
		return nil, nil
	}
	// Best-effort touch last_hit for LRU eviction policies.
	_ = c.db.WithContext(ctx).Exec(
		fmt.Sprintf("UPDATE %s SET last_hit = now() WHERE hash = ?", c.table),
		hash,
	).Error
	return v, nil
}

// Set persists a vector under the hash. Upserts on hash collision.
// Cache interface omits the model dimension; we store an empty
// string placeholder. Production callers using PgvectorCache
// directly may use SetWithModel for full attribution.
func (c *PgvectorCache) Set(ctx context.Context, hash string, v Vector) error {
	return c.SetWithModel(ctx, hash, "", v)
}

// SetWithModel is the model-attributed variant.
func (c *PgvectorCache) SetWithModel(ctx context.Context, hash string, model string, v Vector) error {
	if c == nil || c.db == nil {
		return nil
	}
	literal := vectorLiteral(v)
	q := fmt.Sprintf(
		"INSERT INTO %s (hash, model, vector, last_hit) VALUES (?, ?, ?::vector, now()) "+
			"ON CONFLICT (hash) DO UPDATE SET vector = EXCLUDED.vector, last_hit = now()",
		c.table,
	)
	return c.db.WithContext(ctx).Exec(q, hash, model, literal).Error
}

// vectorLiteral formats a Vector as the pgvector input string
// `[0.1,0.2,...]`. pgvector parses this directly via the ::vector cast.
func vectorLiteral(v Vector) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = fmt.Sprintf("%g", f)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// parseVectorLiteral reverses vectorLiteral. pgvector returns the
// same `[0.1,0.2,...]` shape on text cast.
func parseVectorLiteral(s string) (Vector, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make(Vector, len(parts))
	for i, p := range parts {
		var f float32
		if _, err := fmt.Sscanf(strings.TrimSpace(p), "%f", &f); err != nil {
			return nil, err
		}
		out[i] = f
	}
	return out, nil
}

var _ Cache = (*PgvectorCache)(nil)
