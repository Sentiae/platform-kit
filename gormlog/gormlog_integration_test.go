//go:build integration

package gormlog_test

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/sentiae/platform-kit/gormlog"
	"github.com/sentiae/platform-kit/testutil"
	"gorm.io/gorm"
)

// probeRow is the destination for the finishers that return a value. The
// column name matches the probe table so gorm can map it.
type probeRow struct {
	V string
}

// TestNew_BoundValuesNeverEcho is the test that actually proves the guarantee.
// It runs real statements with real bound values against a real PostgreSQL, so
// the assertions cover gorm's own paths — the ParamsFilter type assertion in
// callbacks.go followed by Dialector.Explain — and not the logger in
// isolation.
//
// It is table-driven over the finishers on purpose. An earlier version
// exercised only Rows(), and that is exactly why it could not see the hole
// this test now covers: Rows(), Exec() and Take() execute through
// processor.Execute, which asserts ParamsFilter on the DB's own logger, while
// (*DB).Scan swaps in gorm's package-level logger.Recorder for the duration of
// the query and replays the already-explained text afterwards. With the
// Recorder's default pass-through filter the Scan rows below render
// `SELECT 'sentinel-...'::text` while every other row renders `$1`. A guard
// that only drives the filtered paths cannot fail on the unfiltered one.
func TestNew_BoundValuesNeverEcho(t *testing.T) {
	db := testutil.NewTestDB(t, "")

	// A real table, so the query-builder cases go through BuildQuerySQL rather
	// than raw SQL — a different path to the same Trace call.
	if err := db.Exec(`CREATE TABLE probe (v text)`).Error; err != nil {
		t.Fatalf("create probe table: %v", err)
	}

	tests := []struct {
		name string
		// run executes one statement binding sentinel as a parameter. It
		// fails the test itself on an unexpected error, and returns the value
		// the database gave back, or "" for a finisher that returns none.
		run func(t *testing.T, sess *gorm.DB, sentinel string) string
	}{
		{
			name: "Exec",
			run: func(t *testing.T, sess *gorm.DB, sentinel string) string {
				if err := sess.Exec("SELECT ?::text", sentinel).Error; err != nil {
					t.Fatalf("Exec: %v", err)
				}
				return ""
			},
		},
		{
			name: "Rows",
			run: func(t *testing.T, sess *gorm.DB, sentinel string) string {
				rows, err := sess.Raw("SELECT ?::text", sentinel).Rows()
				if err != nil {
					t.Fatalf("Rows: %v", err)
				}
				defer func() {
					if err := rows.Close(); err != nil {
						t.Errorf("close rows: %v", err)
					}
				}()

				var got string
				for rows.Next() {
					if err := rows.Scan(&got); err != nil {
						t.Fatalf("scan row: %v", err)
					}
				}
				if err := rows.Err(); err != nil {
					t.Fatalf("iterate rows: %v", err)
				}
				return got
			},
		},
		{
			name: "Take",
			run: func(t *testing.T, sess *gorm.DB, sentinel string) string {
				var row probeRow
				if err := sess.Table("probe").Where("v = ?", sentinel).Take(&row).Error; err != nil {
					t.Fatalf("Take: %v", err)
				}
				return row.V
			},
		},
		{
			name: "Scan",
			run: func(t *testing.T, sess *gorm.DB, sentinel string) string {
				var row probeRow
				if err := sess.Table("probe").Select("v").Where("v = ?", sentinel).Scan(&row).Error; err != nil {
					t.Fatalf("Scan: %v", err)
				}
				return row.V
			},
		},
		{
			name: "Raw().Scan",
			run: func(t *testing.T, sess *gorm.DB, sentinel string) string {
				var row probeRow
				if err := sess.Raw("SELECT ?::text AS v", sentinel).Scan(&row).Error; err != nil {
					t.Fatalf("Raw().Scan: %v", err)
				}
				return row.V
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sentinel := randomSentinel(t)

			// Seeded through the container's own discard logger so the seed
			// never reaches the buffer under test.
			if err := db.Exec("INSERT INTO probe (v) VALUES (?)", sentinel).Error; err != nil {
				t.Fatalf("seed probe row: %v", err)
			}

			var buf strings.Builder
			l, err := gormlog.New(&buf, "info")
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			got := tt.run(t, db.Session(&gorm.Session{Logger: l}), sentinel)
			// Where the finisher returns a value, prove the sentinel really
			// travelled as a bound parameter — otherwise the absence assertion
			// below would be asserting the absence of something never sent.
			if got != "" && got != sentinel {
				t.Fatalf("query returned %q, want the bound sentinel %q", got, sentinel)
			}

			out := buf.String()

			// Positive first: a blank page must fail this test, not pass it.
			if !strings.Contains(out, "SQL executed") {
				t.Fatalf("no statement was logged at all; captured output: %q", out)
			}
			if !strings.Contains(out, "$1") {
				t.Fatalf("logged statement kept no placeholder, so it was not parameterized; captured output: %q", out)
			}

			// Only now is the absence meaningful.
			if strings.Contains(out, sentinel) {
				t.Fatalf("bound value %q was echoed into the log; captured output: %q", sentinel, out)
			}
		})
	}
}

// randomSentinel returns a value that cannot appear in the log by accident.
func randomSentinel(t *testing.T) string {
	t.Helper()

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generate sentinel: %v", err)
	}
	return "sentinel-" + hex.EncodeToString(b)
}
