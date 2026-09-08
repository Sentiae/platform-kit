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

// TestNew_BoundValuesNeverEcho is the test that actually proves the guarantee.
// It runs a real statement with a real bound value against a real PostgreSQL,
// so the assertion covers gorm's own path in callbacks.go — the ParamsFilter
// type assertion followed by Dialector.Explain — and not the logger in
// isolation. Defeating the protection (wrapping the logger so ParamsFilter is
// no longer promoted, or dropping ParameterizedQueries) makes Explain inline
// the vars and this test go red.
//
// Rows() is used rather than Scan() deliberately: Rows() executes through
// processor.Execute, which is the path callbacks.go filters. (*DB).Scan
// swaps in gorm's logger.Recorder for the duration of the query and replays
// the already-explained text, so it does not exercise this logger's filter at
// all (see the package documentation).
func TestNew_BoundValuesNeverEcho(t *testing.T) {
	sentinel := randomSentinel(t)

	db := testutil.NewTestDB(t, "")

	var buf strings.Builder
	l, err := gormlog.New(&buf, "info")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rows, err := db.Session(&gorm.Session{Logger: l}).Raw("SELECT ?::text", sentinel).Rows()
	if err != nil {
		t.Fatalf("run query: %v", err)
	}
	var scanned string
	for rows.Next() {
		if err := rows.Scan(&scanned); err != nil {
			t.Fatalf("scan row: %v", err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rows: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close rows: %v", err)
	}
	// The value really did travel as a bound parameter — otherwise the rest of
	// this test would assert the absence of something never sent.
	if scanned != sentinel {
		t.Fatalf("query returned %q, want the bound sentinel %q", scanned, sentinel)
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
