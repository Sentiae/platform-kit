package gormlog_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/sentiae/platform-kit/gormlog"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestNew_RejectsUnknownLevel(t *testing.T) {
	tests := []struct {
		name      string
		level     string
		wantLevel gormlogger.LogLevel
		wantErr   bool
	}{
		{"silent", "silent", gormlogger.Silent, false},
		{"error", "error", gormlogger.Error, false},
		{"warn", "warn", gormlogger.Warn, false},
		{"info", "info", gormlogger.Info, false},
		{"mixed case", "WaRn", gormlogger.Warn, false},
		{"upper with trailing space", "INFO ", gormlogger.Info, false},
		{"empty", "", 0, true},
		{"slog level not a gorm level", "debug", 0, true},
		{"unknown word", "verbose", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			gotLevel, err := gormlog.ParseLevel(tt.level)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseLevel(%q) = %v, want error", tt.level, gotLevel)
				}
				// The error has to name the offending value and the accepted
				// set, or the operator cannot fix the config from the log.
				if !strings.Contains(err.Error(), tt.level) || !strings.Contains(err.Error(), "silent") {
					t.Errorf("ParseLevel(%q) error = %q, want it to name the value and the accepted set", tt.level, err)
				}
			} else {
				if err != nil {
					t.Fatalf("ParseLevel(%q): unexpected error: %v", tt.level, err)
				}
				if gotLevel != tt.wantLevel {
					t.Errorf("ParseLevel(%q) = %v, want %v", tt.level, gotLevel, tt.wantLevel)
				}
			}

			l, err := gormlog.New(&buf, tt.level)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("New(_, %q) = %T, want error", tt.level, l)
				}
				if l != nil {
					t.Errorf("New(_, %q) returned a logger alongside error %v", tt.level, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("New(_, %q): unexpected error: %v", tt.level, err)
			}
			if l == nil {
				t.Fatal("New returned a nil logger without an error")
			}
		})
	}
}

// TestNew_AlwaysImplementsParamsFilter asserts the guarantee New itself
// checks: gorm's callbacks.go only filters bound values out of a logged
// statement when the logger implements gorm.ParamsFilter.
//
// This test alone is NOT sufficient, and must not be trusted as the
// protection. gorm's own logger implements ParamsFilter unconditionally — it
// just returns the params unchanged when ParameterizedQueries is unset — so
// building the logger without the flag still passes this test. Proving the
// values do not reach the log needs TestNew_BoundValuesNeverEcho, which drives
// the real callbacks path against a real database.
func TestNew_AlwaysImplementsParamsFilter(t *testing.T) {
	var buf bytes.Buffer

	l, err := gormlog.New(&buf, "info")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, ok := l.(gorm.ParamsFilter); !ok {
		t.Fatalf("New returned %T, which does not implement gorm.ParamsFilter", l)
	}
}

// TestNew_ConcurrentIsRaceFree pins the reason the RecorderParamsFilter
// override is a sync.Once and not a bare assignment: New writes a
// package-global of gorm's, and two services (or two tests) building a logger
// at the same time would otherwise race on it. Meaningful only under -race.
func TestNew_ConcurrentIsRaceFree(t *testing.T) {
	const goroutines = 2

	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		errs  = make([]error, goroutines)
	)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = gormlog.New(io.Discard, "info")
		}(i)
	}

	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: New: %v", i, err)
		}
	}
}

// TestDiscard_ImplementsParamsFilter is the compile-time assertion in
// discard.go restated where a reader looks for it, plus the part the assertion
// cannot state: that the filter actually drops the values rather than handing
// them back the way gorm's own logger does when ParameterizedQueries is unset.
//
// Like TestNew_AlwaysImplementsParamsFilter this is NOT the proof that nothing
// is written — a logger can satisfy this and still print. That is
// TestDiscard_EmitsNothing, which drives a real statement.
func TestDiscard_ImplementsParamsFilter(t *testing.T) {
	f, ok := gormlog.Discard.(gorm.ParamsFilter)
	if !ok {
		t.Fatalf("gormlog.Discard is %T, which does not implement gorm.ParamsFilter", gormlog.Discard)
	}

	const sql = "SELECT $1::text"
	gotSQL, gotVars := f.ParamsFilter(context.Background(), sql, "sentinel")
	if gotSQL != sql {
		t.Errorf("ParamsFilter rewrote the statement: got %q, want %q", gotSQL, sql)
	}
	if gotVars != nil {
		t.Errorf("ParamsFilter returned %v, want nil so Dialector.Explain has nothing to inline", gotVars)
	}
}

// TestDiscard_LogModeCannotTurnItUp pins the one way a caller could otherwise
// get output out of a discard logger: gorm calls Logger.LogMode when a Session
// sets LogLevel, and a logger that answered with a louder version of itself
// would start printing.
func TestDiscard_LogModeCannotTurnItUp(t *testing.T) {
	if got := gormlog.Discard.LogMode(gormlogger.Info); got != gormlog.Discard {
		t.Fatalf("Discard.LogMode(Info) = %#v, want the same discard logger", got)
	}
}
