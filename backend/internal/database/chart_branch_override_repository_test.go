package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupChartBranchOverrideRepo(t *testing.T) *GORMChartBranchOverrideRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMChartBranchOverrideRepository(db)
}

func TestGORMChartBranchOverrideRepository_SetAndGet(t *testing.T) {
	t.Parallel()

	repo := setupChartBranchOverrideRepo(t)

	override := &models.ChartBranchOverride{
		StackInstanceID: "si-1",
		ChartConfigID:   "cc-1",
		Branch:          "feature/test",
	}
	err := repo.Set(override)
	require.NoError(t, err)
	assert.NotEmpty(t, override.ID)

	// Get
	found, err := repo.Get("si-1", "cc-1")
	require.NoError(t, err)
	assert.Equal(t, "feature/test", found.Branch)
}

func TestGORMChartBranchOverrideRepository_Set_Upsert(t *testing.T) {
	t.Parallel()

	repo := setupChartBranchOverrideRepo(t)

	// Create initial
	require.NoError(t, repo.Set(&models.ChartBranchOverride{
		StackInstanceID: "si-ups",
		ChartConfigID:   "cc-ups",
		Branch:          "old-branch",
	}))

	// Upsert
	err := repo.Set(&models.ChartBranchOverride{
		StackInstanceID: "si-ups",
		ChartConfigID:   "cc-ups",
		Branch:          "new-branch",
	})
	require.NoError(t, err)

	found, err := repo.Get("si-ups", "cc-ups")
	require.NoError(t, err)
	assert.Equal(t, "new-branch", found.Branch)
}

func TestGORMChartBranchOverrideRepository_Set_ValidationError(t *testing.T) {
	t.Parallel()

	repo := setupChartBranchOverrideRepo(t)

	// Missing required fields should fail validation.
	err := repo.Set(&models.ChartBranchOverride{
		StackInstanceID: "",
		ChartConfigID:   "cc-1",
		Branch:          "main",
	})
	assert.Error(t, err)
}

func TestGORMChartBranchOverrideRepository_Get_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupChartBranchOverrideRepo(t)
	_, err := repo.Get("si-nope", "cc-nope")
	assert.Error(t, err)
}

func TestGORMChartBranchOverrideRepository_List(t *testing.T) {
	t.Parallel()

	repo := setupChartBranchOverrideRepo(t)
	require.NoError(t, repo.Set(&models.ChartBranchOverride{StackInstanceID: "si-list", ChartConfigID: "cc-1", Branch: "b1"}))
	require.NoError(t, repo.Set(&models.ChartBranchOverride{StackInstanceID: "si-list", ChartConfigID: "cc-2", Branch: "b2"}))
	require.NoError(t, repo.Set(&models.ChartBranchOverride{StackInstanceID: "si-other", ChartConfigID: "cc-1", Branch: "b3"}))

	overrides, err := repo.List("si-list")
	require.NoError(t, err)
	assert.Len(t, overrides, 2)
}

func TestGORMChartBranchOverrideRepository_Delete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		seed    bool
		wantErr bool
	}{
		{name: "success", seed: true, wantErr: false},
		{name: "not found", seed: false, wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupChartBranchOverrideRepo(t)
			if tt.seed {
				require.NoError(t, repo.Set(&models.ChartBranchOverride{StackInstanceID: "si-del", ChartConfigID: "cc-del", Branch: "main"}))
			}
			err := repo.Delete("si-del", "cc-del")
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGORMChartBranchOverrideRepository_DeleteByInstance(t *testing.T) {
	t.Parallel()

	repo := setupChartBranchOverrideRepo(t)
	require.NoError(t, repo.Set(&models.ChartBranchOverride{StackInstanceID: "si-delall", ChartConfigID: "cc-1", Branch: "b1"}))
	require.NoError(t, repo.Set(&models.ChartBranchOverride{StackInstanceID: "si-delall", ChartConfigID: "cc-2", Branch: "b2"}))

	err := repo.DeleteByInstance("si-delall")
	require.NoError(t, err)

	overrides, err := repo.List("si-delall")
	require.NoError(t, err)
	assert.Len(t, overrides, 0)
}
