package database

import (
	"testing"

	"backend/internal/models"
	"backend/pkg/dberrors"

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

func TestGORMStackTemplateRepository_FindByIDForUpdate(t *testing.T) {
	t.Parallel()

	repo := setupStackTemplateRepo(t)
	tmpl := &models.StackTemplate{Name: "locked", Version: "1.0.0", OwnerID: "owner-1"}
	require.NoError(t, repo.Create(tmpl))

	tests := []struct {
		name         string
		id           string
		wantNotFound bool
	}{
		{"found", tmpl.ID, false},
		{"not found", "missing", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := repo.FindByIDForUpdate(tt.id)
			if tt.wantNotFound {
				require.Error(t, err)
				assert.ErrorIs(t, err, dberrors.ErrNotFound)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "locked", got.Name)
			assert.Equal(t, "1.0.0", got.Version)
		})
	}
}

func TestGORMStackTemplateRepository_ListPagedNameFilter(t *testing.T) {
	t.Parallel()

	repo := setupStackTemplateRepo(t)
	for _, tmpl := range []*models.StackTemplate{
		{Name: "web", OwnerID: "o", IsPublished: true},
		{Name: "web", OwnerID: "o", IsPublished: false},
		{Name: "web-2", OwnerID: "o", IsPublished: true},
	} {
		require.NoError(t, repo.Create(tmpl))
	}

	tests := []struct {
		name      string
		published bool
		filter    string
		want      int64
	}{
		{"all, exact name", false, "web", 2},
		{"published, exact name", true, "web", 1},
		{"no partial match", false, "we", 0},
		{"no filter", false, "", 3},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			list := repo.ListPaged
			if tt.published {
				list = repo.ListPublishedPaged
			}
			got, total, err := list(10, 0, tt.filter)
			require.NoError(t, err)
			assert.Equal(t, tt.want, total)
			assert.Len(t, got, int(tt.want))
		})
	}
}
