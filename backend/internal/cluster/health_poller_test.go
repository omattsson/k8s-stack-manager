package cluster

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
)

// --- Test helpers ---

type mockBroadcastSender struct {
	mu       sync.Mutex
	messages [][]byte
}

func (m *mockBroadcastSender) Broadcast(message []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, message)
}

func (m *mockBroadcastSender) messageCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.messages)
}

// healthPollerTestRegistry creates a Registry with a stubbed k8s factory that returns
// a client backed by a fake clientset (Discovery().ServerVersion() succeeds).
func healthPollerTestRegistry(repo models.ClusterRepository) *Registry {
	r := NewRegistry(RegistryConfig{
		ClusterRepo: repo,
		HelmBinary:  "helm",
		HelmTimeout: 5 * time.Minute,
	})
	r.k8sFactory = func(_ string) (*k8s.Client, error) {
		return k8s.NewClientFromInterface(fake.NewSimpleClientset()), nil
	}
	r.helmFactory = stubHelmFactory
	return r
}

// healthPollerFailingRegistry creates a Registry whose k8s factory always fails.
func healthPollerFailingRegistry(repo models.ClusterRepository) *Registry {
	r := NewRegistry(RegistryConfig{
		ClusterRepo: repo,
		HelmBinary:  "helm",
		HelmTimeout: 5 * time.Minute,
	})
	r.k8sFactory = func(_ string) (*k8s.Client, error) {
		return nil, fmt.Errorf("connection refused")
	}
	r.helmFactory = stubHelmFactory
	return r
}

// --- Tests ---

func TestHealthPoller_StartStop(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	reg := healthPollerTestRegistry(repo)

	poller := NewHealthPoller(HealthPollerConfig{
		ClusterRepo: repo,
		Registry:    reg,
		Interval:    50 * time.Millisecond,
	})

	poller.Start()
	// Let it poll at least once.
	time.Sleep(100 * time.Millisecond)
	poller.Stop()
	// Stop should return without blocking.
}

func TestHealthPoller_UpdatesUnreachable(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["c1"] = &models.Cluster{
		ID:             "c1",
		Name:           "Cluster 1",
		KubeconfigPath: "/fake/path",
		HealthStatus:   models.ClusterHealthy,
	}

	reg := healthPollerFailingRegistry(repo)
	hub := &mockBroadcastSender{}

	poller := NewHealthPoller(HealthPollerConfig{
		ClusterRepo: repo,
		Registry:    reg,
		Interval:    50 * time.Millisecond,
		Hub:         hub,
	})

	poller.Start()
	// Wait for at least one poll cycle.
	time.Sleep(150 * time.Millisecond)
	poller.Stop()

	repo.mu.Lock()
	cl := repo.clusters["c1"]
	repo.mu.Unlock()

	assert.Equal(t, models.ClusterUnreachable, cl.HealthStatus)
	assert.GreaterOrEqual(t, hub.messageCount(), 1, "expected at least one broadcast")
}

func TestHealthPoller_NoUpdateWhenUnchanged(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["c1"] = &models.Cluster{
		ID:             "c1",
		Name:           "Cluster 1",
		KubeconfigPath: "/fake/path",
		HealthStatus:   models.ClusterHealthy,
	}

	// Use a working fake clientset → status stays "healthy".
	reg := healthPollerTestRegistry(repo)
	hub := &mockBroadcastSender{}

	poller := NewHealthPoller(HealthPollerConfig{
		ClusterRepo: repo,
		Registry:    reg,
		Interval:    50 * time.Millisecond,
		Hub:         hub,
	})

	poller.Start()
	time.Sleep(200 * time.Millisecond)
	poller.Stop()

	// Status hasn't changed → no broadcast.
	assert.Equal(t, 0, hub.messageCount(), "expected no broadcasts when status unchanged")

	repo.mu.Lock()
	cl := repo.clusters["c1"]
	repo.mu.Unlock()
	assert.Equal(t, models.ClusterHealthy, cl.HealthStatus)
}

func TestHealthPoller_NilRegistry(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["c1"] = &models.Cluster{
		ID:             "c1",
		Name:           "Cluster 1",
		KubeconfigPath: "/fake/path",
		HealthStatus:   models.ClusterHealthy,
	}

	poller := NewHealthPoller(HealthPollerConfig{
		ClusterRepo: repo,
		Registry:    nil,
		Interval:    50 * time.Millisecond,
	})

	poller.Start()
	time.Sleep(150 * time.Millisecond)
	poller.Stop()

	// Status should remain unchanged — poller skips when registry is nil.
	repo.mu.Lock()
	cl := repo.clusters["c1"]
	repo.mu.Unlock()
	assert.Equal(t, models.ClusterHealthy, cl.HealthStatus)
}

