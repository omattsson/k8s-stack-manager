package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
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
//   - SQL error: level error, attributes sql, rows, duration and error. A
//     duplicate key is level warn; a cancelled context is level debug, or
//     the slow query line with the error attribute when it was slow.
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
		switch {
		case isDuplicateKeyError(err):
			// The repositories answer it with ErrDuplicateKey (409).
			level, msg = slog.LevelWarn, "SQL duplicate key"
		case errors.Is(err, context.Canceled):
			// The client went away (#656); a passed deadline stays an error.
			level, msg = slog.LevelDebug, "SQL query cancelled"
			if l.isSlow(elapsed) {
				level, msg, isSlow = slog.LevelWarn, "Slow SQL query", true
			}
		}
	case l.isSlow(elapsed):
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
		errText := err.Error()
		if l.parameterized {
			errText = redactSQLError(errText)
		}
		r.AddAttrs(slog.String("error", errText))
	}
	if isSlow {
		r.AddAttrs(slog.Duration("threshold", l.slowThreshold))
	}
	_ = h.Handle(ctx, r)
}

// isSlow reports whether a statement of this duration gets the slow query line.
func (l *slogGormLogger) isSlow(elapsed time.Duration) bool {
	return l.level >= logger.Warn && l.slowThreshold > 0 && elapsed >= l.slowThreshold
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

var (
	sqlErrDuplicateValue = regexp.MustCompile(`(?s)(Duplicate entry )'.*'( for key '[^']*')$`)
	sqlErrColumnValue    = regexp.MustCompile(`(?s)( value: )'.*'( for column '[^']*' at row \d+)$`)
)

// redactSQLError replaces the quoted values of a SQL error text with '?'.
// MySQL quotes values without escaping (1062, 1292, 1366), so only the key
// or column name at the end stays; otherwise all from the first to the last
// quote goes (#653).
func redactSQLError(msg string) string {
	for _, re := range []*regexp.Regexp{sqlErrDuplicateValue, sqlErrColumnValue} {
		if loc := re.FindStringIndex(msg); loc != nil {
			// GORM joins errors with "; ": an earlier error can also hold values.
			return redactSQLError(msg[:loc[0]]) + re.ReplaceAllString(msg[loc[0]:], "$1'?'$2")
		}
	}
	first, last := -1, -1
	for i := 0; i < len(msg); i++ {
		if msg[i] == '\'' && !isWordApostrophe(msg, i) {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return msg
	}
	if last == first {
		return msg[:first] + "'?'"
	}
	return msg[:first] + "'?'" + msg[last+1:]
}

// isWordApostrophe reports a quote between two letters, as in "doesn't".
func isWordApostrophe(s string, i int) bool {
	isLetter := func(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
	return i > 0 && i < len(s)-1 && isLetter(s[i-1]) && isLetter(s[i+1])
}

// parameterizedSQLKey marks the statement context of a release-mode DB.
type parameterizedSQLKey struct{}

func init() {
	// db.Scan logs through logger.Recorder, which filters the values with this
	// package function, not with the DB logger (#653).
	logger.RecorderParamsFilter = recorderParamsFilter
}

func recorderParamsFilter(ctx context.Context, sql string, params ...any) (string, []any) {
	if ctx != nil && ctx.Value(parameterizedSQLKey{}) != nil {
		return sql, nil
	}
	return sql, params
}

// useParameterizedScanSQL makes db.Scan log ? placeholders instead of the
// values, as the release-mode logger does for the other queries.
func useParameterizedScanSQL(db *gorm.DB) error {
	return db.Callback().Row().Before("gorm:row").Register("stackmanager:parameterized_scan_sql", func(tx *gorm.DB) {
		ctx := tx.Statement.Context
		if ctx == nil {
			ctx = context.Background()
		}
		tx.Statement.Context = context.WithValue(ctx, parameterizedSQLKey{}, true)
	})
}
