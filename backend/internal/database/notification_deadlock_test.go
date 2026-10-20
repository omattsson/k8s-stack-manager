package database

import (
	"context"
	"errors"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// injectNotificationErrors makes the first len(errs) create (onCreate) or
// update statements on notifications fail with errs (nil lets that statement run).
// The callback runs before the statement, so a failed attempt writes nothing,
// as when InnoDB rolls the statement back after a deadlock. It returns a
// pointer to the number of statements seen.
func injectNotificationErrors(t *testing.T, db *gorm.DB, onCreate bool, errs []error) *int {
	t.Helper()
	calls := 0
	inject := func(tx *gorm.DB) {
		if tx.Statement.Table != "notifications" {
			return
		}
		calls++
		if calls <= len(errs) && errs[calls-1] != nil {
			_ = tx.AddError(errs[calls-1])
		}
	}
	if onCreate {
		require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:inject_error", inject))
	} else {
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:inject_error", inject))
	}
	return &calls
}

// TestGORMNotificationRepository_DeadlockRetry checks that Create,
// MarkAllAsRead and MarkAsRead run again after a deadlock (#648), give up
// after three attempts and do not retry other errors.
func TestGORMNotificationRepository_DeadlockRetry(t *testing.T) {
	t.Parallel()

	errOther := errors.New("driver: bad connection")
	tests := []struct {
		wantErr   error
		name      string
		op        string // create, mark_all_read, mark_read
		errs      []error
		wantCalls int
		wantRows  int64 // create: stored notifications; mark: unread notifications left
	}{
		{name: "create: deadlock, then success", op: "create", errs: []error{errFakeDeadlock}, wantCalls: 2, wantRows: 1},
		{name: "create: two deadlocks, then success", op: "create", errs: []error{errFakeDeadlock, errFakeDeadlock}, wantCalls: 3, wantRows: 1},
		{name: "create: deadlock three times", op: "create", errs: []error{errFakeDeadlock, errFakeDeadlock, errFakeDeadlock},
			wantErr: errFakeDeadlock, wantCalls: 3, wantRows: 0},
		{name: "create: lock wait timeout, no retry", op: "create", errs: []error{errFakeLockTimeout},
			wantErr: errFakeLockTimeout, wantCalls: 1, wantRows: 0},
		{name: "create: other error, no retry", op: "create", errs: []error{errOther}, wantErr: errOther, wantCalls: 1, wantRows: 0},
		{name: "mark all read: deadlock, then success", op: "mark_all_read", errs: []error{errFakeDeadlock}, wantCalls: 2, wantRows: 0},
		{name: "mark all read: deadlock three times", op: "mark_all_read", errs: []error{errFakeDeadlock, errFakeDeadlock, errFakeDeadlock},
			wantErr: errFakeDeadlock, wantCalls: 3, wantRows: 2},
		{name: "mark all read: other error, no retry", op: "mark_all_read", errs: []error{errOther}, wantErr: errOther, wantCalls: 1, wantRows: 2},
		{name: "mark read: deadlock, then success", op: "mark_read", errs: []error{errFakeDeadlock}, wantCalls: 2, wantRows: 1},
		{name: "mark read: deadlock three times", op: "mark_read", errs: []error{errFakeDeadlock, errFakeDeadlock, errFakeDeadlock},
			wantErr: errFakeDeadlock, wantCalls: 3, wantRows: 2},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := setupTestDBWithAllTables(t)
			repo := NewGORMNotificationRepository(db)
			ctx := context.Background()

			if tt.op != "create" {
				for _, id := range []string{"n-1", "n-2"} {
					require.NoError(t, repo.Create(ctx, &models.Notification{ID: id, UserID: "u1", Type: "deploy", Title: id}))
				}
			}
			calls := injectNotificationErrors(t, db, tt.op == "create", tt.errs)

			var err error
			switch tt.op {
			case "create":
				err = repo.Create(ctx, &models.Notification{ID: "n-new", UserID: "u1", Type: "deploy", Title: "new"})
			case "mark_all_read":
				err = repo.MarkAllAsRead(ctx, "u1")
			default:
				err = repo.MarkAsRead(ctx, "n-1", "u1")
			}

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr, "the error of the last attempt")
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantCalls, *calls, "attempts")

			var rows int64
			q := db.Model(&models.Notification{}).Where("user_id = ?", "u1")
			if tt.op != "create" {
				q = q.Where("is_read = ?", false)
			}
			require.NoError(t, q.Count(&rows).Error)
			assert.Equal(t, tt.wantRows, rows)
		})
	}
}
