package database

import (
	"io"
	"log"
	"os"
	"time"

	"gorm.io/gorm/logger"
)

// gormSlowQueryThreshold is the duration above which GORM logs a query as
// slow. It matches the GORM default.
const gormSlowQueryThreshold = 200 * time.Millisecond

// newGormLogger returns the GORM logger for the backend (#646). It writes to
// stderr through its own *log.Logger, not through the log package default:
// after slog.SetDefault the package default writes through slog, and GORM
// output must not depend on that chain.
func newGormLogger(debug bool) logger.Interface {
	return newGormLoggerTo(os.Stderr, debug)
}

// newGormLoggerTo builds the GORM logger with the given writer.
//
//   - Debug: level Info, every SQL statement with its values.
//   - Release: level Warn, so errors and slow queries (over
//     gormSlowQueryThreshold) are logged, with placeholders instead of
//     values (no user data or secrets in the log).
//
// Both modes ignore gorm.ErrRecordNotFound (an expected answer for many
// lookups; the repositories map it to dberrors.ErrNotFound) and write no
// ANSI color codes.
func newGormLoggerTo(w io.Writer, debug bool) logger.Interface {
	level := logger.Warn
	if debug {
		level = logger.Info
	}
	return logger.New(log.New(w, "", log.LstdFlags), logger.Config{
		SlowThreshold:             gormSlowQueryThreshold,
		LogLevel:                  level,
		IgnoreRecordNotFoundError: true,
		ParameterizedQueries:      !debug,
		Colorful:                  false,
	})
}
