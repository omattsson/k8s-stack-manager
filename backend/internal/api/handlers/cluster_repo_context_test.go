package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookClusterRepo runs the hook of hooks with "before:Cluster.<Method>" and
// "after:Cluster.<Method>" around the calls of MockClusterRepository, which
// fails on a done context (#656). The registry of the fixture reads through
// it too.
type hookClusterRepo struct {
	*MockClusterRepository
	hooks *hookInstanceRepo
}

func (r *hookClusterRepo) Create(ctx context.Context, cl *models.Cluster) error {
	r.hooks.run("before:Cluster.Create", ctx)
	defer r.hooks.run("after:Cluster.Create", ctx)
	return r.MockClusterRepository.Create(ctx, cl)
}

func (r *hookClusterRepo) FindByID(ctx context.Context, id string) (*models.Cluster, error) {
	r.hooks.run("before:Cluster.FindByID", ctx)
	defer r.hooks.run("after:Cluster.FindByID", ctx)
	return r.MockClusterRepository.FindByID(ctx, id)
}

func (r *hookClusterRepo) Update(ctx context.Context, cl *models.Cluster) error {
	r.hooks.run("before:Cluster.Update", ctx)
	defer r.hooks.run("after:Cluster.Update", ctx)
	return r.MockClusterRepository.Update(ctx, cl)
}

func (r *hookClusterRepo) Delete(ctx context.Context, id string) error {
	r.hooks.run("before:Cluster.Delete", ctx)
	defer r.hooks.run("after:Cluster.Delete", ctx)
	return r.MockClusterRepository.Delete(ctx, id)
}

func (r *hookClusterRepo) List(ctx context.Context) ([]models.Cluster, error) {
	r.hooks.run("before:Cluster.List", ctx)
	defer r.hooks.run("after:Cluster.List", ctx)
	return r.MockClusterRepository.List(ctx)
}

func (r *hookClusterRepo) FindDefault(ctx context.Context) (*models.Cluster, error) {
	r.hooks.run("before:Cluster.FindDefault", ctx)
	defer r.hooks.run("after:Cluster.FindDefault", ctx)
	return r.MockClusterRepository.FindDefault(ctx)
}

func (r *hookClusterRepo) SetDefault(ctx context.Context, id string) error {
	r.hooks.run("before:Cluster.SetDefault", ctx)
	defer r.hooks.run("after:Cluster.SetDefault", ctx)
	return r.MockClusterRepository.SetDefault(ctx, id)
}

// hookQuotaRepo runs the hook of hooks around the cluster quota calls and,
// unlike MockResourceQuotaRepository, fails on a done context.
type hookQuotaRepo struct {
	*MockResourceQuotaRepository
	hooks *hookInstanceRepo
}

func (r *hookQuotaRepo) GetByClusterID(ctx context.Context, clusterID string) (*models.ResourceQuotaConfig, error) {
	r.hooks.run("before:Quota.GetByClusterID", ctx)
	defer r.hooks.run("after:Quota.GetByClusterID", ctx)
	if err := ctxDone(ctx, "get_by_cluster_id"); err != nil {
		return nil, err
	}
	return r.MockResourceQuotaRepository.GetByClusterID(ctx, clusterID)
}

func (r *hookQuotaRepo) Upsert(ctx context.Context, config *models.ResourceQuotaConfig) error {
	r.hooks.run("before:Quota.Upsert", ctx)
	defer r.hooks.run("after:Quota.Upsert", ctx)
	if err := ctxDone(ctx, "upsert"); err != nil {
		return err
	}
	return r.MockResourceQuotaRepository.Upsert(ctx, config)
}

func (r *hookQuotaRepo) Delete(ctx context.Context, clusterID string) error {
	r.hooks.run("before:Quota.Delete", ctx)
	defer r.hooks.run("after:Quota.Delete", ctx)
	if err := ctxDone(ctx, "delete"); err != nil {
		return err
	}
	return r.MockResourceQuotaRepository.Delete(ctx, clusterID)
}

