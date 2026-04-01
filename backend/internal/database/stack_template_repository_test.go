package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupStackTemplateRepo(t *testing.T) *GORMStackTemplateRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMStackTemplateRepository(db)
}

func TestGORMStackTemplateRepository_CRUD(t *testing.T) {
	t.Parallel()

	repo := setupStackTemplateRepo(t)

	// Create
	tmpl := &models.StackTemplate{
		Name:          "web-app",
		Description:   "Standard web app",
		Category:      "web",
		Version:       "1.0.0",
		OwnerID:       "owner-1",
		DefaultBranch: "main",
	}
	err := repo.Create(tmpl)
	require.NoError(t, err)
	assert.NotEmpty(t, tmpl.ID)

	// FindByID
	found, err := repo.FindByID(tmpl.ID)
	require.NoError(t, err)
	assert.Equal(t, "web-app", found.Name)

	// Update
	found.Description = "Updated"
	err = repo.Update(found)
	require.NoError(t, err)

	updated, err := repo.FindByID(tmpl.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated", updated.Description)

	// Delete
	err = repo.Delete(tmpl.ID)
	require.NoError(t, err)
	_, err = repo.FindByID(tmpl.ID)
	assert.Error(t, err)
}

func TestGORMStackTemplateRepository_FindByID_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupStackTemplateRepo(t)
	_, err := repo.FindByID("nonexistent")
	assert.Error(t, err)
}

func TestGORMStackTemplateRepository_Delete_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupStackTemplateRepo(t)
	err := repo.Delete("nonexistent")
	assert.Error(t, err)
}

func TestGORMStackTemplateRepository_List(t *testing.T) {
	t.Parallel()

	repo := setupStackTemplateRepo(t)
	require.NoError(t, repo.Create(&models.StackTemplate{Name: "t1", OwnerID: "o1", DefaultBranch: "main"}))
	require.NoError(t, repo.Create(&models.StackTemplate{Name: "t2", OwnerID: "o2", DefaultBranch: "main"}))

	tmpls, err := repo.List()
	require.NoError(t, err)
	assert.Len(t, tmpls, 2)
}

func TestGORMStackTemplateRepository_ListPublished(t *testing.T) {
	t.Parallel()

	repo := setupStackTemplateRepo(t)
	require.NoError(t, repo.Create(&models.StackTemplate{Name: "t1", OwnerID: "o1", DefaultBranch: "main", IsPublished: true}))
	require.NoError(t, repo.Create(&models.StackTemplate{Name: "t2", OwnerID: "o1", DefaultBranch: "main", IsPublished: false}))
	require.NoError(t, repo.Create(&models.StackTemplate{Name: "t3", OwnerID: "o1", DefaultBranch: "main", IsPublished: true}))

	tmpls, err := repo.ListPublished()
	require.NoError(t, err)
	assert.Len(t, tmpls, 2)
}

func TestGORMStackTemplateRepository_ListByOwner(t *testing.T) {
	t.Parallel()

	repo := setupStackTemplateRepo(t)
	require.NoError(t, repo.Create(&models.StackTemplate{Name: "t1", OwnerID: "owner-a", DefaultBranch: "main"}))
	require.NoError(t, repo.Create(&models.StackTemplate{Name: "t2", OwnerID: "owner-a", DefaultBranch: "main"}))
	require.NoError(t, repo.Create(&models.StackTemplate{Name: "t3", OwnerID: "owner-b", DefaultBranch: "main"}))

	tmpls, err := repo.ListByOwner("owner-a")
	require.NoError(t, err)
	assert.Len(t, tmpls, 2)
}
