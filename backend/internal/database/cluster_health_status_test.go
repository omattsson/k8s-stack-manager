package database

import (
	"context"
	"testing"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGORMClusterRepository_UpdateHealthStatus checks that the health write
// keeps an admin edit made after the read of the poller, and that a deleted
// cluster gives not found (#678).
func TestGORMClusterRepository_UpdateHealthStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := setupClusterRepo(t)
	c1 := &models.Cluster{Name: "hs-1", APIServerURL: "https://hs-1.example.com", KubeconfigData: "kc-old",
		RegistryPassword: "pw-old", IsDefault: true, HealthStatus: models.ClusterHealthy}
	c2 := &models.Cluster{Name: "hs-2", APIServerURL: "https://hs-2.example.com"}
	require.NoError(t, repo.Create(ctx, c1))
	require.NoError(t, repo.Create(ctx, c2))

	edited, err := repo.FindByID(ctx, c1.ID)
	require.NoError(t, err)
	edited.KubeconfigData, edited.RegistryPassword, edited.Region = "kc-new", "pw-new", "west"
	require.NoError(t, repo.Update(ctx, edited))
	require.NoError(t, repo.SetDefault(ctx, c2.ID))

	require.NoError(t, repo.UpdateHealthStatus(ctx, c1.ID, models.ClusterUnreachable))

	got, err := repo.FindByID(ctx, c1.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ClusterUnreachable, got.HealthStatus)
	assert.Equal(t, "kc-new", got.KubeconfigData)
	assert.Equal(t, "pw-new", got.RegistryPassword)
	assert.Equal(t, "west", got.Region)
	assert.False(t, got.IsDefault)
	def, err := repo.FindDefault(ctx)
	require.NoError(t, err)
	assert.Equal(t, c2.ID, def.ID)

	err = repo.UpdateHealthStatus(ctx, "missing", models.ClusterHealthy)
	assert.ErrorIs(t, err, dberrors.ErrNotFound)
}

// TestGORMClusterRepository_UpdateKeepsDefaultAndHealth checks that Update
// with a stale row never writes is_default or health_status: SetDefault and
// UpdateHealthStatus own them (#678).
func TestGORMClusterRepository_UpdateKeepsDefaultAndHealth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := setupClusterRepo(t)
	c1 := &models.Cluster{Name: "ud-1", APIServerURL: "https://ud-1.example.com", IsDefault: true,
		HealthStatus: models.ClusterHealthy}
	c2 := &models.Cluster{Name: "ud-2", APIServerURL: "https://ud-2.example.com"}
	require.NoError(t, repo.Create(ctx, c1))
	require.NoError(t, repo.Create(ctx, c2))
	stale, err := repo.FindByID(ctx, c1.ID)
	require.NoError(t, err)

	require.NoError(t, repo.SetDefault(ctx, c2.ID))
	require.NoError(t, repo.UpdateHealthStatus(ctx, c1.ID, models.ClusterUnreachable))
	stale.Description = "edited"
	require.NoError(t, repo.Update(ctx, stale))

	got, err := repo.FindByID(ctx, c1.ID)
	require.NoError(t, err)
	assert.Equal(t, "edited", got.Description)
	assert.False(t, got.IsDefault, "a stale is_default is not written back")
	assert.Equal(t, models.ClusterUnreachable, got.HealthStatus, "a stale health_status is not written back")
	var defaults int64
	require.NoError(t, repo.db.Model(&models.Cluster{}).Where("is_default = ?", true).Count(&defaults).Error)
	assert.Equal(t, int64(1), defaults, "exactly one default cluster")
}