// clusterCtxFixture is a cluster handler over mocks that fail on a done
// context, with a real registry and the audit middleware (#656).
type clusterCtxFixture struct {
	hooks    *hookInstanceRepo
	clusters *hookClusterRepo
	quotas   *hookQuotaRepo
	registry *cluster.Registry
	handler  *ClusterHandler
	audit    *MockAuditLogRepository
	router   *gin.Engine
	// propagated counts the background quota writes (quotaClients calls).
	propagated atomic.Int32
}

// newClusterCtxFixture seeds cl-1 (default, quota 4 CPU) and cl-2. The
// registry has resolved the default (cl-1) before the test request.
func newClusterCtxFixture(t *testing.T) *clusterCtxFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &clusterCtxFixture{hooks: newHookInstanceRepo(), audit: NewMockAuditLogRepository()}
	f.clusters = &hookClusterRepo{MockClusterRepository: NewMockClusterRepository(), hooks: f.hooks}
	f.quotas = &hookQuotaRepo{MockResourceQuotaRepository: NewMockResourceQuotaRepository(), hooks: f.hooks}
	f.registry = cluster.NewRegistry(cluster.RegistryOptions{ClusterRepo: f.clusters, HelmBinary: "helm"})
	f.handler = NewClusterHandlerWithQuotas(f.clusters, f.registry, f.hooks, f.quotas)
	f.handler.quotaClients = func(string) (namespaceQuotaClient, error) {
		f.propagated.Add(1)
		return nil, errors.New("no cluster in the test")
	}
	svh := NewSharedValuesHandler(newMockSharedValuesRepo(), f.clusters)

	r := gin.New()
	r.Use(injectAuthContext("uid-admin", "admin"), middleware.NewAuditMiddleware(f.audit))
	cl := r.Group("/api/v1/clusters")
	cl.GET("", f.handler.ListClusters)
	cl.POST("", f.handler.CreateCluster)
	cl.GET("/:id", f.handler.GetCluster)
	cl.PUT("/:id", f.handler.UpdateCluster)
	cl.DELETE("/:id", f.handler.DeleteCluster)
	cl.POST("/:id/test", f.handler.TestClusterConnection)
	cl.POST("/:id/default", f.handler.SetDefaultCluster)
	cl.GET("/:id/health/summary", f.handler.GetClusterHealthSummary)
	cl.GET("/:id/health/nodes", f.handler.GetClusterNodes)
	cl.GET("/:id/namespaces", f.handler.GetClusterNamespaces)
	cl.GET("/:id/quotas", f.handler.GetQuotas)
	cl.PUT("/:id/quotas", f.handler.UpdateQuotas)
	cl.DELETE("/:id/quotas", f.handler.DeleteQuotas)
	cl.GET("/:id/utilization", f.handler.GetUtilization)
	cl.GET("/:id/shared-values", svh.ListSharedValues)
	cl.POST("/:id/shared-values", svh.CreateSharedValues)
	cl.PUT("/:id/shared-values/:valueId", svh.UpdateSharedValues)
	f.router = r

	ctx := context.Background()
	require.NoError(t, f.clusters.MockClusterRepository.Create(ctx, &models.Cluster{
		ID: "cl-1", Name: "one", APIServerURL: "https://one.example.com", KubeconfigPath: "/kc/one", IsDefault: true,
	}))
	require.NoError(t, f.clusters.MockClusterRepository.Create(ctx, &models.Cluster{
		ID: "cl-2", Name: "two", APIServerURL: "https://two.example.com", KubeconfigPath: "/kc/two",
	}))
	require.NoError(t, f.quotas.MockResourceQuotaRepository.Upsert(ctx, &models.ResourceQuotaConfig{ClusterID: "cl-1", CPULimit: "4"}))
	id, err := f.registry.ResolveClusterID("")
	require.NoError(t, err)
	require.Equal(t, "cl-1", id)
	return f
}

// stored returns the stored cluster, or nil when it does not exist.
func (f *clusterCtxFixture) stored(t *testing.T, id string) *models.Cluster {
	t.Helper()
	cl, err := f.clusters.MockClusterRepository.FindByID(context.Background(), id)
	if errors.Is(err, dberrors.ErrNotFound) {
		return nil
	}
	require.NoError(t, err)
	return cl
}

