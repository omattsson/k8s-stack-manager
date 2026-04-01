package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupSharedValuesRepo(t *testing.T) *GORMSharedValuesRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMSharedValuesRepository(db)
}

func TestGORMSharedValuesRepository_CRUD(t *testing.T) {
	t.Parallel()

	repo := setupSharedValuesRepo(t)

	// Create
	sv := &models.SharedValues{
		ClusterID:   "c1",
		Name:        "common-values",
		Description: "Shared across all instances",
		Values:      "env: production",
		Priority:    10,
	}
	err := repo.Create(sv)
	require.NoError(t, err)
	assert.NotEmpty(t, sv.ID)
	assert.False(t, sv.CreatedAt.IsZero())

	// FindByID
	found, err := repo.FindByID(sv.ID)
	require.NoError(t, err)
	assert.Equal(t, "common-values", found.Name)

	// FindByClusterAndID
	found2, err := repo.FindByClusterAndID("c1", sv.ID)
	require.NoError(t, err)
	assert.Equal(t, sv.ID, found2.ID)

	// Update
	found.Values = "env: staging"
	err = repo.Update(found)
	require.NoError(t, err)
	updated, err := repo.FindByID(sv.ID)
	require.NoError(t, err)
	assert.Equal(t, "env: staging", updated.Values)

	// Delete
	err = repo.Delete(sv.ID)
	require.NoError(t, err)
	_, err = repo.FindByID(sv.ID)
	assert.Error(t, err)
}

func TestGORMSharedValuesRepository_FindByID_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupSharedValuesRepo(t)
	_, err := repo.FindByID("nonexistent")
	assert.Error(t, err)
}

func TestGORMSharedValuesRepository_FindByClusterAndID_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupSharedValuesRepo(t)
	_, err := repo.FindByClusterAndID("c1", "nonexistent")
	assert.Error(t, err)
}

func TestGORMSharedValuesRepository_Delete_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupSharedValuesRepo(t)
	err := repo.Delete("nonexistent")
	assert.Error(t, err)
}

func TestGORMSharedValuesRepository_ListByCluster(t *testing.T) {
	t.Parallel()

	repo := setupSharedValuesRepo(t)
	require.NoError(t, repo.Create(&models.SharedValues{ClusterID: "c-list", Name: "sv1", Values: "a: 1", Priority: 20}))
	require.NoError(t, repo.Create(&models.SharedValues{ClusterID: "c-list", Name: "sv2", Values: "b: 2", Priority: 10}))
	require.NoError(t, repo.Create(&models.SharedValues{ClusterID: "c-other", Name: "sv3", Values: "c: 3", Priority: 1}))

	values, err := repo.ListByCluster("c-list")
	require.NoError(t, err)
	assert.Len(t, values, 2)
	// Should be ordered by priority ASC.
	assert.Equal(t, "sv2", values[0].Name)
	assert.Equal(t, "sv1", values[1].Name)
}
