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

// setupInstanceQuotaOverrideRepo creates a fresh SQLite DB with all tables
// created via GORM's AutoMigrate, then returns a GormInstanceQuotaOverrideRepository.
func setupInstanceQuotaOverrideRepo(t *testing.T) *GormInstanceQuotaOverrideRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGormInstanceQuotaOverrideRepository(db)
}

func intPtr(v int) *int { return &v }

func TestGormInstanceQuotaOverrideRepository_GetByInstanceID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		seed       *models.InstanceQuotaOverride
		instanceID string
		wantErr    bool
	}{
		{
			name: "found",
			seed: &models.InstanceQuotaOverride{
				StackInstanceID: "si-get-1",
				PodLimit:        intPtr(5),
				CPULimit:        "1",
			},
			instanceID: "si-get-1",
			wantErr:    false,
		},
		{
			name:       "not found",
			seed:       nil,
			instanceID: "si-get-missing",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupInstanceQuotaOverrideRepo(t)
			ctx := context.Background()

			if tt.seed != nil {
				require.NoError(t, repo.Upsert(ctx, tt.seed))
			}

			result, err := repo.GetByInstanceID(ctx, tt.instanceID)
			if tt.wantErr {
				assert.Error(t, err)
				var dbErr *dberrors.DatabaseError
				assert.True(t, errors.As(err, &dbErr))
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, tt.instanceID, result.StackInstanceID)
			}
		})
	}
}

func TestGormInstanceQuotaOverrideRepository_Upsert(t *testing.T) {
	t.Parallel()

	t.Run("creates new override", func(t *testing.T) {
		t.Parallel()
		repo := setupInstanceQuotaOverrideRepo(t)
		ctx := context.Background()

		override := &models.InstanceQuotaOverride{
			StackInstanceID: "si-upsert-new",
			CPURequest:      "250m",
			CPULimit:        "500m",
			MemoryRequest:   "128Mi",
			MemoryLimit:     "256Mi",
			PodLimit:        intPtr(10),
		}
		err := repo.Upsert(ctx, override)
		require.NoError(t, err)
		assert.NotEmpty(t, override.ID)
		assert.False(t, override.CreatedAt.IsZero())
		assert.False(t, override.UpdatedAt.IsZero())

		found, err := repo.GetByInstanceID(ctx, "si-upsert-new")
		require.NoError(t, err)
		assert.Equal(t, "250m", found.CPURequest)
		assert.Equal(t, 10, *found.PodLimit)
	})

	t.Run("updates existing override", func(t *testing.T) {
		t.Parallel()
		repo := setupInstanceQuotaOverrideRepo(t)
		ctx := context.Background()

		initial := &models.InstanceQuotaOverride{
			StackInstanceID: "si-upsert-update",
			PodLimit:        intPtr(5),
		}
		require.NoError(t, repo.Upsert(ctx, initial))
		originalID := initial.ID
		originalCreatedAt := initial.CreatedAt

		updated := &models.InstanceQuotaOverride{
			StackInstanceID: "si-upsert-update",
			PodLimit:        intPtr(20),
			CPULimit:        "2",
		}
		err := repo.Upsert(ctx, updated)
		require.NoError(t, err)

		// Should reuse the same ID and preserve original CreatedAt.
		assert.Equal(t, originalID, updated.ID)
		assert.Equal(t, originalCreatedAt, updated.CreatedAt)

		found, err := repo.GetByInstanceID(ctx, "si-upsert-update")
		require.NoError(t, err)
		assert.Equal(t, 20, *found.PodLimit)
		assert.Equal(t, "2", found.CPULimit)
	})

	t.Run("creates override with nil pod_limit", func(t *testing.T) {
		t.Parallel()
		repo := setupInstanceQuotaOverrideRepo(t)
		ctx := context.Background()

		override := &models.InstanceQuotaOverride{
			StackInstanceID: "si-nil-pod",
			CPULimit:        "1",
		}
		err := repo.Upsert(ctx, override)
		require.NoError(t, err)

		found, err := repo.GetByInstanceID(ctx, "si-nil-pod")
		require.NoError(t, err)
		assert.Nil(t, found.PodLimit)
	})

	t.Run("validation error for missing stack_instance_id", func(t *testing.T) {
		t.Parallel()
		repo := setupInstanceQuotaOverrideRepo(t)
		ctx := context.Background()

		override := &models.InstanceQuotaOverride{
			PodLimit: intPtr(5),
		}
		err := repo.Upsert(ctx, override)
		assert.Error(t, err)

		var dbErr *dberrors.DatabaseError
		assert.True(t, errors.As(err, &dbErr))
	})

	t.Run("validation error for negative pod_limit", func(t *testing.T) {
		t.Parallel()
		repo := setupInstanceQuotaOverrideRepo(t)
		ctx := context.Background()

		override := &models.InstanceQuotaOverride{
			StackInstanceID: "si-neg-pod",
			PodLimit:        intPtr(-1),
		}
		err := repo.Upsert(ctx, override)
		assert.Error(t, err)
	})
}

func TestGormInstanceQuotaOverrideRepository_Delete(t *testing.T) {
	t.Parallel()

	t.Run("deletes existing override", func(t *testing.T) {
		t.Parallel()
		repo := setupInstanceQuotaOverrideRepo(t)
		ctx := context.Background()

		override := &models.InstanceQuotaOverride{
			StackInstanceID: "si-del-1",
			PodLimit:        intPtr(5),
		}
		require.NoError(t, repo.Upsert(ctx, override))

		err := repo.Delete(ctx, "si-del-1")
		require.NoError(t, err)

		_, err = repo.GetByInstanceID(ctx, "si-del-1")
		assert.Error(t, err, "should not find deleted override")
	})

	t.Run("returns not found for non-existent instance", func(t *testing.T) {
		t.Parallel()
		repo := setupInstanceQuotaOverrideRepo(t)
		ctx := context.Background()

		err := repo.Delete(ctx, "si-del-missing")
		assert.Error(t, err)

		var dbErr *dberrors.DatabaseError
		assert.True(t, errors.As(err, &dbErr))
		assert.True(t, errors.Is(dbErr.Err, dberrors.ErrNotFound))
	})
}