// storedDefault returns the ID of the stored default cluster ("" for none).
func (f *clusterCtxFixture) storedDefault(t *testing.T) string {
	t.Helper()
	cl, err := f.clusters.MockClusterRepository.FindDefault(context.Background())
	if errors.Is(err, dberrors.ErrNotFound) {
		return ""
	}
	require.NoError(t, err)
	return cl.ID
}

// registryDefault returns the default cluster ID of the registry ("" for
// none). It reads the cache, or the repository after an invalidation.
func (f *clusterCtxFixture) registryDefault(t *testing.T) string {
	t.Helper()
	id, err := f.registry.ResolveClusterID("")
	if errors.Is(err, cluster.ErrNoDefaultCluster) {
		return ""
	}
	require.NoError(t, err)
	return id
}

// TestClusterWrites_CancelledAtWriteStillCommit checks that a client that
// leaves at a cluster write cannot stop the write, the registry refresh, the
// quota writes to the namespaces or the audit entry after it (#656). The
// mocks fail on a done context, so a write with the request context fails.
func TestClusterWrites_CancelledAtWriteStillCommit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		check      func(t *testing.T, f *clusterCtxFixture, w *httptest.ResponseRecorder)
		name       string
		method     string
		path       string
		body       string
		cancelAt   string
		liveAt     string // a hook point that must run with a live, detached context
		wantStatus int
	}{
		{
			name: "create as default", method: http.MethodPost, path: "/api/v1/clusters",
			body:     `{"name":"three","api_server_url":"https://three.example.com","kubeconfig_path":"/kc/three","is_default":true}`,
			cancelAt: "before:Cluster.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *clusterCtxFixture, w *httptest.ResponseRecorder) {
				var resp models.Cluster
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				require.NotNil(t, f.stored(t, resp.ID))
				assert.True(t, resp.IsDefault)
				assert.Equal(t, resp.ID, f.storedDefault(t))
				assert.Equal(t, resp.ID, f.registryDefault(t), "the registry default is refreshed")
			},
		},
		{
			name: "create, cancel before set default", method: http.MethodPost, path: "/api/v1/clusters",
			body:     `{"name":"three","api_server_url":"https://three.example.com","kubeconfig_path":"/kc/three","is_default":true}`,
			cancelAt: "after:Cluster.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *clusterCtxFixture, w *httptest.ResponseRecorder) {
				var resp models.Cluster
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Empty(t, w.Header().Get("Warning"))
				assert.Equal(t, resp.ID, f.storedDefault(t))
				assert.Equal(t, resp.ID, f.registryDefault(t))
			},
		},
		{
			name: "set default", method: http.MethodPost, path: "/api/v1/clusters/cl-2/default",
			cancelAt: "before:Cluster.SetDefault", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *clusterCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, "cl-2", f.storedDefault(t))
				assert.Equal(t, "cl-2", f.registryDefault(t), "the registry default is refreshed")
			},
		},
		{
			name: "update to default", method: http.MethodPut, path: "/api/v1/clusters/cl-2",
			body: `{"is_default":true}`, cancelAt: "before:Cluster.SetDefault", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *clusterCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, "cl-2", f.storedDefault(t))
				assert.True(t, f.stored(t, "cl-2").IsDefault)
				assert.Equal(t, "cl-2", f.registryDefault(t))
			},
		},
		{
			name: "update to default, cancel before update", method: http.MethodPut, path: "/api/v1/clusters/cl-2",
			body: `{"is_default":true,"name":"renamed"}`, cancelAt: "after:Cluster.SetDefault", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *clusterCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, "renamed", f.stored(t, "cl-2").Name)
				assert.Equal(t, "cl-2", f.registryDefault(t))
			},
		},
		{
			name: "update", method: http.MethodPut, path: "/api/v1/clusters/cl-2",
			body: `{"name":"renamed"}`, cancelAt: "before:Cluster.Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *clusterCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, "renamed", f.stored(t, "cl-2").Name)
			},
		},
		{
			name: "delete", method: http.MethodDelete, path: "/api/v1/clusters/cl-1",
			cancelAt: "before:Cluster.Delete", wantStatus: http.StatusNoContent,
			check: func(t *testing.T, f *clusterCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Nil(t, f.stored(t, "cl-1"))
				assert.Empty(t, f.registryDefault(t), "the registry forgets the deleted default")
			},
		},
		{
			name: "update quotas", method: http.MethodPut, path: "/api/v1/clusters/cl-1/quotas",
			body: `{"cpu_limit":"8"}`, cancelAt: "before:Quota.Upsert", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *clusterCtxFixture, w *httptest.ResponseRecorder) {
				q, err := f.quotas.MockResourceQuotaRepository.GetByClusterID(context.Background(), "cl-1")
				require.NoError(t, err)
				assert.Equal(t, "8", q.CPULimit)
				assert.Eventually(t, func() bool { return f.propagated.Load() == 1 },
					2*time.Second, 10*time.Millisecond, "the namespace quota writes start")
			},
		},
		{
			name: "update quotas, cancel before read-back", method: http.MethodPut, path: "/api/v1/clusters/cl-1/quotas",
			body: `{"cpu_limit":"8"}`, cancelAt: "after:Quota.Upsert", liveAt: "after:Quota.GetByClusterID",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, f *clusterCtxFixture, w *httptest.ResponseRecorder) {
				assert.Contains(t, w.Body.String(), `"id"`, "the answer is the read-back")
				assert.Eventually(t, func() bool { return f.propagated.Load() == 1 },
					2*time.Second, 10*time.Millisecond, "the namespace quota writes start")
			},
		},
		{
			name: "delete quotas", method: http.MethodDelete, path: "/api/v1/clusters/cl-1/quotas",
			cancelAt: "before:Quota.Delete", wantStatus: http.StatusNoContent,
			check: func(t *testing.T, f *clusterCtxFixture, _ *httptest.ResponseRecorder) {
				_, err := f.quotas.MockResourceQuotaRepository.GetByClusterID(context.Background(), "cl-1")
				assert.ErrorIs(t, err, dberrors.ErrNotFound)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newClusterCtxFixture(t)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			cancelFirst := cancelAt(tt.cancelAt, 1, cancel)
			live := firstCtxErrAt(tt.liveAt)
			f.hooks.setHook(func(point string, ctx context.Context) {
				cancelFirst(point, ctx)
				live.record(point, ctx)
			})

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			require.Error(t, req.Context().Err(), "the request was cancelled during the handler")
			if tt.liveAt != "" {
				live.assertLive(t)
			}
			tt.check(t, f, w)
			assert.Eventually(t, func() bool { return len(f.audit.snapshot()) == 1 },
				2*time.Second, 10*time.Millisecond, "one audit entry for the write")
		})
	}
}

