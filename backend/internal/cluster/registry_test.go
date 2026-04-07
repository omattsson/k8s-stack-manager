package cluster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"backend/internal/deployer"
	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
)

// --- Mock ClusterRepository ---

type mockClusterRepo struct {
	mu       sync.Mutex
	clusters map[string]*models.Cluster
	// Track call counts for assertions.
	findByIDCalls  int
	findDefaultErr error
	listErr        error
	updateErr      error
}

func newMockClusterRepo() *mockClusterRepo {
	return &mockClusterRepo{
		clusters: make(map[string]*models.Cluster),
	}
}

func (m *mockClusterRepo) Create(cluster *models.Cluster) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clusters[cluster.ID] = cluster
	return nil
}

func (m *mockClusterRepo) FindByID(id string) (*models.Cluster, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.findByIDCalls++
	c, ok := m.clusters[id]
	if !ok {
		return nil, fmt.Errorf("cluster not found: %s", id)
	}
	return c, nil
}

func (m *mockClusterRepo) Update(cluster *models.Cluster) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updateErr != nil {
		return m.updateErr
	}
	m.clusters[cluster.ID] = cluster
	return nil
}

func (m *mockClusterRepo) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.clusters, id)
	return nil
}

func (m *mockClusterRepo) List() ([]models.Cluster, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	var out []models.Cluster
	for _, c := range m.clusters {
		out = append(out, *c)
	}
	return out, nil
}

func (m *mockClusterRepo) FindDefault() (*models.Cluster, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.findDefaultErr != nil {
		return nil, m.findDefaultErr
	}
	for _, c := range m.clusters {
		if c.IsDefault {
			return c, nil
		}
	}
	return nil, fmt.Errorf("no default cluster")
}

func (m *mockClusterRepo) SetDefault(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clusters {
		c.IsDefault = c.ID == id
	}
	return nil
}

// --- Stub factories ---

func stubK8sFactory(_ string) (*k8s.Client, error) {
	return k8s.NewClientFromInterface(nil), nil
}

func stubHelmFactory(_, _ string, _ time.Duration) deployer.HelmExecutor {
	return nil // tests don't exercise helm operations
}

func failingK8sFactory(_ string) (*k8s.Client, error) {
	return nil, fmt.Errorf("k8s connection refused")
}

// --- Helpers ---

func newTestRegistry(repo models.ClusterRepository) *Registry {
	r := NewRegistry(RegistryConfig{
		ClusterRepo: repo,
		HelmBinary:  "helm",
		HelmTimeout: 5 * time.Minute,
	})
	r.k8sFactory = stubK8sFactory
	r.helmFactory = stubHelmFactory
	return r
}

// --- Tests ---

func TestResolveClusterID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		clusterID  string
		hasDefault bool
		wantID     string
		wantErr    string
	}{
		{
			name:      "non-empty ID returned as-is",
			clusterID: "cluster-abc",
			wantID:    "cluster-abc",
		},
		{
			name:       "empty ID resolves to default",
			clusterID:  "",
			hasDefault: true,
			wantID:     "default-cluster",
		},
		{
			name:      "empty ID with no default returns error",
			clusterID: "",
			wantErr:   "no default cluster configured",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := newMockClusterRepo()
			if tt.hasDefault {
				repo.clusters["default-cluster"] = &models.Cluster{
					ID:             "default-cluster",
					Name:           "Default",
					IsDefault:      true,
					KubeconfigPath: "/fake/kubeconfig",
				}
			}

			reg := newTestRegistry(repo)
			id, err := reg.ResolveClusterID(tt.clusterID)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantID, id)
			}
		})
	}
}

func TestGetClients_CacheHit(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["c1"] = &models.Cluster{
		ID:             "c1",
		Name:           "Cluster 1",
		KubeconfigPath: "/fake/kubeconfig",
	}

	reg := newTestRegistry(repo)

	// First call — cache miss.
	cc1, err := reg.GetClients("c1")
	require.NoError(t, err)
	require.NotNil(t, cc1)

	// Second call — cache hit.
	cc2, err := reg.GetClients("c1")
	require.NoError(t, err)
	assert.Same(t, cc1, cc2)

	// Repo should have been called only once.
	repo.mu.Lock()
	assert.Equal(t, 1, repo.findByIDCalls)
	repo.mu.Unlock()
}

