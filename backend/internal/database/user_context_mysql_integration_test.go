//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// userContextCases are the contexts of the MySQL context tests: a deadline
// and a cancel, both 300ms after the start.
var userContextCases = []struct {
	name    string
	ctx     func() (context.Context, context.CancelFunc)
	wantErr error
}{
	{
		name: "deadline",
		ctx: func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 300*time.Millisecond)
		},
		wantErr: context.DeadlineExceeded,
	},
	{
		name: "cancel",
		ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			timer := time.AfterFunc(300*time.Millisecond, cancel)
			return ctx, func() { timer.Stop(); cancel() }
		},
		wantErr: context.Canceled,
	},
}

// setupUserContextMySQL returns the repository on a session like production
// (SkipDefaultTransaction, factory.go) and the raw DB for the lock holder.
func setupUserContextMySQL(t *testing.T, maxConns int) (*gorm.DB, *GORMUserRepository) {
	t.Helper()
	db := setupMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// A broken connection left in the pool would fail the last query.
	sqlDB.SetMaxOpenConns(maxConns)

	var lockWaitSeconds int
	require.NoError(t, db.Raw("SELECT @@innodb_lock_wait_timeout").Scan(&lockWaitSeconds).Error)
	require.GreaterOrEqual(t, lockWaitSeconds, 5, "the test needs a lock wait timeout of at least 5s")

	return db, NewGORMUserRepository(db.Session(&gorm.Session{SkipDefaultTransaction: true}))
}

// lockRows starts a transaction that locks the rows of query with SELECT ...
// FOR UPDATE. The returned function rolls it back (also at cleanup).
func lockRows(t *testing.T, db *gorm.DB, query string, args ...any) func() {
	t.Helper()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	done := false
	rollback := func() {
		if !done {
			done = true
			require.NoError(t, tx.Rollback().Error)
		}
	}
	t.Cleanup(func() {
		if !done {
			tx.Rollback()
		}
	})
	var held []models.User
	require.NoError(t, tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(query, args...).Find(&held).Error)
	require.NotEmpty(t, held)
	return rollback
}

// TestDatabaseMySQL_UserContextEndsLockWait checks on MySQL that the caller
// context ends a user query that waits for a row lock (#656): the call
// returns the context error long before innodb_lock_wait_timeout, and the
// pool still serves the next query.
func TestDatabaseMySQL_UserContextEndsLockWait(t *testing.T) {
	for _, tt := range userContextCases {
		t.Run(tt.name, func(t *testing.T) {
			db, repo := setupUserContextMySQL(t, 2)
			userID := "u-lock-" + tt.name
			require.NoError(t, repo.Create(context.Background(),
				&models.User{ID: userID, Username: userID, Role: models.RoleUser, PasswordHash: "old"}))
			release := lockRows(t, db, "id = ?", userID)

			ctx, cancel := tt.ctx()
			defer cancel()
			start := time.Now()
			// A cancelled autocommit statement can still run on the server
			// after the lock is free, so the test checks only the client side.
			err := repo.UpdatePassword(ctx, userID, "new")
			elapsed := time.Since(start)

			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

			release()
			nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer nextCancel()
			u, err := repo.FindByID(nextCtx, userID)
			require.NoError(t, err, "the pool serves the next query")
			assert.Equal(t, userID, u.ID)
		})
	}
}

// TestDatabaseMySQL_UserGuardedChangeContext checks on MySQL that a context
// that ends while a guarded admin change waits for the admin row locks gives
// an error that matches the context error (499 for a cancel, #656), and that
// the change does not commit.
func TestDatabaseMySQL_UserGuardedChangeContext(t *testing.T) {
	for _, tt := range userContextCases {
		t.Run(tt.name, func(t *testing.T) {
			db, repo := setupUserContextMySQL(t, 3)
			for _, u := range []*models.User{
				{ID: "admin-1", Username: "admin-1", Role: models.RoleAdmin},
				{ID: "admin-2", Username: "admin-2", Role: models.RoleAdmin},
				{ID: "user-1", Username: "user-1", Role: models.RoleUser},
			} {
				require.NoError(t, repo.Create(context.Background(), u))
			}
			release := lockRows(t, db, "role = ?", models.RoleAdmin)

			ctx, cancel := tt.ctx()
			defer cancel()
			start := time.Now()
			_, err := repo.UpdateRole(ctx, "admin-1", "user-1", models.RoleDevOps)
			elapsed := time.Since(start)

			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

			release()
			// The transaction never sent COMMIT; the server rolls it back.
			assert.Never(t, func() bool {
				u, findErr := repo.FindByID(context.Background(), "user-1")
				return findErr != nil || u.Role != models.RoleUser
			}, time.Second, 100*time.Millisecond, "the cancelled role change did not commit")

			nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer nextCancel()
			old, err := repo.UpdateRole(nextCtx, "admin-1", "user-1", models.RoleDevOps)
			require.NoError(t, err, "the pool serves the next guarded change")
			assert.Equal(t, models.RoleUser, old)
		})
	}
}