// TestClusterRequests_CancelledReadChangesNothing checks that a client that
// leaves during a read gets 499, also the guard read before a write: the
// write never runs, the registry keeps its default and no audit entry is
// written (#656).
func TestClusterRequests_CancelledReadChangesNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		cancelAt string
	}{
		{name: "list", method: http.MethodGet, path: "/api/v1/clusters", cancelAt: "before:Cluster.List"},
		{name: "get", method: http.MethodGet, path: "/api/v1/clusters/cl-1", cancelAt: "before:Cluster.FindByID"},
		{name: "health summary", method: http.MethodGet, path: "/api/v1/clusters/cl-1/health/summary", cancelAt: "before:Cluster.FindByID"},
		{name: "nodes", method: http.MethodGet, path: "/api/v1/clusters/cl-1/health/nodes", cancelAt: "before:Cluster.FindByID"},
		{name: "namespaces", method: http.MethodGet, path: "/api/v1/clusters/cl-1/namespaces", cancelAt: "before:Cluster.FindByID"},
		{name: "quotas", method: http.MethodGet, path: "/api/v1/clusters/cl-1/quotas", cancelAt: "before:Cluster.FindByID"},
		{name: "utilization", method: http.MethodGet, path: "/api/v1/clusters/cl-1/utilization", cancelAt: "before:Cluster.FindByID"},
		{name: "shared values", method: http.MethodGet, path: "/api/v1/clusters/cl-1/shared-values", cancelAt: "before:Cluster.FindByID"},
		{name: "test connection", method: http.MethodPost, path: "/api/v1/clusters/cl-1/test", cancelAt: "before:Cluster.FindByID"},
		{
			name: "update", method: http.MethodPut, path: "/api/v1/clusters/cl-2",
			body: `{"name":"renamed","is_default":true}`, cancelAt: "before:Cluster.FindByID",
		},
		{name: "delete, cluster read", method: http.MethodDelete, path: "/api/v1/clusters/cl-1", cancelAt: "before:Cluster.FindByID"},
		{name: "delete, instance check", method: http.MethodDelete, path: "/api/v1/clusters/cl-1", cancelAt: "before:FindByCluster"},
		{
			name: "update quotas, cluster read", method: http.MethodPut, path: "/api/v1/clusters/cl-1/quotas",
			body: `{"cpu_limit":"8"}`, cancelAt: "before:Cluster.FindByID",
		},
		{name: "delete quotas", method: http.MethodDelete, path: "/api/v1/clusters/cl-1/quotas", cancelAt: "before:Cluster.FindByID"},
		{
			name: "create shared values", method: http.MethodPost, path: "/api/v1/clusters/cl-1/shared-values",
			body: `{"name":"base","values":"a: 1"}`, cancelAt: "before:Cluster.FindByID",
		},
		{
			name: "update shared values", method: http.MethodPut, path: "/api/v1/clusters/cl-1/shared-values/sv-1",
			body: `{"name":"base","values":"a: 2"}`, cancelAt: "before:Cluster.FindByID",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newClusterCtxFixture(t)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			// A later ctx-aware read also gives 499, so check that the
			// cancelled read itself got the request context.
			cancelFirst := cancelAt(tt.cancelAt, 1, cancel)
			readCtx := firstCtxErrAt("after:" + strings.TrimPrefix(tt.cancelAt, "before:"))
			f.hooks.setHook(func(point string, ctx context.Context) {
				cancelFirst(point, ctx)
				readCtx.record(point, ctx)
			})

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			require.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
			readCtx.assertCancelled(t)
			assert.Equal(t, "two", f.stored(t, "cl-2").Name)
			assert.NotNil(t, f.stored(t, "cl-1"))
			assert.Equal(t, "cl-1", f.storedDefault(t))
			assert.Equal(t, "cl-1", f.registryDefault(t))
			q, err := f.quotas.MockResourceQuotaRepository.GetByClusterID(context.Background(), "cl-1")
			require.NoError(t, err)
			assert.Equal(t, "4", q.CPULimit)
			assert.Zero(t, f.propagated.Load())
			assert.Never(t, func() bool { return len(f.audit.snapshot()) > 0 },
				100*time.Millisecond, 10*time.Millisecond, "no audit entry")
		})
	}
}