func TestGetClients_ClusterNotFound(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	reg := newTestRegistry(repo)

	_, err := reg.GetClients("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster nonexistent")
	assert.Contains(t, err.Error(), "not found")
}

func TestGetClients_K8sFactoryError(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["c1"] = &models.Cluster{
		ID:             "c1",
		Name:           "Cluster 1",
		KubeconfigPath: "/fake/kubeconfig",
	}

	reg := newTestRegistry(repo)
	reg.k8sFactory = failingK8sFactory

	_, err := reg.GetClients("c1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "k8s connection refused")
}

func TestGetClients_NoKubeconfigReturnsError(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["bare"] = &models.Cluster{
		ID:   "bare",
		Name: "No Kubeconfig",
		// Neither KubeconfigPath nor KubeconfigData set.
	}

	reg := newTestRegistry(repo)

	_, err := reg.GetClients("bare")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither kubeconfig path nor kubeconfig data")
}

func TestGetDefaultClients(t *testing.T) {
	t.Parallel()

	t.Run("returns default cluster clients", func(t *testing.T) {
		t.Parallel()

		repo := newMockClusterRepo()
		repo.clusters["def"] = &models.Cluster{
			ID:             "def",
			Name:           "Default",
			IsDefault:      true,
			KubeconfigPath: "/fake/kubeconfig",
		}
		reg := newTestRegistry(repo)

		cc, err := reg.GetDefaultClients()
		require.NoError(t, err)
		require.NotNil(t, cc)
	})

	t.Run("error when no default configured", func(t *testing.T) {
		t.Parallel()

		repo := newMockClusterRepo()
		reg := newTestRegistry(repo)

		_, err := reg.GetDefaultClients()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no default cluster configured")
	})

	t.Run("caches default resolution", func(t *testing.T) {
		t.Parallel()

		repo := newMockClusterRepo()
		repo.clusters["def"] = &models.Cluster{
			ID:             "def",
			Name:           "Default",
			IsDefault:      true,
			KubeconfigPath: "/fake/kubeconfig",
		}
		reg := newTestRegistry(repo)

		cc1, err := reg.GetDefaultClients()
		require.NoError(t, err)

		cc2, err := reg.GetDefaultClients()
		require.NoError(t, err)
		assert.Same(t, cc1, cc2)
	})
}

func TestInvalidateClient(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["c1"] = &models.Cluster{
		ID:             "c1",
		Name:           "Cluster 1",
		KubeconfigPath: "/fake/kubeconfig",
	}

	reg := newTestRegistry(repo)

	// Populate cache.
	_, err := reg.GetClients("c1")
	require.NoError(t, err)

	// Invalidate.
	reg.InvalidateClient("c1")

	// Should re-fetch from repo on next call.
	_, err = reg.GetClients("c1")
	require.NoError(t, err)

	repo.mu.Lock()
	assert.Equal(t, 2, repo.findByIDCalls)
	repo.mu.Unlock()
}

func TestInvalidateClient_NonexistentIsNoop(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	reg := newTestRegistry(repo)

	// Should not panic.
	reg.InvalidateClient("nonexistent")
}

func TestInvalidateDefault(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["def"] = &models.Cluster{
		ID:             "def",
		Name:           "Default",
		IsDefault:      true,
		KubeconfigPath: "/fake/kubeconfig",
	}

	reg := newTestRegistry(repo)

	// Resolve default.
	_, err := reg.GetDefaultClients()
	require.NoError(t, err)

	reg.mu.RLock()
	assert.True(t, reg.defaultResolved)
	assert.Equal(t, "def", reg.defaultID)
	reg.mu.RUnlock()

	// Invalidate default.
	reg.InvalidateDefault()

	reg.mu.RLock()
	assert.False(t, reg.defaultResolved)
	assert.Empty(t, reg.defaultID)
	reg.mu.RUnlock()

	// Should re-resolve on next call.
	_, err = reg.GetDefaultClients()
	require.NoError(t, err)
}

func TestClose(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["c1"] = &models.Cluster{
		ID:             "c1",
		Name:           "Cluster 1",
		KubeconfigPath: "/fake/kubeconfig",
	}
	repo.clusters["c2"] = &models.Cluster{
		ID:             "c2",
		Name:           "Cluster 2",
		KubeconfigPath: "/fake/kubeconfig2",
	}

	reg := newTestRegistry(repo)

	_, err := reg.GetClients("c1")
	require.NoError(t, err)
	_, err = reg.GetClients("c2")
	require.NoError(t, err)

	err = reg.Close()
	require.NoError(t, err)

	reg.mu.RLock()
	assert.Empty(t, reg.clients)
	reg.mu.RUnlock()
}

func TestBuildClients_KubeconfigPath(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["path-cluster"] = &models.Cluster{
		ID:             "path-cluster",
		Name:           "Path Cluster",
		KubeconfigPath: "/some/path/kubeconfig",
	}

	reg := newTestRegistry(repo)

	cc, err := reg.GetClients("path-cluster")
	require.NoError(t, err)
	require.NotNil(t, cc)
	assert.Empty(t, cc.kubeconfigPath, "no temp file when using path")
}

func TestBuildClients_KubeconfigData_TempFileLifecycle(t *testing.T) {
	t.Parallel()

	kubeconfigContent := "apiVersion: v1\nkind: Config\nclusters: []\n"

	repo := newMockClusterRepo()
	repo.clusters["data-cluster"] = &models.Cluster{
		ID:             "data-cluster",
		Name:           "Data Cluster",
		KubeconfigData: kubeconfigContent,
	}

	reg := newTestRegistry(repo)

	cc, err := reg.GetClients("data-cluster")
	require.NoError(t, err)
	require.NotNil(t, cc)

	// Verify temp file was created.
	require.NotEmpty(t, cc.kubeconfigPath)
	_, err = os.Stat(cc.kubeconfigPath)
	require.NoError(t, err, "temp kubeconfig file should exist")

	// Verify file permissions are 0600.
	info, err := os.Stat(cc.kubeconfigPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	// Verify file content matches decrypted kubeconfig.
	content, err := os.ReadFile(cc.kubeconfigPath)
	require.NoError(t, err)
	assert.Equal(t, kubeconfigContent, string(content))

	tempPath := cc.kubeconfigPath

	// InvalidateClient should clean up the temp file.
	reg.InvalidateClient("data-cluster")
	_, err = os.Stat(tempPath)
	assert.True(t, os.IsNotExist(err), "temp file should be removed after invalidation")
}

func TestBuildClients_KubeconfigData_CleanupOnClose(t *testing.T) {
	t.Parallel()

	kubeconfigContent := "apiVersion: v1\nkind: Config\n"

	repo := newMockClusterRepo()
	repo.clusters["data-cluster"] = &models.Cluster{
		ID:             "data-cluster",
		Name:           "Data Cluster",
		KubeconfigData: kubeconfigContent,
	}

	reg := newTestRegistry(repo)

	cc, err := reg.GetClients("data-cluster")
	require.NoError(t, err)

	tempPath := cc.kubeconfigPath
	require.NotEmpty(t, tempPath)

	// Verify file exists.
	_, err = os.Stat(tempPath)
	require.NoError(t, err)

	// Close should clean up.
	err = reg.Close()
	require.NoError(t, err)

	_, err = os.Stat(tempPath)
	assert.True(t, os.IsNotExist(err), "temp file should be removed after Close")
}

func TestBuildClients_K8sFactoryError_CleansUpTempFile(t *testing.T) {
	t.Parallel()

	kubeconfigContent := "apiVersion: v1\n"

	repo := newMockClusterRepo()
	repo.clusters["fail"] = &models.Cluster{
		ID:             "fail",
		Name:           "Fail Cluster",
		KubeconfigData: kubeconfigContent,
	}

	reg := newTestRegistry(repo)
	reg.k8sFactory = failingK8sFactory

	_, err := reg.GetClients("fail")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "k8s connection refused")

	// Verify no temp files are left behind — glob for the prefix pattern.
	matches, _ := filepath.Glob(os.TempDir() + "/k8s-stack-kubeconfig-*")
	// We can't guarantee other tests haven't created files, but the failure path
	// should have removed ours. This is a best-effort check.
	for _, m := range matches {
		content, readErr := os.ReadFile(m)
		if readErr == nil && string(content) == "apiVersion: v1\n" {
			t.Errorf("temp file %s was not cleaned up after k8s factory error", m)
			os.Remove(m) // clean up for test hygiene
		}
	}
}

func TestConcurrentGetClients(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["c1"] = &models.Cluster{
		ID:             "c1",
		Name:           "Cluster 1",
		KubeconfigPath: "/fake/kubeconfig",
	}

	reg := newTestRegistry(repo)

	var wg sync.WaitGroup
	results := make([]*ClusterClients, 10)
	errs := make([]error, 10)

	for i := range 10 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = reg.GetClients("c1")
		}(i)
	}

	wg.Wait()

	for i := range 10 {
		require.NoError(t, errs[i], "goroutine %d returned error", i)
		require.NotNil(t, results[i])
	}

	// All goroutines should get the same cached instance.
	for i := 1; i < 10; i++ {
		assert.Same(t, results[0], results[i], "goroutine %d got different instance", i)
	}

	// Repo should have been called exactly once despite concurrent access.
	repo.mu.Lock()
	assert.Equal(t, 1, repo.findByIDCalls)
	repo.mu.Unlock()
}

