package cluster

import (
	"context"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// editAfterListRepo applies an admin edit to the stored cluster right after
// List returned, so the poller holds a stale row when it writes.
type editAfterListRepo struct {
	*mockClusterRepo
	edit func(cl *models.Cluster)
}

func (r *editAfterListRepo) List(ctx context.Context) ([]models.Cluster, error) {
	list, err := r.mockClusterRepo.List(ctx)
	if err == nil {
		r.mu.Lock()
		for _, cl := range r.clusters {
			r.edit(cl)
		}
		r.mu.Unlock()
	}
	return list, err
}

// TestHealthPoller_KeepsAdminEditDuringCycle checks that a health status
// change writes only the status: an admin edit between the poller read and
// the poller write stays (#678).
func TestHealthPoller_KeepsAdminEditDuringCycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		edit  func(cl *models.Cluster)
		check func(t *testing.T, cl *models.Cluster)
		name  string
	}{
		{
			name: "rotated registry password",
			edit: func(cl *models.Cluster) { cl.RegistryPassword = "rotated" },
			check: func(t *testing.T, cl *models.Cluster) {
				assert.Equal(t, "rotated", cl.RegistryPassword)
			},
		},
		{
			name: "new kubeconfig",
			edit: func(cl *models.Cluster) { cl.KubeconfigPath, cl.KubeconfigData = "", "new-kubeconfig" },
			check: func(t *testing.T, cl *models.Cluster) {
				assert.Equal(t, "new-kubeconfig", cl.KubeconfigData)
				assert.Empty(t, cl.KubeconfigPath)
			},
		},
		{
			name: "default moved to another cluster",
			edit: func(cl *models.Cluster) { cl.IsDefault = false },
			check: func(t *testing.T, cl *models.Cluster) {
				assert.False(t, cl.IsDefault, "the stale is_default is not written back")
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := newMockClusterRepo()
			base.clusters["c1"] = &models.Cluster{
				ID: "c1", Name: "Cluster 1", KubeconfigPath: "/fake/path",
				RegistryPassword: "old", IsDefault: true, HealthStatus: models.ClusterHealthy,
			}
			repo := &editAfterListRepo{mockClusterRepo: base, edit: tt.edit}
			hub := &mockBroadcastSender{}
			poller := NewHealthPoller(HealthPollerConfig{
				ClusterRepo: repo,
				Registry:    healthPollerFailingRegistry(repo),
				Hub:         hub,
			})

			poller.poll(context.Background())

			base.mu.Lock()
			got := *base.clusters["c1"]
			base.mu.Unlock()
			require.Equal(t, models.ClusterUnreachable, got.HealthStatus, "the status change is written")
			tt.check(t, &got)
			assert.Equal(t, 1, hub.messageCount(), "the change is broadcast")
		})
	}
}

// TestHealthPoller_ClusterDeletedDuringCycle checks that a cluster deleted
// between the read and the write is skipped without a broadcast.
func TestHealthPoller_ClusterDeletedDuringCycle(t *testing.T) {
	t.Parallel()
	base := newMockClusterRepo()
	base.clusters["c1"] = &models.Cluster{
		ID: "c1", Name: "Cluster 1", KubeconfigPath: "/fake/path", HealthStatus: models.ClusterHealthy,
	}
	repo := &deleteAfterListRepo{mockClusterRepo: base}
	hub := &mockBroadcastSender{}
	poller := NewHealthPoller(HealthPollerConfig{
		ClusterRepo: repo,
		Registry:    healthPollerFailingRegistry(repo),
		Hub:         hub,
	})

	poller.poll(context.Background())

	base.mu.Lock()
	_, exists := base.clusters["c1"]
	base.mu.Unlock()
	assert.False(t, exists, "the poller does not create the deleted cluster again")
	assert.Zero(t, hub.messageCount())
}

// deleteAfterListRepo deletes every cluster right after List returned.
type deleteAfterListRepo struct {
	*mockClusterRepo
}

func (r *deleteAfterListRepo) List(ctx context.Context) ([]models.Cluster, error) {
	list, err := r.mockClusterRepo.List(ctx)
	if err == nil {
		r.mu.Lock()
		for id := range r.clusters {
			delete(r.clusters, id)
		}
		r.mu.Unlock()
	}
	return list, err
}