// TestClusterReads_DeadlineGives500 checks that a request whose deadline
// passed gets 500, not 499: a deadline is not a client cancel.
func TestClusterReads_DeadlineGives500(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/api/v1/clusters", "/api/v1/clusters/cl-1", "/api/v1/clusters/cl-1/quotas"} {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			f := newClusterCtxFixture(t)
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), msgInternalServerError)
		})
	}
}

// TestUpdateCluster_InvalidBodyDoesNotChangeDefault checks that the update
// validates the cluster before its first write: a 400 leaves the default.
func TestUpdateCluster_InvalidBodyDoesNotChangeDefault(t *testing.T) {
	t.Parallel()
	f := newClusterCtxFixture(t)

	w := serve(f.router, http.MethodPut, "/api/v1/clusters/cl-2", `{"is_default":true,"name":""}`)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Equal(t, "cl-1", f.storedDefault(t), "no default change before the validation")
	assert.Equal(t, "cl-1", f.registryDefault(t))
}

// TestUpdateCluster_FailedUpdateRefreshesDefault checks that the registry
// forgets the old default as soon as SetDefault committed, also when the
// update after it fails (here a duplicate name).
func TestUpdateCluster_FailedUpdateRefreshesDefault(t *testing.T) {
	t.Parallel()
	f := newClusterCtxFixture(t)
	f.hooks.setHook(func(point string, _ context.Context) {
		if point == "after:Cluster.SetDefault" {
			f.clusters.SetError(dberrors.NewDatabaseError("update", dberrors.ErrDuplicateKey))
		}
	})

	w := serve(f.router, http.MethodPut, "/api/v1/clusters/cl-2", `{"is_default":true,"name":"one"}`)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	f.clusters.SetError(nil)
	assert.Equal(t, "cl-2", f.storedDefault(t), "SetDefault committed")
	assert.Equal(t, "cl-2", f.registryDefault(t), "the registry follows the stored default")
}

