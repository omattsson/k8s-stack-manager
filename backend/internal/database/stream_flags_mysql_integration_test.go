//go:build integration

package database

import (
	"context"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDatabaseMySQL_ClusterStreamFlagsAndInstanceFindByIDs checks on MySQL the
// two projected reads of the stream access re-check: StreamFlagsByIDs reads
// the switches of the clusters in use (the default cluster under the empty
// key) even when another cluster has a damaged secret column, and
// FindByIDs returns the projected instances in one query.
func TestDatabaseMySQL_ClusterStreamFlagsAndInstanceFindByIDs(t *testing.T) {
	ctx := context.Background()
	_, clusters := setupClusterContextMySQL(t, 5)
	db := clusters.db
	instances := NewGORMStackInstanceRepository(db)

	on := &models.Cluster{ID: "sf-on", Name: "sf-on", APIServerURL: "https://on.example.com", KubeconfigPath: "/kc/on", AllowPortForward: true}
	off := &models.Cluster{ID: "sf-off", Name: "sf-off", APIServerURL: "https://off.example.com", KubeconfigPath: "/kc/off"}
	def := &models.Cluster{ID: "sf-def", Name: "sf-def", APIServerURL: "https://def.example.com", KubeconfigPath: "/kc/def", AllowPortForward: true}
	broken := &models.Cluster{ID: "sf-broken", Name: "sf-broken", APIServerURL: "https://broken.example.com", KubeconfigPath: "/kc/broken", AllowPortForward: true}
	for _, c := range []*models.Cluster{on, off, def, broken} {
		require.NoError(t, clusters.Create(ctx, c))
	}
	require.NoError(t, clusters.SetDefault(ctx, def.ID))
	require.NoError(t, db.Model(&models.Cluster{}).Where("id = ?", broken.ID).
		Update("kubeconfig_data", "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQQ==").Error)
	_, listErr := clusters.List(ctx)
	require.Error(t, listErr, "the full read fails on the damaged row")

	got, err := clusters.StreamFlagsByIDs(ctx, []string{on.ID, off.ID, broken.ID, "missing"}, true)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{on.ID: true, off.ID: false, broken.ID: true, models.DefaultClusterKey: true}, got)

	a := &models.StackInstance{Name: "sf-a", StackDefinitionID: "d1", Namespace: "ns-a", OwnerID: "alice", ClusterID: on.ID, Branch: "main", Status: models.StackStatusRunning}
	b := &models.StackInstance{Name: "sf-b", StackDefinitionID: "d1", Namespace: "ns-b", OwnerID: "bob", Branch: "dev", Status: models.StackStatusStopped}
	require.NoError(t, instances.Create(ctx, a))
	require.NoError(t, instances.Create(ctx, b))
	found, err := instances.FindByIDs(ctx, []string{a.ID, b.ID, "missing"})
	require.NoError(t, err)
	require.Len(t, found, 2)
	assert.Equal(t, "alice", found[a.ID].OwnerID)
	assert.Equal(t, on.ID, found[a.ID].ClusterID)
	assert.Equal(t, models.StackStatusStopped, found[b.ID].Status)
	assert.Equal(t, "ns-b", found[b.ID].Namespace)
	assert.Empty(t, found[a.ID].Branch, "columns outside the projection stay empty")
}

// TestDatabaseMySQL_InstanceNamespaceShared checks on MySQL that
// NamespaceShared finds another row with the same namespace and ignores the
// row of the caller (the port-forward namespace owner check).
func TestDatabaseMySQL_InstanceNamespaceShared(t *testing.T) {
	ctx := context.Background()
	_, clusters := setupClusterContextMySQL(t, 5)
	instances := NewGORMStackInstanceRepository(clusters.db)

	victim := &models.StackInstance{Name: "ns-victim", StackDefinitionID: "d1", Namespace: "stack-shop-bob", OwnerID: "bob", Branch: "main", Status: models.StackStatusRunning}
	alone := &models.StackInstance{Name: "ns-alone", StackDefinitionID: "d1", Namespace: "stack-alone-alice", OwnerID: "alice", Branch: "main", Status: models.StackStatusRunning}
	require.NoError(t, instances.Create(ctx, victim))
	require.NoError(t, instances.Create(ctx, alone))

	shared, err := instances.NamespaceShared(ctx, alone.Namespace, alone.ID)
	require.NoError(t, err)
	assert.False(t, shared, "the own row does not count")

	// A row from before the namespace became immutable.
	swapped := &models.StackInstance{Name: "ns-swapped", StackDefinitionID: "d1", Namespace: victim.Namespace, OwnerID: "mallory", Branch: "main", Status: models.StackStatusRunning}
	require.NoError(t, instances.Create(ctx, swapped))
	for _, id := range []string{swapped.ID, victim.ID} {
		shared, err = instances.NamespaceShared(ctx, victim.Namespace, id)
		require.NoError(t, err)
		assert.True(t, shared, "both rows of a shared namespace are refused")
	}
}
