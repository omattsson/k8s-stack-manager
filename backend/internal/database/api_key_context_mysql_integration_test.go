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

// TestDatabaseMySQL_APIKeyContextEndsLockWait checks on MySQL that the caller
// context ends an API key query that waits for a row lock (#656): the call
// returns the context error long before innodb_lock_wait_timeout, and the
// pool still serves the next query.
func TestDatabaseMySQL_APIKeyContextEndsLockWait(t *testing.T) {
	db := setupMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// One connection for the lock holder and one for the waiting query: a
	// broken connection left in the pool would fail the last query.
	sqlDB.SetMaxOpenConns(2)

	var lockWaitSeconds int
	require.NoError(t, db.Raw("SELECT @@innodb_lock_wait_timeout").Scan(&lockWaitSeconds).Error)
	require.GreaterOrEqual(t, lockWaitSeconds, 5, "the test needs a lock wait timeout of at least 5s")

	// Production sets SkipDefaultTransaction (factory.go).
	repo := NewGORMAPIKeyRepository(db.Session(&gorm.Session{SkipDefaultTransaction: true}))

	tests := []struct {
		name    string
		keyID   string
		ctx     func() (context.Context, context.CancelFunc)
		wantErr error
	}{
		{
			name:  "deadline",
			keyID: "k-lock-deadline",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 300*time.Millisecond)
			},
			wantErr: context.DeadlineExceeded,
		},
		{
			name:  "cancel",
			keyID: "k-lock-cancel",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				timer := time.AfterFunc(300*time.Millisecond, cancel)
				return ctx, func() { timer.Stop(); cancel() }
			},
			wantErr: context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userID := "u-" + tt.keyID
			require.NoError(t, repo.Create(context.Background(),
				&models.APIKey{ID: tt.keyID, UserID: userID, Name: "lock", Prefix: tt.keyID, KeyHash: "h"}))

			tx := db.Begin()
			require.NoError(t, tx.Error)
			rolledBack := false
			t.Cleanup(func() {
				if !rolledBack {
					tx.Rollback()
				}
			})
			var held models.APIKey
			require.NoError(t, tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", tt.keyID).First(&held).Error)

			ctx, cancel := tt.ctx()
			defer cancel()
			start := time.Now()
			// A cancelled statement can still run on the server after the lock
			// is free, so the test checks only the client side.
			err := repo.UpdateLastUsed(ctx, userID, tt.keyID, time.Now())
			elapsed := time.Since(start)

			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

			require.NoError(t, tx.Rollback().Error)
			rolledBack = true

			nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer nextCancel()
			keys, err := repo.ListByUser(nextCtx, userID)
			require.NoError(t, err, "the pool serves the next query")
			assert.Len(t, keys, 1)
		})
	}
}
