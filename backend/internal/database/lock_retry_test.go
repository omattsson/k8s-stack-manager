package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var (
	errFakeDeadlock    = &mysql.MySQLError{Number: mysqlErrDeadlock, Message: "Deadlock found when trying to get lock; try restarting transaction"}
	errFakeLockTimeout = &mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded; try restarting transaction"}
	errFakeDuplicate   = &mysql.MySQLError{Number: mysqlErrDuplicateEntry, Message: "Duplicate entry"}
)

func TestRetryOnDeadlock(t *testing.T) {
	t.Parallel()

	wrappedDeadlock := dberrors.NewDatabaseError("update_role", errFakeDeadlock)
	errBadConn := errors.New("driver: bad connection")
	tests := []struct {
		wantErr   error
		name      string
		results   []error // result of each attempt; nil after the end
		wantCalls int
		wantWaits int
	}{
		{name: "success at once", results: []error{nil}, wantCalls: 1},
		{name: "deadlock, then success", results: []error{errFakeDeadlock, nil}, wantCalls: 2, wantWaits: 1},
		{name: "wrapped deadlock, then success", results: []error{wrappedDeadlock, nil}, wantCalls: 2, wantWaits: 1},
		{name: "two deadlocks, then success", results: []error{errFakeDeadlock, wrappedDeadlock, nil}, wantCalls: 3, wantWaits: 2},
		{name: "deadlock three times", results: []error{errFakeDeadlock, errFakeDeadlock, wrappedDeadlock},
			wantErr: wrappedDeadlock, wantCalls: 3, wantWaits: 2},
		{name: "lock wait timeout: no retry", results: []error{errFakeLockTimeout, nil}, wantErr: errFakeLockTimeout, wantCalls: 1},
		{name: "deadlock, then lock wait timeout", results: []error{errFakeDeadlock, errFakeLockTimeout, nil},
			wantErr: errFakeLockTimeout, wantCalls: 2, wantWaits: 1},
		{name: "duplicate key: no retry", results: []error{errFakeDuplicate, nil}, wantErr: errFakeDuplicate, wantCalls: 1},
		{name: "domain error: no retry", results: []error{models.ErrLastAdmin, nil}, wantErr: models.ErrLastAdmin, wantCalls: 1},
		{name: "other error: no retry", results: []error{errBadConn, nil}, wantErr: errBadConn, wantCalls: 1},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls, waits := 0, 0
			err := retryOnDeadlockWait(context.Background(), "test", func() time.Duration { waits++; return 0 }, func() error {
				calls++
				if calls > len(tt.results) {
					return nil
				}
				return tt.results[calls-1]
			})

			if tt.wantErr != nil {
				assert.Same(t, tt.wantErr, err, "the error of the last attempt, unchanged")
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.wantCalls, calls)
			assert.Equal(t, tt.wantWaits, waits)
		})
	}
}

// Not parallel: it swaps the default logger and reads deadlockRetries.
func TestRetryOnDeadlock_LogsAttempts(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	before := deadlockRetries.Load()
	err := retryOnDeadlockWait(context.Background(), "delete", func() time.Duration { return 0 }, func() error { return errFakeDeadlock })

	require.ErrorIs(t, err, errFakeDeadlock)
	assert.Equal(t, int64(2), deadlockRetries.Load()-before, "two attempts ran again")
	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, "level=WARN"), out)
	assert.Contains(t, out, "op=delete")
	assert.Contains(t, out, "attempts=3")
}

func TestDeadlockRetryWait(t *testing.T) {
	t.Parallel()

	for i := 0; i < 1000; i++ {
		d := deadlockRetryWait()
		require.GreaterOrEqual(t, d, deadlockRetryMinWait)
		require.LessOrEqual(t, d, deadlockRetryMaxWait)
	}
}

// TestGuardedAdminChange_RetriesTransaction checks that a deadlock runs
// the whole transaction again and rolls back the writes of the failed attempt.
func TestGuardedAdminChange_RetriesTransaction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		attemptErr  []error
		wantErr     error
		name        string
		wantDisplay string
		wantCalls   int
	}{
		{name: "deadlock, then success", attemptErr: []error{errFakeDeadlock, nil}, wantCalls: 2, wantDisplay: "attempt-2"},
		{name: "deadlock every time", attemptErr: []error{errFakeDeadlock, errFakeDeadlock, errFakeDeadlock},
			wantErr: errFakeDeadlock, wantCalls: 3, wantDisplay: "target"},
		{name: "last admin: no retry", attemptErr: []error{models.ErrLastAdmin},
			wantErr: models.ErrLastAdmin, wantCalls: 1, wantDisplay: "target"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupUserRepo(t)
			require.NoError(t, repo.Create(context.Background(), &models.User{ID: "admin-1", Username: "admin-1", Role: models.RoleAdmin}))
			require.NoError(t, repo.Create(context.Background(), &models.User{ID: "user-1", Username: "user-1", DisplayName: "target", Role: models.RoleUser}))

			calls := 0
			err := repo.guardedAdminChange(context.Background(), "test", "admin-1", "user-1", func(tx *gorm.DB, g guardTarget) error {
				calls++
				display := fmt.Sprintf("attempt-%d", calls)
				if err := updateUserColumns(tx, "test", g.user.ID, map[string]interface{}{"display_name": display}); err != nil {
					return err
				}
				return tt.attemptErr[calls-1]
			})

			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.wantCalls, calls)
			u, err := repo.FindByID(context.Background(), "user-1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantDisplay, u.DisplayName, "only the committed attempt writes")
		})
	}
}