// TestUpdateQuotas_FailedReadBackStillPropagates checks that the namespace
// quota writes start after the save also when the read-back fails.
func TestUpdateQuotas_FailedReadBackStillPropagates(t *testing.T) {
	t.Parallel()
	f := newClusterCtxFixture(t)
	f.hooks.setHook(func(point string, _ context.Context) {
		if point == "after:Quota.Upsert" {
			f.quotas.SetError(errors.New("db down"))
		}
	})

	w := serve(f.router, http.MethodPut, "/api/v1/clusters/cl-1/quotas", `{"cpu_limit":"8"}`)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Eventually(t, func() bool { return f.propagated.Load() == 1 },
		2*time.Second, 10*time.Millisecond, "the namespace quota writes start")
}

// TestCreateCluster_AutoDefaultAfterCancel checks that the first cluster
// becomes the default also when the client leaves after the create: the
// default check after the write runs detached.
func TestCreateCluster_AutoDefaultAfterCancel(t *testing.T) {
	t.Parallel()
	f := newClusterCtxFixture(t)
	for _, id := range []string{"cl-1", "cl-2"} {
		require.NoError(t, f.clusters.MockClusterRepository.Delete(context.Background(), id))
	}
	f.registry.InvalidateDefault()
	req, cancel := liveRequest(t, http.MethodPost, "/api/v1/clusters",
		`{"name":"first","api_server_url":"https://first.example.com","kubeconfig_path":"/kc/first"}`)
	f.hooks.setHook(cancelAt("after:Cluster.Create", 1, cancel))

	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var resp models.Cluster
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.IsDefault, "the first cluster becomes the default")
	assert.Equal(t, resp.ID, f.storedDefault(t))
	assert.Equal(t, resp.ID, f.registryDefault(t))
}

// TestDashboard_ClusterListIgnoresCallerCancel checks that the cluster list
// of the shared dashboard build does not use the request context of the
// caller that started it (#656).
func TestDashboard_ClusterListIgnoresCallerCancel(t *testing.T) {
	t.Parallel()
	f := newClusterCtxFixture(t)
	h := NewDashboardHandler(f.clusters, f.hooks, NewMockDeploymentLogRepository(), nil)
	t.Cleanup(h.Stop)
	r := gin.New()
	r.Use(injectAuthContext("uid-admin", "admin"))
	r.GET("/api/v1/dashboard", h.GetDashboard)
	req, cancel := liveRequest(t, http.MethodGet, "/api/v1/dashboard", "")
	f.hooks.setHook(cancelAt("before:Cluster.List", 1, cancel))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Error(t, req.Context().Err())
	var resp DashboardResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp.Clusters, 2)
}

// TestSetInstanceNames_ClusterLookupUsesContext checks that the cluster names
// of instance responses are read with the request context: a cancelled
// lookup leaves the names out instead of running after the client left.
func TestSetInstanceNames_ClusterLookupUsesContext(t *testing.T) {
	t.Parallel()
	clusters := NewMockClusterRepository()
	require.NoError(t, clusters.Create(context.Background(), &models.Cluster{ID: "cl-1", Name: "one"}))
	h := &InstanceHandler{clusterRepo: clusters}

	live := &models.StackInstance{ID: "i1", ClusterID: "cl-1"}
	h.setInstanceNames(context.Background(), live)
	assert.Equal(t, "one", live.ClusterName)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := &models.StackInstance{ID: "i1", ClusterID: "cl-1"}
	h.setInstanceNames(ctx, cancelled)
	assert.Empty(t, cancelled.ClusterName)
}

