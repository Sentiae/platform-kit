package gormlog

import (
	"context"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Discard is the ORM logger for a query chain whose intent is "this logs
// nothing" — a gorm.Session override around a statement whose text or bound
// values must not reach the log at all.
//
// It is a value, not a constructor, because it has nothing to construct: there
// is no writer to point at and no level to parse. A package-level var built by
// New would have to run New in an initialiser, and New mutates a gorm global
// (see recorderOnce) — a side-effecting package initialiser is exactly what
// §30.14 forbids and what recorderOnce exists to avoid.
//
// It is NOT gormlogger.Discard. That one is a real logger writing to
// io.Discard at Silent level: its ParamsFilter hands the bound values back
// (its Config has no ParameterizedQueries), so gorm still calls
// Dialector.Explain with the values inlined, and LogMode can hand back a
// louder logger built over the same Config. Discard below cannot be turned up
// and never sees a value in the first place.
var Discard gormlogger.Interface = discardLogger{}

// The same assertion New performs at runtime, made at compile time because a
// var has no constructor to perform it in. gorm's callbacks.go type-asserts
// gorm.ParamsFilter before Explain; a logger that does not implement it gets
// the bound values inlined into the statement text.
var _ gorm.ParamsFilter = discardLogger{}

// discardLogger implements gormlogger.Interface by doing nothing at all. It
// holds no writer, so "writes nowhere" is a property of the type rather than
// of how it was configured.
type discardLogger struct{}

// LogMode returns the same logger. A discard logger that could be turned up by
// a caller's LogMode(Info) would not be a discard logger.
func (d discardLogger) LogMode(gormlogger.LogLevel) gormlogger.Interface { return d }

func (discardLogger) Info(context.Context, string, ...any)  {}
func (discardLogger) Warn(context.Context, string, ...any)  {}
func (discardLogger) Error(context.Context, string, ...any) {}

func (discardLogger) Trace(context.Context, time.Time, func() (string, int64), error) {}

// ParamsFilter drops the bound values, so Explain has nothing to inline even
// on the paths that render a statement before consulting the log level.
func (discardLogger) ParamsFilter(_ context.Context, sql string, _ ...any) (string, []any) {
	return sql, nil
}
