//go:build integration

package database

import (
	"context"
	"testing"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDatabaseMySQL_ClusterHealthStatusKeepsAdminEdit checks on MySQL that
// UpdateHealthStatus writes only the status, and that Update with a stale
// row writes neither is_default nor health_status (#678).
func TestDatabaseMySQL_ClusterHealthStatusKeepsAdminEdit(t *testing.T) {
	db, repo := setupClusterContextMySQL(t, 2)
	ctx := context.Background()
	seedMySQLCluster(t, repo, "cl-hs-a", true)
	seedMySQLCluster(t, repo, "cl-hs-b", false)

	stale, err := repo.FindByID(ctx, "cl-hs-a")
	require.NoError(t, err)
	edited := *stale
	edited.RegistryPassword, edited.Region = "rotated", "west"
	require.NoError(t, repo.Update(ctx, &edited))
	require.NoError(t, repo.SetDefault(ctx, "cl-hs-b"))

	require.NoError(t, repo.UpdateHealthStatus(ctx, "cl-hs-a", models.ClusterHealthy))

	got, err := repo.FindByID(ctx, "cl-hs-a")
	require.NoError(t, err)
	assert.Equal(t, models.ClusterHealthy, got.HealthStatus)
	assert.Equal(t, "rotated", got.RegistryPassword)
	assert.Equal(t, "west", got.Region)
	assert.Equal(t, []string{"cl-hs-b"}, defaultClusterIDs(t, db))

	// A stale full row (is_default true, health unreachable) changes neither.
	stale.Description = "stale edit"
	require.NoError(t, repo.Update(ctx, stale))
	got, err = repo.FindByID(ctx, "cl-hs-a")
	require.NoError(t, err)
	assert.Equal(t, "stale edit", got.Description)
	assert.Equal(t, models.ClusterHealthy, got.HealthStatus)
	assert.Equal(t, []string{"cl-hs-b"}, defaultClusterIDs(t, db))

	require.ErrorIs(t, repo.UpdateHealthStatus(ctx, "cl-hs-missing", models.ClusterHealthy), dberrors.ErrNotFound)
}
