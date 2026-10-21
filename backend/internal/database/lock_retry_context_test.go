package database

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cancelledTestContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx, cancel
}

// TestRetryOnDeadlock_ContextDone checks that a done context stops the retry,
// also during the wait, and that the error then matches the context error
// and keeps the error of the attempt (#656).
func TestRetryOnDeadlock_ContextDone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		attemptErr error
		ctx        func() (context.Context, context.CancelFunc)
		wantCtxErr error
		name       string
		wait       time.Duration
	}{
		{
			name:       "cancel during the wait",
			attemptErr: errFakeDeadlock,
			wait:       time.Hour,
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				timer := time.AfterFunc(20*time.Millisecond, cancel)
				return ctx, func() { timer.Stop(); cancel() }
			},
			wantCtxErr: context.Canceled,
		},
		{
			name:       "deadline during the wait",
			attemptErr: errFakeDeadlock,
			wait:       time.Hour,
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 20*time.Millisecond)
			},
			wantCtxErr: context.DeadlineExceeded,
		},
		{
			name:       "cancelled before a deadlock: no retry",
			attemptErr: errFakeDeadlock,
			ctx:        cancelledTestContext,
			wantCtxErr: context.Canceled,
		},
		{
			name:       "transaction done after a cancel",
			attemptErr: sql.ErrTxDone,
			ctx:        cancelledTestContext,
			wantCtxErr: context.Canceled,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := tt.ctx()
			defer cancel()

			calls := 0
			start := time.Now()
			err := retryOnDeadlockWait(ctx, "update_role", func() time.Duration { return tt.wait }, func() error {
				calls++
				return tt.attemptErr
			})

			assert.Less(t, time.Since(start), 5*time.Second, "the context ends the wait")
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantCtxErr)
			assert.ErrorIs(t, err, tt.attemptErr, "the error of the attempt stays")
			var dbErr *dberrors.DatabaseError
			assert.ErrorAs(t, err, &dbErr)
			assert.Equal(t, 1, calls, "no attempt after the context ended")
		})
	}
}

// TestRetryOnDeadlock_CommitBeforeCancel checks that a transaction that
// committed reports success even when the context ended afterwards.
func TestRetryOnDeadlock_CommitBeforeCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := cancelledTestContext()
	defer cancel()

	assert.NoError(t, retryOnDeadlockWait(ctx, "update_role", func() time.Duration { return 0 }, func() error { return nil }))
}

func TestWithContextErr(t *testing.T) {
	t.Parallel()

	wrappedCancel := dberrors.NewDatabaseError("find_by_id", context.Canceled)
	liveContext := func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }
	tests := []struct {
		err       error
		ctx       func() (context.Context, context.CancelFunc)
		wantSame  error // the input error, unchanged
		name      string
		wantIs    []error
		wantNil   bool
		wantDBErr bool
	}{
		{name: "nil error", ctx: cancelledTestContext, wantNil: true},
		{name: "live context: unchanged", err: errFakeLockTimeout, ctx: liveContext, wantSame: errFakeLockTimeout},
		{name: "cancelled DatabaseError: unchanged", err: wrappedCancel, ctx: cancelledTestContext, wantSame: wrappedCancel},
		{name: "raw context error: wrapped", err: context.Canceled, ctx: cancelledTestContext,
			wantIs: []error{context.Canceled}, wantDBErr: true},
		{name: "other error: wraps both", err: sql.ErrTxDone, ctx: cancelledTestContext,
			wantIs: []error{context.Canceled, sql.ErrTxDone}, wantDBErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := tt.ctx()
			defer cancel()

			got := withContextErr(ctx, "update_role", tt.err)

			switch {
			case tt.wantNil:
				assert.NoError(t, got)
			case tt.wantSame != nil:
				assert.Same(t, tt.wantSame, got)
			}
			for _, want := range tt.wantIs {
				assert.ErrorIs(t, got, want)
			}
			if tt.wantDBErr {
				var dbErr *dberrors.DatabaseError
				require.ErrorAs(t, got, &dbErr)
				assert.Equal(t, "update_role", dbErr.Op)
			}
		})
	}
}

// TestRetryOnDeadlock_CancelDuringWaitNotCounted checks that a retry that a
// cancel stops during the wait does not count as a retry.
// Not parallel: it reads deadlockRetries.
func TestRetryOnDeadlock_CancelDuringWaitNotCounted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	before := deadlockRetries.Load()
	err := retryOnDeadlockWait(ctx, "update_role", func() time.Duration {
		cancel()
		return time.Hour
	}, func() error { return errFakeDeadlock })

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, before, deadlockRetries.Load(), "no attempt ran again")
}
