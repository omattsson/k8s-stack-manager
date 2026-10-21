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
)

// setupRefreshTokenContextMySQL returns the raw DB for the lock holder and
// the refresh token repository on a session like production
// (SkipDefaultTransaction, factory.go). The refresh_tokens table is empty.
func setupRefreshTokenContextMySQL(t *testing.T, maxConns int) (*gorm.DB, *GORMRefreshTokenRepository) {
	t.Helper()
	db, _, _ := setupInstanceContextMySQL(t, maxConns)
	require.NoError(t, db.AutoMigrate(&models.RefreshToken{}))
	require.NoError(t, db.Exec("DELETE FROM refresh_tokens").Error)
	return db, NewGORMRefreshTokenRepository(db.Session(&gorm.Session{SkipDefaultTransaction: true}))
}

// seedMySQLRefreshToken stores an active token of user u1 in family.
func seedMySQLRefreshToken(t *testing.T, repo *GORMRefreshTokenRepository, id, family string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.Create(context.Background(), &models.RefreshToken{
		ID: id, UserID: "u1", FamilyID: family, TokenHash: "hash-" + id,
		ExpiresAt: now.Add(time.Hour), LastActivity: now, CreatedAt: now, SessionStartedAt: now,
	}))
}

// TestDatabaseMySQL_RefreshTokenContextEndsLockWait checks on MySQL that the
// caller context ends a refresh token query that waits for a row lock
// (#656): the call returns the context error long before
// innodb_lock_wait_timeout, and the pool still serves the next query.
func TestDatabaseMySQL_RefreshTokenContextEndsLockWait(t *testing.T) {
	ops := []struct {
		call func(ctx context.Context, repo *GORMRefreshTokenRepository, id string) error
		name string
	}{
		{name: "MarkRotatedIfActive in WithTx", call: func(ctx context.Context, repo *GORMRefreshTokenRepository, id string) error {
			return repo.WithTx(ctx, func(tx models.RefreshTokenRepository) error {
				_, err := tx.MarkRotatedIfActive(ctx, id, time.Now().UTC())
				return err
			})
		}},
		{name: "RevokeFamily", call: func(ctx context.Context, repo *GORMRefreshTokenRepository, id string) error {
			// A cancelled autocommit statement can still run on the server
			// after the lock is free, so the test checks only the client side.
			return repo.RevokeFamily(ctx, id)
		}},
	}
	for _, op := range ops {
		for _, tt := range userContextCases {
			t.Run(op.name+"/"+tt.name, func(t *testing.T) {
				db, repo := setupRefreshTokenContextMySQL(t, 2)
				id := "rt-lock-" + tt.name
				seedMySQLRefreshToken(t, repo, id, id)
				release := lockTableRows(t, db, "refresh_tokens", "id = ?", id)

				ctx, cancel := tt.ctx()
				defer cancel()
				start := time.Now()
				err := op.call(ctx, repo, id)
				elapsed := time.Since(start)

				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

				release()
				nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer nextCancel()
				_, err = repo.FindByTokenHash(nextCtx, "hash-"+id)
				require.NoError(t, err, "the pool serves the next query")
			})
		}
	}
}

// TestDatabaseMySQL_RefreshTokenTxContextCommitsNothing checks on MySQL that
// a context that ends inside a rotation commits nothing (#656): the
// transaction consumes a token and stores its successor, then waits for the
// row lock of another token. The error matches the context error, the
// consume and the successor are rolled back, and the pool serves the next
// rotation.
func TestDatabaseMySQL_RefreshTokenTxContextCommitsNothing(t *testing.T) {
	for _, tt := range userContextCases {
		t.Run(tt.name, func(t *testing.T) {
			db, repo := setupRefreshTokenContextMySQL(t, 3)
			consumed := "rt-a-" + tt.name
			held := "rt-held-" + tt.name
			successor := "rt-next-" + tt.name
			seedMySQLRefreshToken(t, repo, consumed, consumed)
			seedMySQLRefreshToken(t, repo, held, held)
			release := lockTableRows(t, db, "refresh_tokens", "id = ?", held)

			ctx, cancel := tt.ctx()
			defer cancel()
			start := time.Now()
			err := repo.WithTx(ctx, func(tx models.RefreshTokenRepository) error {
				affected, err := tx.MarkRotatedIfActive(ctx, consumed, time.Now().UTC())
				if err != nil {
					return err
				}
				if affected != 1 {
					return assert.AnError
				}
				now := time.Now().UTC().Truncate(time.Second)
				if err := tx.Create(ctx, &models.RefreshToken{
					ID: successor, UserID: "u1", FamilyID: consumed, TokenHash: "hash-" + successor,
					ExpiresAt: now.Add(time.Hour), LastActivity: now, CreatedAt: now, SessionStartedAt: now,
				}); err != nil {
					return err
				}
				_, err = tx.MarkRotatedIfActive(ctx, held, time.Now().UTC())
				return err
			})
			elapsed := time.Since(start)

			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

			release()
			// The transaction never sent COMMIT; the server rolls it back.
			assert.Never(t, func() bool {
				var n int64
				db.Table("refresh_tokens").Where("id = ? OR (id = ? AND revoked = ?)", successor, consumed, true).Count(&n)
				return n != 0
			}, time.Second, 100*time.Millisecond, "the cancelled rotation did not commit")

			nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer nextCancel()
			var affected int64
			require.NoError(t, repo.WithTx(nextCtx, func(tx models.RefreshTokenRepository) error {
				var err error
				affected, err = tx.MarkRotatedIfActive(nextCtx, consumed, time.Now().UTC())
				return err
			}), "the pool serves the next rotation")
			assert.Equal(t, int64(1), affected, "the token was still active")
		})
	}
}