func TestGetHelmExecutor(t *testing.T) {
	t.Parallel()

	t.Run("returns executor for specific cluster", func(t *testing.T) {
		t.Parallel()

		repo := newMockClusterRepo()
		repo.clusters["c1"] = &models.Cluster{
			ID:             "c1",
			Name:           "Cluster 1",
			KubeconfigPath: "/fake/kubeconfig",
		}

		reg := newTestRegistry(repo)

		exec, err := reg.GetHelmExecutor("c1")
		require.NoError(t, err)
		// stubHelmFactory returns nil, so exec is nil — that's expected.
		_ = exec
	})

	t.Run("returns executor for default cluster when empty ID", func(t *testing.T) {
		t.Parallel()

		repo := newMockClusterRepo()
		repo.clusters["def"] = &models.Cluster{
			ID:             "def",
			Name:           "Default",
			IsDefault:      true,
			KubeconfigPath: "/fake/kubeconfig",
		}

		reg := newTestRegistry(repo)

		exec, err := reg.GetHelmExecutor("")
		require.NoError(t, err)
		_ = exec
	})

	t.Run("error when cluster not found", func(t *testing.T) {
		t.Parallel()

		repo := newMockClusterRepo()
		reg := newTestRegistry(repo)

		_, err := reg.GetHelmExecutor("nonexistent")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cluster nonexistent")
	})

	t.Run("error when empty ID and no default", func(t *testing.T) {
		t.Parallel()

		repo := newMockClusterRepo()
		reg := newTestRegistry(repo)

		_, err := reg.GetHelmExecutor("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no default cluster configured")
	})
}

