//go:build integration

package database

import (
	"context"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDatabaseMySQL_RegistryPasswordWithoutKey checks on MySQL that a keyless
// repository refuses a new password, keeps an unchanged legacy password
// (also a NULL column) and counts the legacy passwords.
func TestDatabaseMySQL_RegistryPasswordWithoutKey(t *testing.T) {
	db, _ := setupClusterContextMySQL(t, 5)
	ctx := context.Background()
	repo := NewGORMClusterRepository(db, "")

	require.NoError(t, repo.Create(ctx, &models.Cluster{ID: "rp-null", Name: "rp-null", APIServerURL: "https://a.example.com"}))
	require.NoError(t, db.Exec("UPDATE clusters SET registry_password = NULL WHERE id = ?", "rp-null").Error)
	c, err := repo.FindByID(ctx, "rp-null")
	require.NoError(t, err)
	c.Region = "west"
	require.NoError(t, repo.Update(ctx, c), "an update of a cluster with a NULL password works")

	legacy := &models.Cluster{ID: "rp-legacy", Name: "rp-legacy", APIServerURL: "https://b.example.com"}
	require.NoError(t, repo.Create(ctx, legacy))
	require.NoError(t, db.Exec("UPDATE clusters SET registry_url = 'r.example.com', registry_username = 'u', registry_password = 'plain' WHERE id = ?", "rp-legacy").Error)
	found, err := repo.FindByID(ctx, "rp-legacy")
	require.NoError(t, err)
	found.Region = "west"
	require.NoError(t, repo.Update(ctx, found))

	found.RegistryPassword = "changed"
	require.Error(t, repo.Update(ctx, found))
	assert.Equal(t, "plain", storedPassword(t, db, "rp-legacy"))

	cleartext, encrypted, err := repo.CountRegistryPasswordsWithoutKey(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, cleartext)
	assert.Zero(t, encrypted)

	refused := &models.Cluster{Name: "rp-new", APIServerURL: "https://c.example.com", RegistryURL: "r.example.com", RegistryUsername: "u", RegistryPassword: "pw"}
	require.Error(t, repo.Create(ctx, refused))
}
