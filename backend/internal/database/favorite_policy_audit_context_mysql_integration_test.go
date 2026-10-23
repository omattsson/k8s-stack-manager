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

// TestDatabaseMySQL_FavoritePolicyAuditContextEndsLockWait checks on MySQL
// that the caller context ends a user favorite, cleanup policy or audit log
// query that waits for a row lock (#656): the call returns the context error
// long before innodb_lock_wait_timeout, and the pool still serves the next
// query.
func TestDatabaseMySQL_FavoritePolicyAuditContextEndsLockWait(t *testing.T) {
	type repos struct {
		favorites *GORMUserFavoriteRepository
		policies  *GORMCleanupPolicyRepository
		audit     *GORMAuditLogRepository
	}
	ops := []struct {
		call  func(ctx context.Context, r repos) error
		name  string
		table string
		query string
		arg   string
	}{
		{
			name: "UserFavorite.Remove", table: "user_favorites", query: "user_id = ?", arg: "u-lock",
			call: func(ctx context.Context, r repos) error {
				return r.favorites.Remove(ctx, "u-lock", "template", "tmpl-lock")
			},
		},
		{
			name: "CleanupPolicy.Update", table: "cleanup_policies", query: "id = ?", arg: "p-lock",
			call: func(ctx context.Context, r repos) error {
				return r.policies.Update(ctx, &models.CleanupPolicy{ID: "p-lock", Name: "renamed", ClusterID: "all", Action: "stop", Condition: "idle_days:7", Schedule: "0 2 * * *"})
			},
		},
		{
			name: "CleanupPolicy.Delete", table: "cleanup_policies", query: "id = ?", arg: "p-lock",
			call: func(ctx context.Context, r repos) error { return r.policies.Delete(ctx, "p-lock") },
		},
		{
			name: "AuditLog.Create", table: "audit_logs", query: "id = ?", arg: "a-lock",
			call: func(ctx context.Context, r repos) error {
				return r.audit.Create(ctx, &models.AuditLog{ID: "a-lock", UserID: "u1", Action: "create", EntityType: "stack_instance", EntityID: "si-1"})
			},
		},
	}
	for _, op := range ops {
		for _, tt := range userContextCases {
			t.Run(op.name+"/"+tt.name, func(t *testing.T) {
				db, _, _ := setupInstanceContextMySQL(t, 2)
				session := db.Session(&gorm.Session{SkipDefaultTransaction: true})
				r := repos{
					favorites: NewGORMUserFavoriteRepository(session),
					policies:  NewGORMCleanupPolicyRepository(session),
					audit:     NewGORMAuditLogRepository(session),
				}
				seed := context.Background()
				require.NoError(t, r.favorites.Add(seed, &models.UserFavorite{UserID: "u-lock", EntityType: "template", EntityID: "tmpl-lock"}))
				require.NoError(t, r.policies.Create(seed, &models.CleanupPolicy{ID: "p-lock", Name: "lock", ClusterID: "all", Action: "stop", Condition: "idle_days:7", Schedule: "0 2 * * *"}))
				require.NoError(t, r.audit.Create(seed, &models.AuditLog{ID: "a-lock", UserID: "u1", Action: "create", EntityType: "stack_instance", EntityID: "si-0"}))
				release := lockTableRows(t, db, op.table, op.query, op.arg)

				ctx, cancel := tt.ctx()
				defer cancel()
				start := time.Now()
				// A cancelled autocommit statement can still run on the server
				// after the lock is free, so the test checks only the client side.
				err := op.call(ctx, r)
				elapsed := time.Since(start)

				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

				release()
				nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer nextCancel()
				_, err = r.favorites.List(nextCtx, "u-lock")
				require.NoError(t, err, "the pool serves the next query")
				_, err = r.policies.List(nextCtx)
				require.NoError(t, err, "the pool serves the next query")
				_, err = r.audit.List(nextCtx, models.AuditLogFilters{Limit: 5})
				require.NoError(t, err, "the pool serves the next query")
			})
		}
	}
}
