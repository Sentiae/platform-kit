package retention

import (
	"context"
	"errors"
	"io"
	"time"
)

// ColdStore is the object-storage port used by the S3 cold tier
// mover. Implementations live in service-specific adapter packages
// (s3, gcs, in-memory for tests). (§H.4)
type ColdStore interface {
	Put(ctx context.Context, key string, body io.Reader) (uri string, err error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// HotRow is the abstract row a retention worker hands to the
// cold-tier mover. PrimaryKey is the row id, Body is the bytes to
// archive (typically gob/json/parquet of the warm row).
type HotRow struct {
	PrimaryKey string
	CreatedAt  time.Time
	Body       []byte
}

// HotSource lets the mover iterate rows past their cold cutoff
// without coupling to a specific DB schema.
type HotSource interface {
	ListBefore(ctx context.Context, cutoff time.Time, limit int) ([]HotRow, error)
	MarkArchived(ctx context.Context, primaryKey, coldURI string) error
	DeleteWhereArchivedBefore(ctx context.Context, cutoff time.Time) (int, error)
}

// ColdTierMover walks past-cutoff hot rows, writes them to S3, and
// updates the source row with the cold URI. Subsequent retention
// passes hard-delete archived rows that are also past DeleteAfter.
type ColdTierMover struct {
	policy RetentionPolicy
	src    HotSource
	cold   ColdStore
	now    func() time.Time
	prefix string
	batch  int
}

// NewColdTierMover builds the mover. `prefix` is the S3 key prefix
// (e.g. "signals/" or "audit/"); each row writes under
// `<prefix><primary_key>.bin`.
func NewColdTierMover(policy RetentionPolicy, src HotSource, cold ColdStore, prefix string) *ColdTierMover {
	return &ColdTierMover{
		policy: policy,
		src:    src,
		cold:   cold,
		now:    time.Now,
		prefix: prefix,
		batch:  500,
	}
}

// ErrInvalidPolicy means the mover got handed a policy that doesn't
// archive (ColdAfter == 0). Catch this at boot time.
var ErrInvalidPolicy = errors.New("cold_tier: policy has no cold tier")

// Run performs one archive pass + one delete pass. It is safe to
// call repeatedly from a cron — both passes are idempotent.
func (m *ColdTierMover) Run(ctx context.Context) (archived, deleted int, err error) {
	if err := m.policy.Validate(); err != nil {
		return 0, 0, err
	}
	if m.policy.ColdAfter == 0 {
		return 0, 0, ErrInvalidPolicy
	}
	now := m.now()
	archiveCutoff := now.Add(-time.Duration(m.policy.ColdAfter) * 24 * time.Hour)

	rows, err := m.src.ListBefore(ctx, archiveCutoff, m.batch)
	if err != nil {
		return 0, 0, err
	}
	for _, row := range rows {
		key := m.prefix + row.PrimaryKey + ".bin"
		uri, err := m.cold.Put(ctx, key, bytesReader(row.Body))
		if err != nil {
			return archived, 0, err
		}
		if err := m.src.MarkArchived(ctx, row.PrimaryKey, uri); err != nil {
			return archived, 0, err
		}
		archived++
	}

	if m.policy.DeleteAfter > 0 {
		deleteCutoff := now.Add(-time.Duration(m.policy.DeleteAfter) * 24 * time.Hour)
		n, err := m.src.DeleteWhereArchivedBefore(ctx, deleteCutoff)
		if err != nil {
			return archived, 0, err
		}
		deleted = n
	}
	return archived, deleted, nil
}

// bytesReader wraps a byte slice in an io.Reader. We avoid pulling
// bytes.NewReader into the public surface so platform-kit/retention
// stays light on stdlib imports for downstream services.
type byteReader struct {
	b   []byte
	pos int
}

func bytesReader(b []byte) io.Reader { return &byteReader{b: b} }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}