// TestChannelFilterWarnings_ClusterLookupUsesContext checks that the cluster
// check of the channel filter warnings runs with the request context: a
// cancelled check gives no warning instead of a false one.
func TestChannelFilterWarnings_ClusterLookupUsesContext(t *testing.T) {
	t.Parallel()
	h := (&NotificationChannelHandler{}).WithFilterLookups(nil, nil, NewMockClusterRepository())
	filters := models.NotificationChannelFilters{ClusterIDs: []string{"cl-missing"}}

	assert.Len(t, h.filterWarnings(context.Background(), filters), 1)
	assert.Empty(t, h.filterWarnings(cancelledCtx(), filters))
}

// TestLoadSharedValues_DefaultClusterReadUsesContext checks that the default
// cluster read of the values pipeline (without a registry) uses the caller
// context and fails closed on a cancel: values without the shared layer
// would be wrong.
func TestLoadSharedValues_DefaultClusterReadUsesContext(t *testing.T) {
	t.Parallel()
	clusters := NewMockClusterRepository()
	require.NoError(t, clusters.Create(context.Background(), &models.Cluster{ID: "cl-1", Name: "one", IsDefault: true}))
	shared := newMockSharedValuesRepo()
	require.NoError(t, shared.Create(context.Background(), &models.SharedValues{ID: "sv-1", ClusterID: "cl-1", Name: "base", Values: "a: 1\n"}))
	inst := &models.StackInstance{ID: "i1"}

	got, err := loadSharedValues(context.Background(), shared, nil, clusters, inst)
	require.NoError(t, err)
	assert.Equal(t, []string{"a: 1\n"}, got)

	_, err = loadSharedValues(cancelledCtx(), shared, nil, clusters, inst)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestValuesBuilder_DefaultClusterReadUsesContext checks that the values
// pipeline passes its context to the default cluster read of the shared
// values layer: a cancel fails the build instead of reading detached.
func TestValuesBuilder_DefaultClusterReadUsesContext(t *testing.T) {
	t.Parallel()
	clusters := NewMockClusterRepository()
	require.NoError(t, clusters.Create(context.Background(), &models.Cluster{ID: "cl-1", Name: "one", IsDefault: true}))
	shared := newMockSharedValuesRepo()
	require.NoError(t, shared.Create(context.Background(), &models.SharedValues{ID: "sv-1", ClusterID: "cl-1", Name: "base", Values: "a: 1\n"}))
	b := &valuesBuilder{sharedValuesRepo: shared, clusterRepo: clusters}
	inst := &models.StackInstance{ID: "i1", Name: "web"}
	def := &models.StackDefinition{ID: "def-1", Name: "web"}
	charts := []models.ChartConfig{{ID: "ch-1", ChartName: "api"}}

	layers, _, _, err := b.layers(context.Background(), inst, def, charts, "dev")
	require.NoError(t, err)
	require.Len(t, layers, 1)
	assert.Equal(t, []string{"a: 1\n"}, layers[0].SharedValues)

	_, _, _, err = b.layers(cancelledCtx(), inst, def, charts, "dev")
	assert.ErrorIs(t, err, context.Canceled)
}

func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// ctxErrAt records ctx.Err() at the first call of one hook point.
type ctxErrAt struct {
	err   error
	point string
	mu    sync.Mutex
	seen  bool
}

func firstCtxErrAt(point string) *ctxErrAt { return &ctxErrAt{point: point} }

func (r *ctxErrAt) record(point string, ctx context.Context) {
	if point != r.point {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.seen {
		r.seen, r.err = true, ctx.Err()
	}
}

// assertCancelled checks that the call at the point ran with the cancelled
// request context, not a detached one.
func (r *ctxErrAt) assertCancelled(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.True(t, r.seen, "%s ran", r.point)
	assert.ErrorIs(t, r.err, context.Canceled, "%s got the request context", r.point)
}

// assertLive checks that the call at the point ran with a live context.
func (r *ctxErrAt) assertLive(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.True(t, r.seen, "%s ran", r.point)
	assert.NoError(t, r.err, "%s ran detached from the request", r.point)
}

// instanceCreateWithClusters returns a router with CreateInstance over the
// cluster repository of f and definition def-1.
func instanceCreateWithClusters(t *testing.T, f *clusterCtxFixture, registry *cluster.Registry) (*gin.Engine, *hookInstanceRepo) {
	t.Helper()
	defs := NewMockStackDefinitionRepository()
	require.NoError(t, defs.Create(context.Background(), &models.StackDefinition{ID: "def-1", Name: "web", OwnerID: "uid-dev"}))
	h := &InstanceHandler{
		instanceRepo:   f.hooks,
		definitionRepo: defs,
		clusterRepo:    f.clusters,
		registry:       registry,
		txRunner:       &mockHandlerTxRunner{repos: database.TxRepos{StackInstance: f.hooks}},
	}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", "uid-dev")
		c.Set("username", "dev")
		c.Set("role", "user")
		c.Next()
	})
	r.POST("/api/v1/stack-instances", h.CreateInstance)
	return r, f.hooks
}