func TestHealthPoller_DefaultInterval(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	poller := NewHealthPoller(HealthPollerConfig{
		ClusterRepo: repo,
		Registry:    healthPollerTestRegistry(repo),
		Interval:    0, // should default
	})

	require.Equal(t, defaultPollInterval, poller.interval)
	// Cleanup — don't actually start a 60s poller in tests.
}

func TestHealthPoller_NilHub(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["c1"] = &models.Cluster{
		ID:             "c1",
		Name:           "Cluster 1",
		KubeconfigPath: "/fake/path",
		HealthStatus:   models.ClusterHealthy,
	}

	reg := healthPollerFailingRegistry(repo)

	// Hub is nil — should not panic.
	poller := NewHealthPoller(HealthPollerConfig{
		ClusterRepo: repo,
		Registry:    reg,
		Interval:    50 * time.Millisecond,
		Hub:         nil,
	})

	poller.Start()
	time.Sleep(150 * time.Millisecond)
	poller.Stop()

	repo.mu.Lock()
	cl := repo.clusters["c1"]
	repo.mu.Unlock()
	assert.Equal(t, models.ClusterUnreachable, cl.HealthStatus)
}

func TestHealthPoller_ListError(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["c1"] = &models.Cluster{
		ID:             "c1",
		Name:           "Cluster 1",
		KubeconfigPath: "/fake/path",
		HealthStatus:   models.ClusterHealthy,
	}
	repo.listErr = fmt.Errorf("database unavailable")

	reg := healthPollerTestRegistry(repo)
	hub := &mockBroadcastSender{}

	poller := NewHealthPoller(HealthPollerConfig{
		ClusterRepo: repo,
		Registry:    reg,
		Interval:    50 * time.Millisecond,
		Hub:         hub,
	})

	poller.Start()
	time.Sleep(150 * time.Millisecond)
	poller.Stop()

	// Status should be unchanged since list failed.
	repo.mu.Lock()
	cl := repo.clusters["c1"]
	repo.mu.Unlock()
	assert.Equal(t, models.ClusterHealthy, cl.HealthStatus)
	assert.Equal(t, 0, hub.messageCount())
}

func TestHealthPoller_UpdateRepoError(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["c1"] = &models.Cluster{
		ID:             "c1",
		Name:           "Cluster 1",
		KubeconfigPath: "/fake/path",
		HealthStatus:   models.ClusterHealthy,
	}

	// Use a failing registry so health changes to "unreachable",
	// but make update fail.
	reg := healthPollerFailingRegistry(repo)
	repo.updateErr = fmt.Errorf("database write failed")
	hub := &mockBroadcastSender{}

	poller := NewHealthPoller(HealthPollerConfig{
		ClusterRepo: repo,
		Registry:    reg,
		Interval:    50 * time.Millisecond,
		Hub:         hub,
	})

	poller.Start()
	time.Sleep(150 * time.Millisecond)
	poller.Stop()

	// Update failed, so status in repo should remain original (but poll shouldn't crash).
	repo.mu.Lock()
	cl := repo.clusters["c1"]
	repo.mu.Unlock()
	assert.Equal(t, models.ClusterHealthy, cl.HealthStatus)
	// No broadcast since update failed (continue skips broadcastChange).
	assert.Equal(t, 0, hub.messageCount())
}

func TestHealthPoller_MultipleClusters(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["c1"] = &models.Cluster{
		ID:             "c1",
		Name:           "Cluster 1",
		KubeconfigPath: "/fake/path",
		HealthStatus:   models.ClusterUnreachable, // will become healthy
	}
	repo.clusters["c2"] = &models.Cluster{
		ID:             "c2",
		Name:           "Cluster 2",
		KubeconfigPath: "/fake/path2",
		HealthStatus:   models.ClusterHealthy, // stays healthy
	}

	reg := healthPollerTestRegistry(repo)
	hub := &mockBroadcastSender{}

	poller := NewHealthPoller(HealthPollerConfig{
		ClusterRepo: repo,
		Registry:    reg,
		Interval:    50 * time.Millisecond,
		Hub:         hub,
	})

	poller.Start()
	time.Sleep(200 * time.Millisecond)
	poller.Stop()

	repo.mu.Lock()
	c1 := repo.clusters["c1"]
	c2 := repo.clusters["c2"]
	repo.mu.Unlock()

	// c1 should now be healthy (changed from unreachable).
	assert.Equal(t, models.ClusterHealthy, c1.HealthStatus)
	// c2 should still be healthy (unchanged).
	assert.Equal(t, models.ClusterHealthy, c2.HealthStatus)
	// Only c1 changed → at least 1 broadcast.
	assert.GreaterOrEqual(t, hub.messageCount(), 1)
}

func TestHealthPoller_NegativeInterval(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	poller := NewHealthPoller(HealthPollerConfig{
		ClusterRepo: repo,
		Registry:    healthPollerTestRegistry(repo),
		Interval:    -1 * time.Second, // negative should default
	})

	require.Equal(t, defaultPollInterval, poller.interval)
}
