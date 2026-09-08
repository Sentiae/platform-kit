// Package gormlog builds the single GORM logger every service uses.
//
// GORM renders a statement for the log by calling Dialector.Explain(sql,
// vars...), which inlines the bound values into the SQL text. The only thing
// that stops it is the optional ParamsFilter interface: gorm's callbacks.go
// does `if filter, ok := db.Logger.(ParamsFilter); ok { sql, vars = ... }`
// before Explain, and gorm's own logger returns nil vars from ParamsFilter
// only when Config.ParameterizedQueries is set. A logger that forgets the
// flag therefore writes every bound value — tokens, emails, secrets — into
// the log, silently.
//
// This package exists so that mistake cannot be expressed. There is no
// ParameterizedQueries parameter to forget: New always sets it, and refuses
// to return a logger that does not implement gorm.ParamsFilter.
//
// One finisher does not consult the DB's logger at all, and New closes that
// hole too. Measured against gorm v1.31.2: (*gorm.DB).Scan substitutes gorm's
// own logger.Recorder for the duration of the query (finisher_api.go) and
// replays the already-explained text through the real logger afterwards. The
// Recorder's ParamsFilter delegates to the package-global
// logger.RecorderParamsFilter, which ships as a pass-through, so a statement
// run through Scan was explained with its values inlined no matter which
// logger the DB was opened with. New overrides that global (see
// recorderOnce). Exec, Rows, Row, First/Take/Find execute through
// processor.Execute and are filtered by this logger's own ParamsFilter.
//
// Usage:
//
//	l, err := gormlog.New(os.Stdout, cfg.Database.LogLevel)
//	if err != nil { return nil, fmt.Errorf("gorm logger: %w", err) }
//	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: l})
package gormlog

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sentiae/platform-kit/logger"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// slowThreshold is the elapsed time above which a statement is logged as slow.
const slowThreshold = 200 * time.Millisecond

// acceptedLevels is the exact set ParseLevel accepts, quoted for error messages.
const acceptedLevels = `"silent", "error", "warn", "info"`

// recorderOnce guards the one-time override of gorm's package-global
// logger.RecorderParamsFilter. See New for why the override is needed.
//
// sync.Once, not init(): a side-effecting package initialiser is forbidden
// (constitution §30.14), and a bare assignment from New would be a write that
// races every concurrent New under -race. Once also gives the ordering the
// guarantee rests on — the write happens-before every New returns, so every
// DB built through this constructor sees the filtering override already in
// place.
var recorderOnce sync.Once

// ParseLevel maps a configured level name to a gorm log level. It is exported
// because services validate their configuration before opening a database;
// New calls it itself, so a caller can never hand New an unparsed level.
//
// Parsing is fail-closed: an unrecognised value is an error, never a default.
// A silent fallback would let a typo decide how much the ORM prints.
func ParseLevel(level string) (gormlogger.LogLevel, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "silent":
		return gormlogger.Silent, nil
	case "error":
		return gormlogger.Error, nil
	case "warn":
		return gormlogger.Warn, nil
	case "info":
		return gormlogger.Info, nil
	default:
		return 0, fmt.Errorf("gormlog: unknown log level %q: accepted values are %s", level, acceptedLevels)
	}
}

// New returns the GORM logger for the given writer and level. A nil writer
// means os.Stdout. An unrecognised level is an error.
//
// The returned logger always filters bound values out of the logged SQL; that
// is not configurable, and New verifies it before returning.
func New(w io.Writer, level string) (gormlogger.Interface, error) {
	if w == nil {
		w = os.Stdout
	}

	logLevel, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}

	// (*gorm.DB).Scan swaps the DB's logger for gorm's package-level
	// logger.Recorder while the statement runs, so callbacks.go asserts
	// ParamsFilter on the Recorder, not on the logger built below; the values
	// are inlined into the recorded text before the real logger is handed it.
	// The Recorder delegates to this global, which gorm documents as the
	// override point ("defaults to no-op, allows to be run-over by a different
	// implementation") and ships as a pass-through. Dropping the params here
	// makes Explain leave the placeholders alone, which is the same outcome
	// ParameterizedQueries produces on every other finisher.
	//
	// The global is process-wide, so this also affects a DB that was not built
	// by New. That effect is strictly more filtering and never less: the
	// default returns the params, this returns none, and no caller can lose a
	// value it was previously logging by accident.
	recorderOnce.Do(func() {
		gormlogger.RecorderParamsFilter = func(_ context.Context, sql string, _ ...any) (string, []any) {
			return sql, nil
		}
	})

	// The slog level is hardcoded "debug" and deliberately NOT derived from
	// level: two gates in series would mean two places to get it wrong, and
	// the one GORM consults (Config.LogLevel) would be silently overridden by
	// the other. slog's gate stays permanently open so GORM's LogLevel is the
	// single authority over how much the ORM prints.
	base := logger.New(logger.Config{Writer: w, Level: "debug", Format: "json"})

	l := gormlogger.NewSlogLogger(base, gormlogger.Config{
		SlowThreshold: slowThreshold,
		LogLevel:      logLevel,
		// A missing row is the caller's business, not a swallowed log line.
		IgnoreRecordNotFoundError: false,
		ParameterizedQueries:      true,
	})

	// The same assertion gorm's callbacks.go performs before Explain. If it
	// fails there, bound values are inlined into every logged statement and
	// nothing reports it; asserted here, a future change to the underlying
	// constructor becomes a startup error instead of a silent data leak.
	if _, ok := l.(gorm.ParamsFilter); !ok {
		return nil, fmt.Errorf("gormlog: %T does not implement gorm.ParamsFilter, so gorm would inline bound values into logged SQL", l)
	}

	return l, nil
}
