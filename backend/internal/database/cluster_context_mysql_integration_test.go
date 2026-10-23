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

// setupClusterContextMySQL returns the raw DB for the lock holder and the
// cluster repository on a session like production (SkipDefaultTransaction,
// factory.go).
func setupClusterContextMySQL(t *testing.T, maxConns int) (*gorm.DB, *GORMClusterRepository) {
	t.Helper()
	db, _, _ := setupInstanceContextMySQL(t, maxConns)
	session := db.Session(&gorm.Session{SkipDefaultTransaction: true})
	return db, NewGORMClusterRepository(session, "test-encryption-key-for-integration")
}

// seedMySQLCluster stores a cluster with a kubeconfig path.
func seedMySQLCluster(t *testing.T, repo *GORMClusterRepository, id string, isDefault bool) {
	t.Helper()
	require.NoError(t, repo.Create(context.Background(), &models.Cluster{
		ID: id, Name: id, APIServerURL: "https://" + id + ".example.com", KubeconfigPath: "/kc/" + id,
		HealthStatus: models.ClusterUnreachable, IsDefault: isDefault,
	}))
}

// defaultClusterIDs returns the IDs of the clusters marked as default.
func defaultClusterIDs(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var ids []string
	require.NoError(t, db.Model(&models.Cluster{}).Where("is_default = ?", true).Order("id").Pluck("id", &ids).Error)
	return ids
}

// TestDatabaseMySQL_ClusterContextEndsLockWait checks on MySQL that the
// caller context ends a cluster query that waits for a row lock (#656): the
// call returns the context error long before innodb_lock_wait_timeout, and
// the pool still serves the next query.
func TestDatabaseMySQL_ClusterContextEndsLockWait(t *testing.T) {
	ops := []struct {
		call func(ctx context.Context, repo *GORMClusterRepository, target string) error
		name string
	}{
		{name: "SetDefault", call: func(ctx context.Context, repo *GORMClusterRepository, target string) error {
			return repo.SetDefault(ctx, target)
		}},
		{name: "Update", call: func(ctx context.Context, repo *GORMClusterRepository, target string) error {
			// A cancelled autocommit statement can still run on the server
			// after the lock is free, so the test checks only the client side.
			return repo.Update(ctx, &models.Cluster{
				ID: target, Name: target, APIServerURL: "https://renamed.example.com", KubeconfigPath: "/kc/" + target,
				HealthStatus: models.ClusterHealthy,
			})
		}},
		{name: "Delete", call: func(ctx context.Context, repo *GORMClusterRepository, target string) error {
			return repo.Delete(ctx, target)
		}},
	}
	for _, op := range ops {
		for _, tt := range userContextCases {
			t.Run(op.name+"/"+tt.name, func(t *testing.T) {
				db, repo := setupClusterContextMySQL(t, 2)
				target := "cl-lock-" + tt.name
				seedMySQLCluster(t, repo, "cl-default-"+tt.name, true)
				seedMySQLCluster(t, repo, target, false)
				release := lockTableRows(t, db, "clusters", "id = ?", target)

				ctx, cancel := tt.ctx()
				defer cancel()
				start := time.Now()
				err := op.call(ctx, repo, target)
				elapsed := time.Since(start)

				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

				release()
				nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer nextCancel()
				_, err = repo.FindDefault(nextCtx)
				require.NoError(t, err, "the pool serves the next query")
			})
		}
	}
}

// TestDatabaseMySQL_ClusterSetDefaultContextCommitsNothing checks on MySQL
// that a context that ends inside the SetDefault transaction commits nothing
// (#656). The transaction waits for the row lock of the new default (the
// locking read scans all rows). The error matches the context error, the old
// default stays the only default, and the pool serves the next SetDefault.
func TestDatabaseMySQL_ClusterSetDefaultContextCommitsNothing(t *testing.T) {
	for _, tt := range userContextCases {
		t.Run(tt.name, func(t *testing.T) {
			db, repo := setupClusterContextMySQL(t, 3)
			oldDefault := "cl-old-" + tt.name
			target := "cl-new-" + tt.name
			seedMySQLCluster(t, repo, oldDefault, true)
			seedMySQLCluster(t, repo, target, false)
			release := lockTableRows(t, db, "clusters", "id = ?", target)

			ctx, cancel := tt.ctx()
			defer cancel()
			start := time.Now()
			err := repo.SetDefault(ctx, target)
			elapsed := time.Since(start)

			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Less(t, elapsed, 3*time.Second, "the context, not the lock wait timeout, ends the call")

			release()
			// The transaction never sent COMMIT; the server rolls it back.
			assert.Never(t, func() bool {
				var ids []string
				db.Model(&models.Cluster{}).Where("is_default = ?", true).Pluck("id", &ids)
				return len(ids) != 1 || ids[0] != oldDefault
			}, time.Second, 100*time.Millisecond, "the cancelled SetDefault did not commit")

			nextCtx, nextCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer nextCancel()
			require.NoError(t, repo.SetDefault(nextCtx, target), "the pool serves the next SetDefault")
			assert.Equal(t, []string{target}, defaultClusterIDs(t, db))
		})
	}
}
