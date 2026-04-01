package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupStackDefinitionRepo(t *testing.T) *GORMStackDefinitionRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMStackDefinitionRepository(db)
}

func TestGORMStackDefinitionRepository_CRUD(t *testing.T) {
	t.Parallel()

	repo := setupStackDefinitionRepo(t)

	// Create
	def := &models.StackDefinition{
		Name:          "my-stack",
		OwnerID:       "owner-1",
		DefaultBranch: "main",
	}
	err := repo.Create(def)
	require.NoError(t, err)
	assert.NotEmpty(t, def.ID)
	assert.False(t, def.CreatedAt.IsZero())

	// FindByID
	found, err := repo.FindByID(def.ID)
	require.NoError(t, err)
	assert.Equal(t, "my-stack", found.Name)

	// Update
	found.Description = "updated description"
	err = repo.Update(found)
	require.NoError(t, err)

	updated, err := repo.FindByID(def.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated description", updated.Description)

	// Delete
	err = repo.Delete(def.ID)
	require.NoError(t, err)
	_, err = repo.FindByID(def.ID)
	assert.Error(t, err)
}

func TestGORMStackDefinitionRepository_FindByID_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupStackDefinitionRepo(t)
	_, err := repo.FindByID("nonexistent")
	assert.Error(t, err)
}

func TestGORMStackDefinitionRepository_Delete_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupStackDefinitionRepo(t)
	err := repo.Delete("nonexistent")
	assert.Error(t, err)
}

func TestGORMStackDefinitionRepository_List(t *testing.T) {
	t.Parallel()

	repo := setupStackDefinitionRepo(t)
	require.NoError(t, repo.Create(&models.StackDefinition{Name: "s1", OwnerID: "o1", DefaultBranch: "main"}))
	require.NoError(t, repo.Create(&models.StackDefinition{Name: "s2", OwnerID: "o2", DefaultBranch: "main"}))

	defs, err := repo.List()
	require.NoError(t, err)
	assert.Len(t, defs, 2)
}

func TestGORMStackDefinitionRepository_ListByOwner(t *testing.T) {
	t.Parallel()

	repo := setupStackDefinitionRepo(t)
	require.NoError(t, repo.Create(&models.StackDefinition{Name: "s1", OwnerID: "owner-a", DefaultBranch: "main"}))
	require.NoError(t, repo.Create(&models.StackDefinition{Name: "s2", OwnerID: "owner-a", DefaultBranch: "main"}))
	require.NoError(t, repo.Create(&models.StackDefinition{Name: "s3", OwnerID: "owner-b", DefaultBranch: "main"}))

	defs, err := repo.ListByOwner("owner-a")
	require.NoError(t, err)
	assert.Len(t, defs, 2)
}

func TestGORMStackDefinitionRepository_ListByTemplate(t *testing.T) {
	t.Parallel()

	repo := setupStackDefinitionRepo(t)
	require.NoError(t, repo.Create(&models.StackDefinition{Name: "s1", OwnerID: "o1", DefaultBranch: "main", SourceTemplateID: "t1"}))
	require.NoError(t, repo.Create(&models.StackDefinition{Name: "s2", OwnerID: "o1", DefaultBranch: "main", SourceTemplateID: "t2"}))

	defs, err := repo.ListByTemplate("t1")
	require.NoError(t, err)
	assert.Len(t, defs, 1)
}