// TestCreateInstance_ClusterReadFailureStopsCreate checks that a failed read
// of the cluster (for the per-user instance limit) stops the create: it no
// longer skips the limit (#656).
func TestCreateInstance_ClusterReadFailureStopsCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup      func(f *clusterCtxFixture, cancel context.CancelFunc, readCtx *ctxErrAt)
		name       string
		clusterID  string
		wantStatus int
	}{
		{
			name: "read error", clusterID: "cl-1", wantStatus: http.StatusInternalServerError,
			setup: func(f *clusterCtxFixture, _ context.CancelFunc, _ *ctxErrAt) {
				f.clusters.SetFetchError(errors.New("db down"))
			},
		},
		{
			name: "cancelled", clusterID: "cl-1", wantStatus: statusClientClosedRequest,
			setup: func(f *clusterCtxFixture, cancel context.CancelFunc, readCtx *ctxErrAt) {
				cancelFirst := cancelAt("before:Cluster.FindByID", 1, cancel)
				f.hooks.setHook(func(point string, ctx context.Context) {
					cancelFirst(point, ctx)
					readCtx.record(point, ctx)
				})
			},
		},
		{name: "unknown cluster", clusterID: "cl-missing", wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newClusterCtxFixture(t)
			router, instances := instanceCreateWithClusters(t, f, nil)
			req, cancel := liveRequest(t, http.MethodPost, "/api/v1/stack-instances",
				`{"stack_definition_id":"def-1","name":"web-one","cluster_id":"`+tt.clusterID+`"}`)
			readCtx := firstCtxErrAt("after:Cluster.FindByID")
			if tt.setup != nil {
				tt.setup(f, cancel, readCtx)
			}

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			all, err := instances.MockStackInstanceRepository.List(context.Background())
			require.NoError(t, err)
			assert.Empty(t, all, "no instance is created")
			if tt.wantStatus == statusClientClosedRequest {
				// A later instance read also gives 499: check the cluster read itself.
				readCtx.assertCancelled(t)
			}
		})
	}
}

// TestCreateInstance_RegistryResolveIgnoresRequestCancel checks that the
// default resolve of the registry runs on the registry context: a caller
// that leaves during it gets 499, but the shared resolve completes and the
// next caller uses the cached default without a new read (#656).
func TestCreateInstance_RegistryResolveIgnoresRequestCancel(t *testing.T) {
	t.Parallel()
	f := newClusterCtxFixture(t)
	f.registry.InvalidateDefault()
	var defaultReads atomic.Int32
	router, instances := instanceCreateWithClusters(t, f, f.registry)
	req, cancel := liveRequest(t, http.MethodPost, "/api/v1/stack-instances",
		`{"stack_definition_id":"def-1","name":"web-one"}`)
	cancelFirst := cancelAt("before:Cluster.FindDefault", 1, cancel)
	f.hooks.setHook(func(point string, ctx context.Context) {
		if point == "before:Cluster.FindDefault" {
			defaultReads.Add(1)
		}
		cancelFirst(point, ctx)
	})

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
	all, err := instances.MockStackInstanceRepository.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, all)
	assert.Equal(t, int32(1), defaultReads.Load())
	assert.Equal(t, "cl-1", f.registryDefault(t), "the shared resolve completed")
	assert.Equal(t, int32(1), defaultReads.Load(), "the next caller uses the cached default")
}
