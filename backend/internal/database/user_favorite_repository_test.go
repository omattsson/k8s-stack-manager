package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupUserFavoriteRepo(t *testing.T) *GORMUserFavoriteRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMUserFavoriteRepository(db)
}

func TestGORMUserFavoriteRepository_AddAndList(t *testing.T) {
	t.Parallel()

	repo := setupUserFavoriteRepo(t)

	fav := &models.UserFavorite{
		UserID:     "u1",
		EntityType: "template",
		EntityID:   "tmpl-1",
	}
	err := repo.Add(fav)
	require.NoError(t, err)
	assert.NotEmpty(t, fav.ID)

	favs, err := repo.List("u1")
	require.NoError(t, err)
	assert.Len(t, favs, 1)
	assert.Equal(t, "template", favs[0].EntityType)
}

func TestGORMUserFavoriteRepository_Add_ValidationError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		fav  models.UserFavorite
	}{
		{name: "missing user_id", fav: models.UserFavorite{EntityType: "template", EntityID: "t1"}},
		{name: "missing entity_type", fav: models.UserFavorite{UserID: "u1", EntityID: "t1"}},
		{name: "invalid entity_type", fav: models.UserFavorite{UserID: "u1", EntityType: "invalid", EntityID: "t1"}},
		{name: "missing entity_id", fav: models.UserFavorite{UserID: "u1", EntityType: "template"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupUserFavoriteRepo(t)
			err := repo.Add(&tt.fav)
			assert.Error(t, err)
		})
	}
}

func TestGORMUserFavoriteRepository_Add_DuplicateIsIdempotent(t *testing.T) {
	t.Parallel()

	repo := setupUserFavoriteRepo(t)
	fav := &models.UserFavorite{UserID: "u1", EntityType: "instance", EntityID: "i1"}
	require.NoError(t, repo.Add(fav))

	// Adding the same combination again should not error (idempotent).
	// Note: SQLite uses "UNIQUE constraint failed" which the repo handles.
	fav2 := &models.UserFavorite{UserID: "u1", EntityType: "instance", EntityID: "i1"}
	err := repo.Add(fav2)
	assert.NoError(t, err)
}

func TestGORMUserFavoriteRepository_Remove(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		seed    bool
		wantErr bool
	}{
		{name: "success", seed: true, wantErr: false},
		{name: "not found", seed: false, wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupUserFavoriteRepo(t)
			if tt.seed {
				require.NoError(t, repo.Add(&models.UserFavorite{UserID: "u-rem", EntityType: "definition", EntityID: "d1"}))
			}
			err := repo.Remove("u-rem", "definition", "d1")
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGORMUserFavoriteRepository_IsFavorite(t *testing.T) {
	t.Parallel()

	repo := setupUserFavoriteRepo(t)
	require.NoError(t, repo.Add(&models.UserFavorite{UserID: "u-fav", EntityType: "template", EntityID: "t1"}))

	tests := []struct {
		name       string
		entityType string
		entityID   string
		want       bool
	}{
		{name: "is favorite", entityType: "template", entityID: "t1", want: true},
		{name: "not favorite", entityType: "template", entityID: "t2", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			isFav, err := repo.IsFavorite("u-fav", tt.entityType, tt.entityID)
			require.NoError(t, err)
			assert.Equal(t, tt.want, isFav)
		})
	}
}

func TestGORMUserFavoriteRepository_List_Empty(t *testing.T) {
	t.Parallel()

	repo := setupUserFavoriteRepo(t)
	favs, err := repo.List("nonexistent")
	require.NoError(t, err)
	assert.Len(t, favs, 0)
}
