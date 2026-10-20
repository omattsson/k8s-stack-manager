package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/utils"
)

// gormSlowQueryThreshold is the duration from which GORM logs a query as
// slow. It matches the GORM default.
const gormSlowQueryThreshold = 200 * time.Millisecond

var (
	_ logger.Interface  = (*slogGormLogger)(nil)
	_ gorm.ParamsFilter = (*slogGormLogger)(nil)
)

// newGormLogger returns the GORM logger of the backend. It writes through the
// default slog logger, so LOG_LEVEL applies and, with OTEL_ENABLED, the lines
// reach the collector with the trace and span ID of the query context (#653).
func newGormLogger(debug bool) logger.Interface {
	return newSlogGormLogger(nil, debug)
}

// newSlogGormLogger returns a GORM logger that writes to log. A nil log uses
// slog.Default() at each call, so a later slog.SetDefault also applies.
//
//   - Debug: every statement with its values.
//   - Release: SQL errors and slow queries only, with ? placeholders instead
//     of the values (no user data or secrets in the log).
func newSlogGormLogger(log *slog.Logger, debug bool) *slogGormLogger {
	level := logger.Warn
	if debug {
		level = logger.Info
	}
	return &slogGormLogger{
		log:           log,
		slowThreshold: gormSlowQueryThreshold,
		level:         level,
		parameterized: !debug,
	}
}

// slogGormLogger is a GORM logger.Interface on slog.
//
//   - SQL error: level error, attributes sql, rows, duration and error.
//   - Slow query: level warn, attributes sql, rows, duration and threshold.
//   - Other statements (GORM level Info, debug mode only): level debug,
//     attributes sql, rows and duration.
//
// rows is left out when GORM does not know it (-1). gorm.ErrRecordNotFound
// is never an error line: the repositories map it to dberrors.ErrNotFound.
type slogGormLogger struct {
	log           *slog.Logger
	slowThreshold time.Duration
	level         logger.LogLevel
	parameterized bool
}

// LogMode returns a copy with the GORM log level (db.Debug() sets Info).
func (l *slogGormLogger) LogMode(level logger.LogLevel) logger.Interface {
	c := *l
	c.level = level
	return &c
}

// Info logs a GORM message at level info.
func (l *slogGormLogger) Info(ctx context.Context, msg string, data ...any) {
	if l.level >= logger.Info {
		l.message(ctx, slog.LevelInfo, msg, data)
	}
}

// Warn logs a GORM message at level warn.
func (l *slogGormLogger) Warn(ctx context.Context, msg string, data ...any) {
	if l.level >= logger.Warn {
		l.message(ctx, slog.LevelWarn, msg, data)
	}
}

// Error logs a GORM message at level error.
func (l *slogGormLogger) Error(ctx context.Context, msg string, data ...any) {
	if l.level >= logger.Error {
		l.message(ctx, slog.LevelError, msg, data)
	}
}

// Trace logs one SQL statement. fc builds the SQL text, so it runs only when
// the handler takes the line.
func (l *slogGormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	elapsed := time.Since(begin)
	var (
		level  slog.Level
		msg    string
		isErr  bool
		isSlow bool
	)
	switch {
	case l.level >= logger.Error && err != nil && !errors.Is(err, logger.ErrRecordNotFound):
		level, msg, isErr = slog.LevelError, "SQL error", true
	case l.level >= logger.Warn && l.slowThreshold > 0 && elapsed >= l.slowThreshold:
		level, msg, isSlow = slog.LevelWarn, "Slow SQL query", true
	case l.level >= logger.Info:
		level, msg = slog.LevelDebug, "SQL query"
	default:
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h := l.handler()
	if !h.Enabled(ctx, level) {
		return
	}

	sql, rows := fc()
	// CallerFrame must be called here directly: it skips a fixed number of
	// frames, then the GORM frames, and so finds the repository call.
	r := slog.NewRecord(time.Now(), level, msg, utils.CallerFrame().PC)
	r.AddAttrs(slog.String("sql", sql))
	if rows >= 0 {
		r.AddAttrs(slog.Int64("rows", rows))
	}
	r.AddAttrs(slog.Duration("duration", elapsed))
	if isErr {
		r.AddAttrs(slog.String("error", err.Error()))
	}
	if isSlow {
		r.AddAttrs(slog.Duration("threshold", l.slowThreshold))
	}
	_ = h.Handle(ctx, r)
}

// ParamsFilter drops the values of the SQL text in release mode, so the sql
// attribute has ? placeholders.
func (l *slogGormLogger) ParamsFilter(_ context.Context, sql string, params ...any) (string, []any) {
	if l.parameterized {
		return sql, nil
	}
	return sql, params
}

func (l *slogGormLogger) message(ctx context.Context, level slog.Level, format string, data []any) {
	if ctx == nil {
		ctx = context.Background()
	}
	h := l.handler()
	if !h.Enabled(ctx, level) {
		return
	}
	r := slog.NewRecord(time.Now(), level, fmt.Sprintf(format, data...), 0)
	_ = h.Handle(ctx, r)
}

func (l *slogGormLogger) handler() slog.Handler {
	if l.log != nil {
		return l.log.Handler()
	}
	return slog.Default().Handler()
}
