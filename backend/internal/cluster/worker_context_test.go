package cluster

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
)

type workerCtxKey struct{}

// findByClusterRecorder records the marker of the context of FindByCluster
// and ends the cycle with an error.
type findByClusterRecorder struct {
	*mockInstanceRepo
	mu     sync.Mutex
	marker any
	called bool
}

func (r *findByClusterRecorder) FindByCluster(ctx context.Context, _ string) ([]models.StackInstance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.marker, r.called = ctx.Value(workerCtxKey{}), true
	return nil, errors.New("stop the cycle")
}

func (r *findByClusterRecorder) seen() (any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.marker, r.called
}

// TestMonitors_UseWorkerContext checks that the quota monitor, the secret
// monitor and the secret refresher read the instances with the context of
// the worker (the leadership term), not a background context (#656).
func TestMonitors_UseWorkerContext(t *testing.T) {
	t.Parallel()

	newClusters := func() *mockClusterRepo {
		repo := newMockClusterRepo()
		repo.clusters["cluster-1"] = &models.Cluster{
			ID: "cluster-1", Name: "dev", UseInCluster: true,
			RegistryURL: "registry.example.com", RegistryUsername: "user", RegistryPassword: "pass",
		}
		return repo
	}
	tests := []struct {
		name string
		run  func(instances models.StackInstanceRepository) func(context.Context)
	}{
		{name: "quota monitor", run: func(instances models.StackInstanceRepository) func(context.Context) {
			return NewQuotaMonitor(QuotaMonitorConfig{
				ClusterRepo:  newClusters(),
				InstanceRepo: instances,
				QuotaRepo:    &mockQuotaRepo{configs: map[string]*models.ResourceQuotaConfig{"cluster-1": {ClusterID: "cluster-1", CPULimit: "4"}}},
				Notifier:     &mockQuotaNotifier{},
				Interval:     time.Hour,
			}).Run
		}},
		{name: "secret monitor", run: func(instances models.StackInstanceRepository) func(context.Context) {
			return NewSecretMonitor(SecretMonitorConfig{
				ClusterRepo:  newClusters(),
				InstanceRepo: instances,
				Notifier:     &mockSecretNotifier{},
				Interval:     time.Hour,
			}).Run
		}},
		{name: "secret refresher", run: func(instances models.StackInstanceRepository) func(context.Context) {
			return NewSecretRefresher(SecretRefresherConfig{
				ClusterRepo:  newClusters(),
				InstanceRepo: instances,
				Registry:     NewRegistryForTest("cluster-1", k8s.NewClientFromInterface(fake.NewSimpleClientset()), nil),
				Interval:     time.Hour,
			}).Run
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &findByClusterRecorder{mockInstanceRepo: &mockInstanceRepo{}}
			run := tt.run(repo)
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), workerCtxKey{}, "worker"))
			defer cancel()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				run(ctx)
			}()

			require.Eventually(t, func() bool { _, ok := repo.seen(); return ok }, 5*time.Second, 5*time.Millisecond)
			cancel()
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("the worker did not stop")
			}
			marker, _ := repo.seen()
			assert.Equal(t, "worker", marker, "FindByCluster runs with the worker context")
		})
	}
}

// quotaCtxRecorder records the marker of the context of GetByClusterID.
type quotaCtxRecorder struct {
	*mockQuotaRepo
	mu     sync.Mutex
	marker any
	called bool
}

func (r *quotaCtxRecorder) GetByClusterID(ctx context.Context, clusterID string) (*models.ResourceQuotaConfig, error) {
	r.mu.Lock()
	r.marker, r.called = ctx.Value(workerCtxKey{}), true
	r.mu.Unlock()
	return r.mockQuotaRepo.GetByClusterID(ctx, clusterID)
}

func (r *quotaCtxRecorder) seen() (any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.marker, r.called
}

// TestQuotaMonitor_QuotaReadUsesWorkerContext checks that the quota monitor
// reads the cluster quota with the context of the worker (#656).
func TestQuotaMonitor_QuotaReadUsesWorkerContext(t *testing.T) {
	t.Parallel()
	clusters := newMockClusterRepo()
	clusters.clusters["cluster-1"] = &models.Cluster{ID: "cluster-1", Name: "dev", UseInCluster: true}
	quotas := &quotaCtxRecorder{mockQuotaRepo: &mockQuotaRepo{configs: map[string]*models.ResourceQuotaConfig{}}}
	monitor := NewQuotaMonitor(QuotaMonitorConfig{
		ClusterRepo:  clusters,
		InstanceRepo: &mockInstanceRepo{},
		QuotaRepo:    quotas,
		Notifier:     &mockQuotaNotifier{},
		Interval:     time.Hour,
	})
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), workerCtxKey{}, "worker"))
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		monitor.Run(ctx)
	}()

	require.Eventually(t, func() bool { _, ok := quotas.seen(); return ok }, 5*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not stop")
	}
	marker, _ := quotas.seen()
	assert.Equal(t, "worker", marker, "GetByClusterID runs with the worker context")
}
