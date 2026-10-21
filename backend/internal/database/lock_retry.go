package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"backend/pkg/dberrors"

	"github.com/go-sql-driver/mysql"
)

const (
	mysqlErrDeadlock = 1213

	deadlockRetryAttempts = 3
	deadlockRetryMinWait  = 10 * time.Millisecond
	deadlockRetryMaxWait  = 50 * time.Millisecond
)

// deadlockRetries counts the attempts that ran again after a deadlock (read by tests).
var deadlockRetries atomic.Int64

// isDeadlockError reports whether InnoDB rolled back the transaction with a
// deadlock (1213). A lock wait timeout (1205) is not retried: each attempt
// can wait innodb_lock_wait_timeout.
func isDeadlockError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlErrDeadlock
}

// retryOnDeadlock runs txFn, a complete transaction without side effects
// outside it, again after a deadlock. It returns the last error. When ctx is
// done it stops, and the error matches the context error (withContextErr).
func retryOnDeadlock(ctx context.Context, op string, txFn func() error) error {
	return retryOnDeadlockWait(ctx, op, deadlockRetryWait, txFn)
}

func retryOnDeadlockWait(ctx context.Context, op string, wait func() time.Duration, txFn func() error) error {
	var err error
	for attempt := 1; attempt <= deadlockRetryAttempts; attempt++ {
		if err = txFn(); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return withContextErr(ctx, op, err)
		}
		if !isDeadlockError(err) {
			return err
		}
		if attempt < deadlockRetryAttempts {
			if !waitOrDone(ctx, wait()) {
				return withContextErr(ctx, op, err)
			}
			deadlockRetries.Add(1)
		}
	}
	slog.Warn("Database transaction failed on a deadlock after all attempts",
		"op", op, "attempts", deadlockRetryAttempts, "error", err)
	return err
}

func deadlockRetryWait() time.Duration {
	// #nosec G404 -- retry jitter, not a secret.
	return deadlockRetryMinWait + rand.N(deadlockRetryMaxWait-deadlockRetryMinWait+1)
}

// waitOrDone waits for d and reports false when ctx ends first.
func waitOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// withContextErr returns err of a failed transaction as a DatabaseError that
// matches the context error when ctx is done. database/sql can report a
// cancel inside a transaction as sql.ErrTxDone or a bad connection, which
// would give 500 instead of 499 (#656).
func withContextErr(ctx context.Context, op string, err error) error {
	ctxErr := ctx.Err()
	if err == nil || ctxErr == nil {
		return err
	}
	if !errors.Is(err, ctxErr) {
		err = fmt.Errorf("%w: %w", ctxErr, err)
	} else if _, ok := errors.AsType[*dberrors.DatabaseError](err); ok {
		return err
	}
	return dberrors.NewDatabaseError(op, err)
}
