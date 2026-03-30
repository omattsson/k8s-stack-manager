package database

import (
	"context"
	"errors"
	"testing"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupResourceQuotaRepo creates a fresh SQLite DB with all tables created via
// GORM's AutoMigrate, then returns a GormResourceQuotaRepository ready for testing.
func setupResourceQuotaRepo(t *testing.T) *GormResourceQuotaRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGormResourceQuotaRepository(db)
}

func TestGormResourceQuotaRepository_GetByClusterID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		seed      *models.ResourceQuotaConfig
		clusterID string
		wantErr   bool
		wantFound bool
	}{
		{
			name: "found",
			seed: &models.ResourceQuotaConfig{
				ClusterID: "c-get-1",
				PodLimit:  10,
				CPULimit:  "2",
			},
			clusterID: "c-get-1",
			wantFound: true,
		},
		{
			name:      "not found",
			seed:      nil,
			clusterID: "c-get-missing",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupResourceQuotaRepo(t)
			ctx := context.Background()

			if tt.seed != nil {
				require.NoError(t, repo.Upsert(ctx, tt.seed))
			}

			result, err := repo.GetByClusterID(ctx, tt.clusterID)
			if tt.wantErr {
				assert.Error(t, err)
				var dbErr *dberrors.DatabaseError
				assert.True(t, errors.As(err, &dbErr))
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, tt.clusterID, result.ClusterID)
			}
		})
	}
}

func TestGormResourceQuotaRepository_Upsert(t *testing.T) {
	t.Parallel()

	t.Run("creates new config", func(t *testing.T) {
		t.Parallel()
		repo := setupResourceQuotaRepo(t)
		ctx := context.Background()

		config := &models.ResourceQuotaConfig{
			ClusterID:     "c-upsert-new",
			CPURequest:    "500m",
			CPULimit:      "1",
			MemoryRequest: "256Mi",
			MemoryLimit:   "512Mi",
			PodLimit:      20,
		}
		err := repo.Upsert(ctx, config)
		require.NoError(t, err)
		assert.NotEmpty(t, config.ID, "ID should be assigned")
		assert.False(t, config.CreatedAt.IsZero())
		assert.False(t, config.UpdatedAt.IsZero())

		found, err := repo.GetByClusterID(ctx, "c-upsert-new")
		require.NoError(t, err)
		assert.Equal(t, "500m", found.CPURequest)
		assert.Equal(t, 20, found.PodLimit)
	})

	t.Run("updates existing config", func(t *testing.T) {
		t.Parallel()
		repo := setupResourceQuotaRepo(t)
		ctx := context.Background()

		initial := &models.ResourceQuotaConfig{
			ClusterID: "c-upsert-update",
			PodLimit:  10,
		}
		require.NoError(t, repo.Upsert(ctx, initial))
		originalID := initial.ID

		updated := &models.ResourceQuotaConfig{
			ClusterID: "c-upsert-update",
			PodLimit:  50,
			CPULimit:  "4",
		}
		err := repo.Upsert(ctx, updated)
		require.NoError(t, err)

		// Should reuse the same ID.
		assert.Equal(t, originalID, updated.ID)

		found, err := repo.GetByClusterID(ctx, "c-upsert-update")
		require.NoError(t, err)
		assert.Equal(t, 50, found.PodLimit)
		assert.Equal(t, "4", found.CPULimit)
	})

	t.Run("validation error for missing cluster_id", func(t *testing.T) {
		t.Parallel()
		repo := setupResourceQuotaRepo(t)
		ctx := context.Background()

		config := &models.ResourceQuotaConfig{
			PodLimit: 10,
		}
		err := repo.Upsert(ctx, config)
		assert.Error(t, err)

		var dbErr *dberrors.DatabaseError
		assert.True(t, errors.As(err, &dbErr))
	})

	t.Run("validation error for negative pod_limit", func(t *testing.T) {
		t.Parallel()
		repo := setupResourceQuotaRepo(t)
		ctx := context.Background()

		config := &models.ResourceQuotaConfig{
			ClusterID: "c-neg-pod",
			PodLimit:  -1,
		}
		err := repo.Upsert(ctx, config)
		assert.Error(t, err)
	})
}

func TestGormResourceQuotaRepository_Delete(t *testing.T) {
	t.Parallel()

	t.Run("deletes existing config", func(t *testing.T) {
		t.Parallel()
		repo := setupResourceQuotaRepo(t)
		ctx := context.Background()

		config := &models.ResourceQuotaConfig{
			ClusterID: "c-del-1",
			PodLimit:  5,
		}
		require.NoError(t, repo.Upsert(ctx, config))

		err := repo.Delete(ctx, "c-del-1")
		require.NoError(t, err)

		_, err = repo.GetByClusterID(ctx, "c-del-1")
		assert.Error(t, err, "should not find deleted config")
	})

	t.Run("returns not found for non-existent cluster", func(t *testing.T) {
		t.Parallel()
		repo := setupResourceQuotaRepo(t)
		ctx := context.Background()

		err := repo.Delete(ctx, "c-del-missing")
		assert.Error(t, err)

		var dbErr *dberrors.DatabaseError
		assert.True(t, errors.As(err, &dbErr))
		assert.True(t, errors.Is(dbErr.Err, dberrors.ErrNotFound))
	})
}
