package database

import (
	"testing"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupUserRepo(t *testing.T) *GORMUserRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMUserRepository(db)
}

func TestGORMUserRepository_Create(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		user    models.User
		wantErr bool
		errType error
	}{
		{
			name:    "success",
			user:    models.User{ID: "u1", Username: "alice", Role: "developer"},
			wantErr: false,
		},
		{
			name:    "empty username fails validation",
			user:    models.User{ID: "u2", Username: ""},
			wantErr: true,
			errType: dberrors.ErrValidation,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupUserRepo(t)
			err := repo.Create(&tt.user)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, tt.user.CreatedAt)
				assert.NotEmpty(t, tt.user.UpdatedAt)
			}
		})
	}
}

func TestGORMUserRepository_FindByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		seed    bool
		wantErr bool
	}{
		{name: "found", id: "u-find-1", seed: true, wantErr: false},
		{name: "not found", id: "nonexistent", seed: false, wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupUserRepo(t)
			if tt.seed {
				require.NoError(t, repo.Create(&models.User{ID: tt.id, Username: "user-" + tt.id, Role: "developer"}))
			}
			user, err := repo.FindByID(tt.id)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, user)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.id, user.ID)
			}
		})
	}
}

func TestGORMUserRepository_FindByUsername(t *testing.T) {
	t.Parallel()

	repo := setupUserRepo(t)
	require.NoError(t, repo.Create(&models.User{ID: "u-fname", Username: "findme", Role: "developer"}))

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		user, err := repo.FindByUsername("findme")
		require.NoError(t, err)
		assert.Equal(t, "findme", user.Username)
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		_, err := repo.FindByUsername("nope")
		assert.Error(t, err)
	})
}

func TestGORMUserRepository_FindByExternalID(t *testing.T) {
	t.Parallel()

	repo := setupUserRepo(t)
	extID := "ext-123"
	require.NoError(t, repo.Create(&models.User{
		ID:           "u-ext",
		Username:     "external-user",
		Role:         "developer",
		AuthProvider: "oidc",
		ExternalID:   &extID,
	}))

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		user, err := repo.FindByExternalID("oidc", "ext-123")
		require.NoError(t, err)
		assert.Equal(t, "external-user", user.Username)
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		_, err := repo.FindByExternalID("oidc", "nope")
		assert.Error(t, err)
	})
}

func TestGORMUserRepository_Update(t *testing.T) {
	t.Parallel()

	repo := setupUserRepo(t)
	user := models.User{ID: "u-upd", Username: "before", Role: "developer"}
	require.NoError(t, repo.Create(&user))

	user.DisplayName = "Updated Name"
	err := repo.Update(&user)
	require.NoError(t, err)

	found, err := repo.FindByID("u-upd")
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", found.DisplayName)
}

func TestGORMUserRepository_Delete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		seed    bool
		wantErr bool
	}{
		{name: "success", id: "u-del-1", seed: true, wantErr: false},
		{name: "not found", id: "u-del-none", seed: false, wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupUserRepo(t)
			if tt.seed {
				require.NoError(t, repo.Create(&models.User{ID: tt.id, Username: "del-" + tt.id, Role: "developer"}))
			}
			err := repo.Delete(tt.id)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				_, err := repo.FindByID(tt.id)
				assert.Error(t, err)
			}
		})
	}
}

func TestGORMUserRepository_List(t *testing.T) {
	t.Parallel()

	repo := setupUserRepo(t)
	require.NoError(t, repo.Create(&models.User{ID: "u-l1", Username: "list1", Role: "developer"}))
	require.NoError(t, repo.Create(&models.User{ID: "u-l2", Username: "list2", Role: "admin"}))

	users, err := repo.List()
	require.NoError(t, err)
	assert.Len(t, users, 2)
}
