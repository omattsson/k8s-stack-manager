package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupValueOverrideRepo(t *testing.T) *GORMValueOverrideRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMValueOverrideRepository(db)
}

func TestGORMValueOverrideRepository_CRUD(t *testing.T) {
	t.Parallel()

	repo := setupValueOverrideRepo(t)

	// Create
	vo := &models.ValueOverride{
		ID:              "vo-1",
		StackInstanceID: "si-1",
		ChartConfigID:   "cc-1",
		Values:          "key: value",
	}
	err := repo.Create(vo)
	require.NoError(t, err)
	assert.False(t, vo.UpdatedAt.IsZero())

	// FindByID
	found, err := repo.FindByID("vo-1")
	require.NoError(t, err)
	assert.Equal(t, "key: value", found.Values)

	// Update
	found.Values = "key: updated"
	err = repo.Update(found)
	require.NoError(t, err)
	updated, err := repo.FindByID("vo-1")
	require.NoError(t, err)
	assert.Equal(t, "key: updated", updated.Values)

	// Delete
	err = repo.Delete("vo-1")
	require.NoError(t, err)
	_, err = repo.FindByID("vo-1")
	assert.Error(t, err)
}

func TestGORMValueOverrideRepository_FindByID_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupValueOverrideRepo(t)
	_, err := repo.FindByID("nonexistent")
	assert.Error(t, err)
}

func TestGORMValueOverrideRepository_Delete_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupValueOverrideRepo(t)
	err := repo.Delete("nonexistent")
	assert.Error(t, err)
}

func TestGORMValueOverrideRepository_FindByInstanceAndChart(t *testing.T) {
	t.Parallel()

	repo := setupValueOverrideRepo(t)
	require.NoError(t, repo.Create(&models.ValueOverride{ID: "vo-fic", StackInstanceID: "si-x", ChartConfigID: "cc-x", Values: "found: true"}))

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		vo, err := repo.FindByInstanceAndChart("si-x", "cc-x")
		require.NoError(t, err)
		assert.Equal(t, "found: true", vo.Values)
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		_, err := repo.FindByInstanceAndChart("si-x", "cc-nope")
		assert.Error(t, err)
	})
}

func TestGORMValueOverrideRepository_ListByInstance(t *testing.T) {
	t.Parallel()

	repo := setupValueOverrideRepo(t)
	require.NoError(t, repo.Create(&models.ValueOverride{ID: "vo-li1", StackInstanceID: "si-a", ChartConfigID: "cc-1", Values: "a: 1"}))
	require.NoError(t, repo.Create(&models.ValueOverride{ID: "vo-li2", StackInstanceID: "si-a", ChartConfigID: "cc-2", Values: "b: 2"}))
	require.NoError(t, repo.Create(&models.ValueOverride{ID: "vo-li3", StackInstanceID: "si-b", ChartConfigID: "cc-1", Values: "c: 3"}))

	overrides, err := repo.ListByInstance("si-a")
	require.NoError(t, err)
	assert.Len(t, overrides, 2)
}
