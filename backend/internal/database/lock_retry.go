package database

import (
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/go-sql-driver/mysql"
)

const (
	mysqlErrLockWaitTimeout = 1205
	mysqlErrDeadlock        = 1213

	lockRetryAttempts = 3
	lockRetryMinWait  = 10 * time.Millisecond
	lockRetryMaxWait  = 50 * time.Millisecond
)

// isLockConflictError reports whether InnoDB ended the statement or the
// transaction with a deadlock (1213) or a lock wait timeout (1205).
func isLockConflictError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	return mysqlErr.Number == mysqlErrDeadlock || mysqlErr.Number == mysqlErrLockWaitTimeout
}

// retryOnLockConflict runs txFn, a complete transaction without side effects
// outside it, again after a lock conflict. It returns the last error.
func retryOnLockConflict(op string, txFn func() error) error {
	return retryOnLockConflictWait(op, lockRetryWait, txFn)
}

func retryOnLockConflictWait(op string, wait func() time.Duration, txFn func() error) error {
	var err error
	for attempt := 1; attempt <= lockRetryAttempts; attempt++ {
		if err = txFn(); err == nil || !isLockConflictError(err) {
			return err
		}
		if attempt < lockRetryAttempts {
			time.Sleep(wait())
		}
	}
	slog.Warn("Database transaction failed on a lock conflict after all attempts",
		"op", op, "attempts", lockRetryAttempts, "error", err)
	return err
}

func lockRetryWait() time.Duration {
	// #nosec G404 -- retry jitter, not a secret.
	return lockRetryMinWait + rand.N(lockRetryMaxWait-lockRetryMinWait+1)
}