func TestGetK8sClient(t *testing.T) {
	t.Parallel()

	t.Run("returns client for specific cluster", func(t *testing.T) {
		t.Parallel()

		repo := newMockClusterRepo()
		repo.clusters["c1"] = &models.Cluster{
			ID:             "c1",
			Name:           "Cluster 1",
			KubeconfigPath: "/fake/kubeconfig",
		}

		reg := newTestRegistry(repo)

		client, err := reg.GetK8sClient("c1")
		require.NoError(t, err)
		require.NotNil(t, client)
	})

	t.Run("returns default client when empty ID", func(t *testing.T) {
		t.Parallel()

		repo := newMockClusterRepo()
		repo.clusters["def"] = &models.Cluster{
			ID:             "def",
			Name:           "Default",
			IsDefault:      true,
			KubeconfigPath: "/fake/kubeconfig",
		}

		reg := newTestRegistry(repo)

		client, err := reg.GetK8sClient("")
		require.NoError(t, err)
		require.NotNil(t, client)
	})

	t.Run("error when empty ID and no default", func(t *testing.T) {
		t.Parallel()

		repo := newMockClusterRepo()
		reg := newTestRegistry(repo)

		_, err := reg.GetK8sClient("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no default cluster configured")
	})

	t.Run("error when specific cluster not found", func(t *testing.T) {
		t.Parallel()

		repo := newMockClusterRepo()
		reg := newTestRegistry(repo)

		_, err := reg.GetK8sClient("nonexistent")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cluster nonexistent")
	})
}

