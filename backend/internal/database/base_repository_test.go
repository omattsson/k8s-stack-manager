package database

import (
	"testing"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupBaseRepoTestDB creates an in-memory SQLite DB with the CleanupPolicy
// table, returning a BaseRepository ready for testing.
func setupBaseRepoTestDB(t *testing.T) *BaseRepository[models.CleanupPolicy] {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.CleanupPolicy{}))
	return &BaseRepository[models.CleanupPolicy]{DB: db}
}

func TestBaseRepository_FindByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		seed      bool
		lookupID  string
		wantErr   bool
		wantSentinel error
	}{
		{
			name:     "found",
			seed:     true,
			lookupID: "", // filled in test body
		},
		{
			name:         "not found",
			seed:         false,
			lookupID:     "nonexistent-id",
			wantErr:      true,
			wantSentinel: dberrors.ErrNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupBaseRepoTestDB(t)

			id := tt.lookupID
			if tt.seed {
				id = uuid.New().String()
				policy := models.CleanupPolicy{
					Name:     "test-policy",
					Schedule: "0 * * * *",
					Action:   "stop",
				}
				policy.ID = id
				require.NoError(t, repo.Create(&policy))
			}

			result, err := repo.FindByID(id)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantSentinel)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, id, result.ID)
			}
		})
	}
}

func TestBaseRepository_Delete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		seed         bool
		deleteID     string
		wantErr      bool
		wantSentinel error
	}{
		{
			name: "success",
			seed: true,
		},
		{
			name:         "not found",
			seed:         false,
			deleteID:     "nonexistent-id",
			wantErr:      true,
			wantSentinel: dberrors.ErrNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupBaseRepoTestDB(t)

			id := tt.deleteID
			if tt.seed {
				id = uuid.New().String()
				policy := models.CleanupPolicy{
					Name:     "to-delete",
					Schedule: "0 * * * *",
					Action:   "stop",
				}
				policy.ID = id
				require.NoError(t, repo.Create(&policy))
			}

			err := repo.Delete(id)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantSentinel)
			} else {
				require.NoError(t, err)
				// Verify it is gone.
				_, err = repo.FindByID(id)
				assert.ErrorIs(t, err, dberrors.ErrNotFound)
			}
		})
	}
}

func TestBaseRepository_Create(t *testing.T) {
	t.Parallel()
	repo := setupBaseRepoTestDB(t)

	policy := models.CleanupPolicy{
		Name:     "new-policy",
		Schedule: "0 * * * *",
		Action:   "clean",
	}
	policy.ID = uuid.New().String()

	err := repo.Create(&policy)
	require.NoError(t, err)

	// Verify persisted.
	found, err := repo.FindByID(policy.ID)
	require.NoError(t, err)
	assert.Equal(t, "new-policy", found.Name)
}

func TestBaseRepository_Save(t *testing.T) {
	t.Parallel()
	repo := setupBaseRepoTestDB(t)

	policy := models.CleanupPolicy{
		Name:     "original",
		Schedule: "0 * * * *",
		Action:   "stop",
	}
	policy.ID = uuid.New().String()
	require.NoError(t, repo.Create(&policy))

	policy.Name = "updated"
	err := repo.Save(&policy)
	require.NoError(t, err)

	found, err := repo.FindByID(policy.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated", found.Name)
}
