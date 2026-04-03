package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupChartConfigRepo(t *testing.T) *GORMChartConfigRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMChartConfigRepository(db)
}

func TestGORMChartConfigRepository_CRUD(t *testing.T) {
	t.Parallel()

	repo := setupChartConfigRepo(t)

	// Create
	cfg := &models.ChartConfig{
		ID:                "cc-1",
		StackDefinitionID: "def-1",
		ChartName:         "nginx",
		RepositoryURL:     "https://charts.example.com",
		DeployOrder:       1,
	}
	err := repo.Create(cfg)
	require.NoError(t, err)
	assert.False(t, cfg.CreatedAt.IsZero())

	// FindByID
	found, err := repo.FindByID("cc-1")
	require.NoError(t, err)
	assert.Equal(t, "nginx", found.ChartName)

	// Update
	found.ChartVersion = "1.2.3"
	err = repo.Update(found)
	require.NoError(t, err)
	updated, err := repo.FindByID("cc-1")
	require.NoError(t, err)
	assert.Equal(t, "1.2.3", updated.ChartVersion)

	// Delete
	err = repo.Delete("cc-1")
	require.NoError(t, err)
	_, err = repo.FindByID("cc-1")
	assert.Error(t, err)
}

func TestGORMChartConfigRepository_FindByID_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupChartConfigRepo(t)
	_, err := repo.FindByID("nonexistent")
	assert.Error(t, err)
}

func TestGORMChartConfigRepository_Delete_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupChartConfigRepo(t)
	err := repo.Delete("nonexistent")
	assert.Error(t, err)
}

func TestGORMChartConfigRepository_ListByDefinition(t *testing.T) {
	t.Parallel()

	repo := setupChartConfigRepo(t)
	require.NoError(t, repo.Create(&models.ChartConfig{ID: "cc-l1", StackDefinitionID: "def-a", ChartName: "chart1", DeployOrder: 2}))
	require.NoError(t, repo.Create(&models.ChartConfig{ID: "cc-l2", StackDefinitionID: "def-a", ChartName: "chart2", DeployOrder: 1}))
	require.NoError(t, repo.Create(&models.ChartConfig{ID: "cc-l3", StackDefinitionID: "def-b", ChartName: "chart3", DeployOrder: 1}))

	configs, err := repo.ListByDefinition("def-a")
	require.NoError(t, err)
	assert.Len(t, configs, 2)
	// Should be ordered by deploy_order ASC.
	assert.Equal(t, "chart2", configs[0].ChartName)
	assert.Equal(t, "chart1", configs[1].ChartName)
}