func TestNewRegistryForTest(t *testing.T) {
	t.Parallel()

	k8sClient := k8s.NewClientFromInterface(nil)
	reg := NewRegistryForTest("test-cluster", k8sClient, nil)

	// Should have pre-populated client.
	cc, err := reg.GetClients("test-cluster")
	require.NoError(t, err)
	assert.Same(t, k8sClient, cc.K8s)

	// Default should be resolved.
	id, err := reg.ResolveClusterID("")
	require.NoError(t, err)
	assert.Equal(t, "test-cluster", id)
}

func TestResolveClusterID_CachesDefault(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["def"] = &models.Cluster{
		ID:             "def",
		Name:           "Default",
		IsDefault:      true,
		KubeconfigPath: "/fake/kubeconfig",
	}

	reg := newTestRegistry(repo)

	// First call resolves and caches.
	id1, err := reg.ResolveClusterID("")
	require.NoError(t, err)
	assert.Equal(t, "def", id1)

	// Second call uses cache — even if we remove the cluster from repo.
	delete(repo.clusters, "def")
	id2, err := reg.ResolveClusterID("")
	require.NoError(t, err)
	assert.Equal(t, "def", id2)
}

func TestGetDefaultClients_ErrorCachesEmpty(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.findDefaultErr = fmt.Errorf("db error")

	reg := newTestRegistry(repo)

	// First call — error, but default gets "resolved" as empty.
	_, err := reg.GetDefaultClients()
	require.Error(t, err)

	// Second call — uses cached empty default, still error but without hitting repo again.
	_, err = reg.GetDefaultClients()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no default cluster configured")
}

func TestConcurrentResolveClusterID(t *testing.T) {
	t.Parallel()

	repo := newMockClusterRepo()
	repo.clusters["def"] = &models.Cluster{
		ID:             "def",
		Name:           "Default",
		IsDefault:      true,
		KubeconfigPath: "/fake/kubeconfig",
	}

	reg := newTestRegistry(repo)

	var wg sync.WaitGroup
	results := make([]string, 20)
	errs := make([]error, 20)

	for i := range 20 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = reg.ResolveClusterID("")
		}(i)
	}

	wg.Wait()

	for i := range 20 {
		require.NoError(t, errs[i], "goroutine %d returned error", i)
		assert.Equal(t, "def", results[i], "goroutine %d got wrong ID", i)
	}
}

func TestHealthCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		clusters   map[string]*models.Cluster
		k8sFactory K8sClientFactory
		listErr    error
		wantErr    string
	}{
		{
			name:     "no clusters registered returns nil",
			clusters: map[string]*models.Cluster{},
		},
		{
			name:    "list error returns error",
			listErr: fmt.Errorf("db down"),
			wantErr: "failed to list clusters",
		},
		{
			name: "at least one reachable cluster returns nil",
			clusters: map[string]*models.Cluster{
				"c1": {ID: "c1", Name: "Cluster 1", KubeconfigPath: "/fake/kubeconfig"},
				"c2": {ID: "c2", Name: "Cluster 2", KubeconfigPath: "/fake/kubeconfig"},
			},
			k8sFactory: func(_ string) (*k8s.Client, error) {
				return k8s.NewClientFromInterface(fake.NewSimpleClientset()), nil
			},
		},
		{
			name: "all clusters unreachable returns error",
			clusters: map[string]*models.Cluster{
				"c1": {ID: "c1", Name: "Cluster 1", KubeconfigPath: "/fake/kubeconfig"},
			},
			k8sFactory: failingK8sFactory,
			wantErr:    "all 1 registered clusters are unreachable",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := newMockClusterRepo()
			repo.listErr = tt.listErr
			for id, cl := range tt.clusters {
				repo.clusters[id] = cl
			}

			reg := newTestRegistry(repo)
			if tt.k8sFactory != nil {
				reg.k8sFactory = tt.k8sFactory
			}

			err := reg.HealthCheck(context.Background())
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
