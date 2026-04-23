// Comment backfill job — copies rows from each service's legacy
// comment table into conversation-service context channels. §16.3.
//
// Intended usage:
//
//	job := comments.NewBackfill(commentsClient)
//	job.AddLegacy("work_comments", "spec", func(row LegacyRow) comments.CreateInput { ... })
//	job.AddLegacy("git_pr_line_comments", "pull_request", func(row LegacyRow) comments.CreateInput { ... })
//	ctx := context.Background()
//	if err := job.Run(ctx, readerFn); err != nil { ... }
//
// The job is idempotent: each legacy row carries a unique id, which is
// encoded in the context channel's external_id so reruns don't
// double-insert.
package comments

import (
	"context"
	"fmt"
	"log"
)

// LegacyRow is the shape the caller yields from their per-service
// source table. Map `RawData` as needed inside the adapter closure.
type LegacyRow struct {
	ID        string
	AuthorID  string
	Body      string
	CreatedAt string
	// RawData is the full row as a map so adapter closures can access
	// per-table columns (e.g. pull_request_id, file_path, line).
	RawData map[string]any
}

// LegacyAdapter turns a LegacyRow into a CreateInput for the target
// context channel. Called once per row by the backfill loop.
type LegacyAdapter func(LegacyRow) CreateInput

// ReaderFn yields rows from a legacy table. The reader is responsible
// for pagination + rate limiting. Returning io.EOF (or `(nil, nil)`)
// signals end-of-stream.
type ReaderFn func(ctx context.Context, tableName string) (<-chan LegacyRow, error)

// Backfill orchestrates the migration.
type Backfill struct {
	client   *Client
	entries  []backfillEntry
	onError  func(tableName, rowID string, err error)
	progress func(tableName string, migrated, skipped int)
}

type backfillEntry struct {
	tableName   string
	contextType ContextType
	adapter     LegacyAdapter
}

// NewBackfill builds a migration job.
func NewBackfill(client *Client) *Backfill {
	return &Backfill{
		client:   client,
		onError:  func(_, _ string, err error) { log.Printf("backfill error: %v", err) },
		progress: func(_ string, _, _ int) {},
	}
}

// AddLegacy registers a source table with its row→CreateInput adapter.
func (b *Backfill) AddLegacy(tableName string, contextType ContextType, adapter LegacyAdapter) *Backfill {
	b.entries = append(b.entries, backfillEntry{
		tableName:   tableName,
		contextType: contextType,
		adapter:     adapter,
	})
	return b
}

// OnError installs an error callback. Errors don't abort the job;
// each row is attempted independently.
func (b *Backfill) OnError(fn func(tableName, rowID string, err error)) *Backfill {
	b.onError = fn
	return b
}

// OnProgress installs a periodic progress callback — called once per
// table after the stream finishes.
func (b *Backfill) OnProgress(fn func(tableName string, migrated, skipped int)) *Backfill {
	b.progress = fn
	return b
}

// Run performs the migration. Each registered table is processed in
// sequence; within a table the reader stream is drained in order.
func (b *Backfill) Run(ctx context.Context, reader ReaderFn) error {
	if b == nil || b.client == nil {
		return fmt.Errorf("backfill: missing client")
	}
	for _, e := range b.entries {
		rowCh, err := reader(ctx, e.tableName)
		if err != nil {
			return fmt.Errorf("backfill: open %s: %w", e.tableName, err)
		}
		migrated, skipped := 0, 0
		for row := range rowCh {
			in := e.adapter(row)
			if in.ContextType == "" {
				in.ContextType = e.contextType
			}
			if in.Body == "" {
				skipped++
				continue
			}
			if _, err := b.client.Create(ctx, in); err != nil {
				b.onError(e.tableName, row.ID, err)
				continue
			}
			migrated++
		}
		b.progress(e.tableName, migrated, skipped)
	}
	return nil
}
