package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"backend/internal/config"
	"backend/internal/health"
	"backend/internal/models"
	"backend/internal/sessionstore"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ctxRecordingInstanceRepo records the contexts of the startup instance
// queries.
type ctxRecordingInstanceRepo struct {
	*mockInstanceRepo
	mu   sync.Mutex
	ctxs map[string]context.Context
}

func (r *ctxRecordingInstanceRepo) record(method string, ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctxs == nil {
		r.ctxs = map[string]context.Context{}
	}
	r.ctxs[method] = ctx
}

func (r *ctxRecordingInstanceRepo) ctx(method string) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ctxs[method]
}

func (r *ctxRecordingInstanceRepo) List(ctx context.Context) ([]models.StackInstance, error) {
	r.record("List", ctx)
	return r.mockInstanceRepo.List(ctx)
}

func (r *ctxRecordingInstanceRepo) Update(ctx context.Context, inst *models.StackInstance) error {
	r.record("Update", ctx)
	return r.mockInstanceRepo.Update(ctx, inst)
}

// FindByNameOnCluster and DuplicateInstanceNames make the repository a
// models.InstanceNameLookup (UNIQUE_INSTANCE_NAMES).
func (r *ctxRecordingInstanceRepo) FindByNameOnCluster(ctx context.Context, _, _ string, _ bool) ([]models.StackInstance, error) {
	return nil, ctx.Err()
}

func (r *ctxRecordingInstanceRepo) DuplicateInstanceNames(ctx context.Context, _ string) ([]models.DuplicateInstanceName, error) {
	r.record("DuplicateInstanceNames", ctx)
	return nil, ctx.Err()
}

// assertStartupContext checks that ctx has a deadline about timeout after
// start and was cancelled when the startup step returned.
func assertStartupContext(t *testing.T, ctx context.Context, start time.Time, timeout time.Duration) {
	t.Helper()
	require.NotNil(t, ctx, "the startup query ran")
	deadline, ok := ctx.Deadline()
	require.True(t, ok, "the startup context has a deadline")
	assert.WithinDuration(t, start.Add(timeout), deadline, 5*time.Second)
	assert.ErrorIs(t, ctx.Err(), context.Canceled, "the context is cancelled after the step")
}

// TestEnsureDefaultCluster_BackfillStartupContext checks that the startup
// backfill of the default cluster ID runs with a bounded context that ends
// when the backfill returns (#656).
func TestEnsureDefaultCluster_BackfillStartupContext(t *testing.T) {
	t.Parallel()

	kubeconfigPath := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfigPath, []byte(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://k8s.example.com:6443
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
users:
- name: test-user
  user:
    token: fake-token
`), 0o600))
	instances := &ctxRecordingInstanceRepo{mockInstanceRepo: newMockInstanceRepo()}
	require.NoError(t, instances.Create(context.Background(), &models.StackInstance{ID: "i1", Name: "one"}))
	cfg := &config.Config{Deployment: config.DeploymentConfig{KubeconfigPath: kubeconfigPath}}

	start := time.Now()
	ensureDefaultCluster(newMockClusterRepo(), instances, cfg)

	got, err := instances.FindByID(context.Background(), "i1")
	require.NoError(t, err)
	assert.NotEmpty(t, got.ClusterID, "the instance got the default cluster")
	listCtx := instances.ctx("List")
	assertStartupContext(t, listCtx, start, instanceBackfillTimeout)
	assert.Same(t, listCtx, instances.ctx("Update"), "list and update share the startup context")
}

// TestBuildHandlers_DuplicateNamesStartupContext checks that the startup
// query for duplicate instance names (UNIQUE_INSTANCE_NAMES) runs with a
// bounded context that ends when the query returns (#656).
func TestBuildHandlers_DuplicateNamesStartupContext(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := buildTestConfig()
	cfg.App.UniqueInstanceNames = true
	repos := buildTestRepositorySet()
	instances := &ctxRecordingInstanceRepo{mockInstanceRepo: newMockInstanceRepo()}
	repos.StackInstance = instances
	// The default stub returns no cluster and no error for FindDefault.
	repos.Cluster = newMockClusterRepo()
	hub := buildTestHub(t)
	svc, err := buildDomainServices(cfg, repos, hub, health.New())
	require.NoError(t, err)
	sessStore := sessionstore.NewMemoryStore()
	t.Cleanup(func() { sessStore.Stop() })

	start := time.Now()
	_, err = buildHandlers(cfg, repos, svc, sessStore, hub)
	require.NoError(t, err)

	assertStartupContext(t, instances.ctx("DuplicateInstanceNames"), start, duplicateNamesTimeout)
}
