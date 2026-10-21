package database

import (
	"context"
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
	err := repo.Create(context.Background(), def)
	require.NoError(t, err)
	assert.NotEmpty(t, def.ID)
	assert.False(t, def.CreatedAt.IsZero())

	// FindByID
	found, err := repo.FindByID(context.Background(), def.ID)
	require.NoError(t, err)
	assert.Equal(t, "my-stack", found.Name)

	// Update
	found.Description = "updated description"
	err = repo.Update(context.Background(), found)
	require.NoError(t, err)

	updated, err := repo.FindByID(context.Background(), def.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated description", updated.Description)

	// Delete
	err = repo.Delete(context.Background(), def.ID)
	require.NoError(t, err)
	_, err = repo.FindByID(context.Background(), def.ID)
	assert.Error(t, err)
}

func TestGORMStackDefinitionRepository_SetTemplateLink(t *testing.T) {
	t.Parallel()

	repo := setupStackDefinitionRepo(t)
	def := &models.StackDefinition{Name: "imported", OwnerID: "owner-1", DefaultBranch: "main", Description: "keep me"}
	require.NoError(t, repo.Create(context.Background(), def))

	tests := []struct {
		name        string
		id          string
		templateID  string
		version     string
		wantErr     bool
		wantID      string
		wantVersion string
	}{
		{name: "set the link", id: def.ID, templateID: "t1", version: "1.2.0", wantID: "t1", wantVersion: "1.2.0"},
		{name: "remove the link", id: def.ID, templateID: "", version: "", wantID: "", wantVersion: ""},
		{name: "unknown definition", id: "nonexistent", templateID: "t1", version: "1.0.0", wantErr: true},
	}
	// The cases share one row, so they run in order.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := repo.SetTemplateLink(context.Background(), tt.id, tt.templateID, tt.version)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			got, err := repo.FindByID(context.Background(), def.ID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, got.SourceTemplateID)
			assert.Equal(t, tt.wantVersion, got.SourceTemplateVersion)
			// Other columns stay.
			assert.Equal(t, "imported", got.Name)
			assert.Equal(t, "keep me", got.Description)
		})
	}
}

func TestGORMStackDefinitionRepository_FindByID_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupStackDefinitionRepo(t)
	_, err := repo.FindByID(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestGORMStackDefinitionRepository_Delete_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupStackDefinitionRepo(t)
	err := repo.Delete(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestGORMStackDefinitionRepository_List(t *testing.T) {
	t.Parallel()

	repo := setupStackDefinitionRepo(t)
	require.NoError(t, repo.Create(context.Background(), &models.StackDefinition{Name: "s1", OwnerID: "o1", DefaultBranch: "main"}))
	require.NoError(t, repo.Create(context.Background(), &models.StackDefinition{Name: "s2", OwnerID: "o2", DefaultBranch: "main"}))

	defs, err := repo.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, defs, 2)
}

func TestGORMStackDefinitionRepository_ListByOwner(t *testing.T) {
	t.Parallel()

	repo := setupStackDefinitionRepo(t)
	require.NoError(t, repo.Create(context.Background(), &models.StackDefinition{Name: "s1", OwnerID: "owner-a", DefaultBranch: "main"}))
	require.NoError(t, repo.Create(context.Background(), &models.StackDefinition{Name: "s2", OwnerID: "owner-a", DefaultBranch: "main"}))
	require.NoError(t, repo.Create(context.Background(), &models.StackDefinition{Name: "s3", OwnerID: "owner-b", DefaultBranch: "main"}))

	defs, err := repo.ListByOwner(context.Background(), "owner-a")
	require.NoError(t, err)
	assert.Len(t, defs, 2)
}

func TestGORMStackDefinitionRepository_ListByTemplate(t *testing.T) {
	t.Parallel()

	repo := setupStackDefinitionRepo(t)
	require.NoError(t, repo.Create(context.Background(), &models.StackDefinition{Name: "s1", OwnerID: "o1", DefaultBranch: "main", SourceTemplateID: "t1"}))
	require.NoError(t, repo.Create(context.Background(), &models.StackDefinition{Name: "s2", OwnerID: "o1", DefaultBranch: "main", SourceTemplateID: "t2"}))

	defs, err := repo.ListByTemplate(context.Background(), "t1")
	require.NoError(t, err)
	assert.Len(t, defs, 1)
}
