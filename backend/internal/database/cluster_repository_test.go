package database

import (
	"context"
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
	err := repo.Create(context.Background(), cluster)
	require.NoError(t, err)
	assert.NotEmpty(t, cluster.ID)
	assert.Equal(t, models.ClusterUnreachable, cluster.HealthStatus)

	// FindByID
	found, err := repo.FindByID(context.Background(), cluster.ID)
	require.NoError(t, err)
	assert.Equal(t, "dev-cluster", found.Name)

	// Update
	found.Region = "westus"
	err = repo.Update(context.Background(), found)
	require.NoError(t, err)
	updated, err := repo.FindByID(context.Background(), cluster.ID)
	require.NoError(t, err)
	assert.Equal(t, "westus", updated.Region)

	// Delete
	err = repo.Delete(context.Background(), cluster.ID)
	require.NoError(t, err)
	_, err = repo.FindByID(context.Background(), cluster.ID)
	assert.Error(t, err)
}

func TestGORMClusterRepository_FindByID_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	_, err := repo.FindByID(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestGORMClusterRepository_Delete_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	err := repo.Delete(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestGORMClusterRepository_List(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	require.NoError(t, repo.Create(context.Background(), &models.Cluster{Name: "c1", APIServerURL: "https://c1.example.com"}))
	require.NoError(t, repo.Create(context.Background(), &models.Cluster{Name: "c2", APIServerURL: "https://c2.example.com"}))

	clusters, err := repo.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, clusters, 2)
}

func TestGORMClusterRepository_FindDefault(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	require.NoError(t, repo.Create(context.Background(), &models.Cluster{Name: "non-default", APIServerURL: "https://nd.example.com"}))
	require.NoError(t, repo.Create(context.Background(), &models.Cluster{Name: "default-cluster", APIServerURL: "https://dc.example.com", IsDefault: true}))

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		cluster, err := repo.FindDefault(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "default-cluster", cluster.Name)
		assert.True(t, cluster.IsDefault)
	})
}

func TestGORMClusterRepository_FindDefault_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	require.NoError(t, repo.Create(context.Background(), &models.Cluster{Name: "no-default", APIServerURL: "https://x.example.com", IsDefault: false}))

	_, err := repo.FindDefault(context.Background())
	assert.Error(t, err)
}

func TestGORMClusterRepository_SetDefault(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)

	c1 := &models.Cluster{Name: "c1", APIServerURL: "https://c1.example.com", IsDefault: true}
	c2 := &models.Cluster{Name: "c2", APIServerURL: "https://c2.example.com", IsDefault: false}
	require.NoError(t, repo.Create(context.Background(), c1))
	require.NoError(t, repo.Create(context.Background(), c2))

	// Switch default to c2.
	err := repo.SetDefault(context.Background(), c2.ID)
	require.NoError(t, err)

	// c2 should be default now.
	newDefault, err := repo.FindDefault(context.Background())
	require.NoError(t, err)
	assert.Equal(t, c2.ID, newDefault.ID)

	// c1 should no longer be default.
	old, err := repo.FindByID(context.Background(), c1.ID)
	require.NoError(t, err)
	assert.False(t, old.IsDefault)
}

func TestGORMClusterRepository_SetDefault_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	err := repo.SetDefault(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestGORMClusterRepository_CountAllAndByHealthStatus(t *testing.T) {
	t.Parallel()

	repo := setupClusterRepo(t)
	seed := []struct{ name, health string }{
		{"ca-h1", models.ClusterHealthy},
		{"ca-h2", models.ClusterHealthy},
		{"ca-d1", models.ClusterDegraded},
		{"ca-u1", models.ClusterUnreachable},
	}
	for _, s := range seed {
		require.NoError(t, repo.Create(context.Background(), &models.Cluster{
			Name:         s.name,
			APIServerURL: "https://" + s.name + ".example.com",
			HealthStatus: s.health,
		}))
	}

	total, err := repo.CountAll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 4, total)

	healthy, err := repo.CountByHealthStatus(context.Background(), models.ClusterHealthy)
	require.NoError(t, err)
	assert.Equal(t, 2, healthy, "CountByHealthStatus must exclude non-healthy clusters")

	degraded, err := repo.CountByHealthStatus(context.Background(), models.ClusterDegraded)
	require.NoError(t, err)
	assert.Equal(t, 1, degraded)

	none, err := repo.CountByHealthStatus(context.Background(), "nonexistent")
	require.NoError(t, err)
	assert.Equal(t, 0, none)
}
