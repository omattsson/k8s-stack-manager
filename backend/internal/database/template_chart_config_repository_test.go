package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTemplateChartConfigRepo(t *testing.T) *GORMTemplateChartConfigRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMTemplateChartConfigRepository(db)
}

func TestGORMTemplateChartConfigRepository_CRUD(t *testing.T) {
	t.Parallel()

	repo := setupTemplateChartConfigRepo(t)

	// Create
	cfg := &models.TemplateChartConfig{
		ID:              "tcc-1",
		StackTemplateID: "tmpl-1",
		ChartName:       "redis",
		RepositoryURL:   "https://charts.example.com",
		DeployOrder:     1,
		Required:        true,
	}
	err := repo.Create(cfg)
	require.NoError(t, err)
	assert.False(t, cfg.CreatedAt.IsZero())

	// FindByID
	found, err := repo.FindByID("tcc-1")
	require.NoError(t, err)
	assert.Equal(t, "redis", found.ChartName)
	assert.True(t, found.Required)

	// Update
	found.ChartVersion = "7.0.0"
	err = repo.Update(found)
	require.NoError(t, err)
	updated, err := repo.FindByID("tcc-1")
	require.NoError(t, err)
	assert.Equal(t, "7.0.0", updated.ChartVersion)

	// Delete
	err = repo.Delete("tcc-1")
	require.NoError(t, err)
	_, err = repo.FindByID("tcc-1")
	assert.Error(t, err)
}

func TestGORMTemplateChartConfigRepository_FindByID_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupTemplateChartConfigRepo(t)
	_, err := repo.FindByID("nonexistent")
	assert.Error(t, err)
}

func TestGORMTemplateChartConfigRepository_Delete_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupTemplateChartConfigRepo(t)
	err := repo.Delete("nonexistent")
	assert.Error(t, err)
}

func TestGORMTemplateChartConfigRepository_ListByTemplate(t *testing.T) {
	t.Parallel()

	repo := setupTemplateChartConfigRepo(t)
	require.NoError(t, repo.Create(&models.TemplateChartConfig{ID: "tcc-l1", StackTemplateID: "tmpl-a", ChartName: "c1", DeployOrder: 2}))
	require.NoError(t, repo.Create(&models.TemplateChartConfig{ID: "tcc-l2", StackTemplateID: "tmpl-a", ChartName: "c2", DeployOrder: 1}))
	require.NoError(t, repo.Create(&models.TemplateChartConfig{ID: "tcc-l3", StackTemplateID: "tmpl-b", ChartName: "c3", DeployOrder: 1}))

	configs, err := repo.ListByTemplate("tmpl-a")
	require.NoError(t, err)
	assert.Len(t, configs, 2)
	// Ordered by deploy_order ASC.
	assert.Equal(t, "c2", configs[0].ChartName)
	assert.Equal(t, "c1", configs[1].ChartName)
}
