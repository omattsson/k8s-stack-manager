package database

import (
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupAPIKeyRepo(t *testing.T) *GORMAPIKeyRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMAPIKeyRepository(db)
}

func TestGORMAPIKeyRepository_Create(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     models.APIKey
		wantErr bool
	}{
		{
			name:    "success",
			key:     models.APIKey{ID: "k1", UserID: "u1", Name: "my-key", Prefix: "abc123", KeyHash: "hash1"},
			wantErr: false,
		},
		{
			name:    "missing user ID fails",
			key:     models.APIKey{ID: "k2", Name: "my-key"},
			wantErr: true,
		},
		{
			name:    "missing name fails",
			key:     models.APIKey{ID: "k3", UserID: "u1"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupAPIKeyRepo(t)
			err := repo.Create(&tt.key)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.False(t, tt.key.CreatedAt.IsZero())
			}
		})
	}
}

func TestGORMAPIKeyRepository_FindByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		userID  string
		keyID   string
		seed    bool
		wantErr bool
	}{
		{name: "found", userID: "u1", keyID: "k-find", seed: true, wantErr: false},
		{name: "not found", userID: "u1", keyID: "nope", seed: false, wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := setupAPIKeyRepo(t)
			if tt.seed {
				require.NoError(t, repo.Create(&models.APIKey{ID: tt.keyID, UserID: tt.userID, Name: "test", Prefix: "pfx", KeyHash: "h"}))
			}
			key, err := repo.FindByID(tt.userID, tt.keyID)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, key)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.keyID, key.ID)
			}
		})
	}
}

func TestGORMAPIKeyRepository_FindByPrefix(t *testing.T) {
	t.Parallel()

	repo := setupAPIKeyRepo(t)
	require.NoError(t, repo.Create(&models.APIKey{ID: "k-pfx1", UserID: "u1", Name: "k1", Prefix: "unique-prefix", KeyHash: "h1"}))

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		keys, err := repo.FindByPrefix("unique-prefix")
		require.NoError(t, err)
		assert.Len(t, keys, 1)
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		_, err := repo.FindByPrefix("nonexistent")
		assert.Error(t, err)
	})
}

func TestGORMAPIKeyRepository_ListByUser(t *testing.T) {
	t.Parallel()

	repo := setupAPIKeyRepo(t)
	require.NoError(t, repo.Create(&models.APIKey{ID: "k-lu1", UserID: "u-list", Name: "k1", Prefix: "p1", KeyHash: "h1"}))
	require.NoError(t, repo.Create(&models.APIKey{ID: "k-lu2", UserID: "u-list", Name: "k2", Prefix: "p2", KeyHash: "h2"}))
	require.NoError(t, repo.Create(&models.APIKey{ID: "k-lu3", UserID: "u-other", Name: "k3", Prefix: "p3", KeyHash: "h3"}))

	keys, err := repo.ListByUser("u-list")
	require.NoError(t, err)
	assert.Len(t, keys, 2)
}

func TestGORMAPIKeyRepository_UpdateLastUsed(t *testing.T) {
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
			repo := setupAPIKeyRepo(t)
			if tt.seed {
				require.NoError(t, repo.Create(&models.APIKey{ID: "k-ulu", UserID: "u-ulu", Name: "test", Prefix: "p", KeyHash: "h"}))
			}
			err := repo.UpdateLastUsed("u-ulu", "k-ulu", time.Now())
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGORMAPIKeyRepository_Delete(t *testing.T) {
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
			repo := setupAPIKeyRepo(t)
			if tt.seed {
				require.NoError(t, repo.Create(&models.APIKey{ID: "k-del", UserID: "u-del", Name: "test", Prefix: "p", KeyHash: "h"}))
			}
			err := repo.Delete("u-del", "k-del")
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
