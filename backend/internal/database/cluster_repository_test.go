package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupClusterRepo(t *testing.T) *GORMClusterRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMClusterRepository(db, "test-encryption-key-for-unit-tests")
}

func TestGORMClusterRepository_CRUD(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)

	// Create
	cluster := &models.Cluster{
		Name:         "dev-cluster",
		APIServerURL: "https://k8s.example.com",
		Region:       "eastus",
	}
	err := repo.Create(cluster)
	require.NoError(t, err)
	assert.NotEmpty(t, cluster.ID)
	assert.Equal(t, models.ClusterUnreachable, cluster.HealthStatus)

	// FindByID
	found, err := repo.FindByID(cluster.ID)
	require.NoError(t, err)
	assert.Equal(t, "dev-cluster", found.Name)

	// Update
	found.Region = "westus"
	err = repo.Update(found)
	require.NoError(t, err)
	updated, err := repo.FindByID(cluster.ID)
	require.NoError(t, err)
	assert.Equal(t, "westus", updated.Region)

	// Delete
	err = repo.Delete(cluster.ID)
	require.NoError(t, err)
	_, err = repo.FindByID(cluster.ID)
	assert.Error(t, err)
}

func TestGORMClusterRepository_FindByID_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	_, err := repo.FindByID("nonexistent")
	assert.Error(t, err)
}

func TestGORMClusterRepository_Delete_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	err := repo.Delete("nonexistent")
	assert.Error(t, err)
}

func TestGORMClusterRepository_List(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	require.NoError(t, repo.Create(&models.Cluster{Name: "c1", APIServerURL: "https://c1.example.com"}))
	require.NoError(t, repo.Create(&models.Cluster{Name: "c2", APIServerURL: "https://c2.example.com"}))

	clusters, err := repo.List()
	require.NoError(t, err)
	assert.Len(t, clusters, 2)
}

func TestGORMClusterRepository_FindDefault(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	require.NoError(t, repo.Create(&models.Cluster{Name: "non-default", APIServerURL: "https://nd.example.com"}))
	require.NoError(t, repo.Create(&models.Cluster{Name: "default-cluster", APIServerURL: "https://dc.example.com", IsDefault: true}))

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		cluster, err := repo.FindDefault()
		require.NoError(t, err)
		assert.Equal(t, "default-cluster", cluster.Name)
		assert.True(t, cluster.IsDefault)
	})
}

func TestGORMClusterRepository_FindDefault_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	require.NoError(t, repo.Create(&models.Cluster{Name: "no-default", APIServerURL: "https://x.example.com", IsDefault: false}))

	_, err := repo.FindDefault()
	assert.Error(t, err)
}

func TestGORMClusterRepository_SetDefault(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)

	c1 := &models.Cluster{Name: "c1", APIServerURL: "https://c1.example.com", IsDefault: true}
	c2 := &models.Cluster{Name: "c2", APIServerURL: "https://c2.example.com", IsDefault: false}
	require.NoError(t, repo.Create(c1))
	require.NoError(t, repo.Create(c2))

	// Switch default to c2.
	err := repo.SetDefault(c2.ID)
	require.NoError(t, err)

	// c2 should be default now.
	newDefault, err := repo.FindDefault()
	require.NoError(t, err)
	assert.Equal(t, c2.ID, newDefault.ID)

	// c1 should no longer be default.
	old, err := repo.FindByID(c1.ID)
	require.NoError(t, err)
	assert.False(t, old.IsDefault)
}

func TestGORMClusterRepository_SetDefault_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	err := repo.SetDefault("nonexistent")
	assert.Error(t, err)
}
