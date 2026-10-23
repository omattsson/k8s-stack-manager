package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// overrideCtxFixture is the value override API of an InstanceHandler over
// mocks that fail on a done context, with the audit middleware (#656).
type overrideCtxFixture struct {
	hooks  *hookInstanceRepo
	ov     *hookValueOverrideRepo
	audit  *MockAuditLogRepository
	router *gin.Engine
}

// newOverrideCtxFixture seeds definition d1 (charts cc1 and cc2), instance i1
// of uid-1 and the override of i1 / cc1 (ov-1, "replicas: 1"). A non-nil repo
// replaces the override repository of the handler.
func newOverrideCtxFixture(t *testing.T, repo models.ValueOverrideRepository) *overrideCtxFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &overrideCtxFixture{hooks: newHookInstanceRepo(), audit: NewMockAuditLogRepository()}
	f.ov = newHookValueOverrideRepo(f.hooks)
	def := &hookDefinitionRepo{MockStackDefinitionRepository: NewMockStackDefinitionRepository(), hooks: f.hooks}
	seedDefinition(t, def.MockStackDefinitionRepository, "d1", "def-1", "uid-1")
	charts := newHookChartConfigRepo(f.hooks)
	ctx := context.Background()
	require.NoError(t, charts.Create(ctx, &models.ChartConfig{ID: "cc1", StackDefinitionID: "d1", ChartName: "web"}))
	require.NoError(t, charts.Create(ctx, &models.ChartConfig{ID: "cc2", StackDefinitionID: "d1", ChartName: "api"}))
	seedInstance(t, f.hooks.MockStackInstanceRepository, "i1", "one", "d1", "uid-1", models.StackStatusRunning)
	require.NoError(t, f.ov.MockValueOverrideRepository.Create(ctx, &models.ValueOverride{
		ID: "ov-1", StackInstanceID: "i1", ChartConfigID: "cc1", Values: "replicas: 1\n",
	}))
	if repo == nil {
		repo = f.ov
	}
	h := NewInstanceHandler(f.hooks, repo, NewMockChartBranchOverrideRepository(), def, charts,
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), NewMockUserRepository(), 0)

	r := gin.New()
	r.Use(injectAuthContext("uid-1", "user"), middleware.NewAuditMiddleware(f.audit))
	g := r.Group("/api/v1/stack-instances/:id/overrides")
	g.GET("", h.GetOverrides)
	g.GET("/:chartId", h.GetOverride)
	g.PUT("/:chartId", h.SetOverride)
	g.DELETE("/:chartId", h.DeleteOverride)
	f.router = r
	return f
}

// stored returns the stored override of i1, or nil when there is none.
func (f *overrideCtxFixture) stored(t *testing.T, chartID string) *models.ValueOverride {
	t.Helper()
	o, err := f.ov.MockValueOverrideRepository.FindByInstanceAndChart(context.Background(), "i1", chartID)
	if err != nil {
		return nil
	}
	return o
}

func (f *overrideCtxFixture) assertUnchanged(t *testing.T) {
	t.Helper()
	ov := f.stored(t, "cc1")
	require.NotNil(t, ov, "the override of cc1 stays")
	assert.Equal(t, "replicas: 1\n", ov.Values)
	assert.Nil(t, f.stored(t, "cc2"), "no override of cc2 is stored")
}

// TestValueOverrideWrites_CancelledAtWriteStillCommit checks that a client
// that leaves at a value override write cannot stop the write, the answer or
// the audit entry (#656).
func TestValueOverrideWrites_CancelledAtWriteStillCommit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		check      func(t *testing.T, f *overrideCtxFixture, w *httptest.ResponseRecorder)
		name       string
		method     string
		path       string
		body       string
		writeAt    string
		wantStatus int
	}{
		{
			name: "set new", method: http.MethodPut, path: "/api/v1/stack-instances/i1/overrides/cc2",
			body: `{"values":"a: 2\n"}`, writeAt: "Override.Create", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *overrideCtxFixture, _ *httptest.ResponseRecorder) {
				require.NotNil(t, f.stored(t, "cc2"))
				assert.Equal(t, "a: 2\n", f.stored(t, "cc2").Values)
			},
		},
		{
			name: "set existing keeps the ID", method: http.MethodPut, path: "/api/v1/stack-instances/i1/overrides/cc1",
			body: `{"values":"replicas: 3\n"}`, writeAt: "Override.Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *overrideCtxFixture, w *httptest.ResponseRecorder) {
				var resp models.ValueOverride
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "ov-1", resp.ID)
				assert.Equal(t, "replicas: 3\n", f.stored(t, "cc1").Values)
			},
		},
		{
			name: "set empty values removes", method: http.MethodPut, path: "/api/v1/stack-instances/i1/overrides/cc1",
			body: `{"values":"  "}`, writeAt: "Override.Delete", wantStatus: http.StatusNoContent,
			check: func(t *testing.T, f *overrideCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Nil(t, f.stored(t, "cc1"))
			},
		},
		{
			name: "delete", method: http.MethodDelete, path: "/api/v1/stack-instances/i1/overrides/cc1",
			writeAt: "Override.Delete", wantStatus: http.StatusNoContent,
			check: func(t *testing.T, f *overrideCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Nil(t, f.stored(t, "cc1"))
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newOverrideCtxFixture(t, nil)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			cancelFirst := cancelAt("before:"+tt.writeAt, 1, cancel)
			writeCtx := firstCtxErrAt("after:" + tt.writeAt)
			f.hooks.setHook(func(point string, ctx context.Context) {
				cancelFirst(point, ctx)
				writeCtx.record(point, ctx)
			})

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			require.Error(t, req.Context().Err(), "the request was cancelled during the handler")
			writeCtx.assertLive(t)
			tt.check(t, f, w)
			assert.Eventually(t, func() bool { return len(f.audit.snapshot()) == 1 },
				2*time.Second, 10*time.Millisecond, "one audit entry for the write")
		})
	}
}

// overrideReadCases are the reads of the value override API, also the reads
// before a write.
var overrideReadCases = []struct {
	name   string
	method string
	path   string
	body   string
	readAt string
}{
	{name: "list", method: http.MethodGet, path: "/api/v1/stack-instances/i1/overrides", readAt: "Override.ListByInstance"},
	{name: "get", method: http.MethodGet, path: "/api/v1/stack-instances/i1/overrides/cc1", readAt: "Override.FindByInstanceAndChart"},
	{name: "set new at the existing override read", method: http.MethodPut, path: "/api/v1/stack-instances/i1/overrides/cc2", body: `{"values":"a: 2\n"}`, readAt: "Override.FindByInstanceAndChart"},
	{name: "set existing at the override read", method: http.MethodPut, path: "/api/v1/stack-instances/i1/overrides/cc1", body: `{"values":"replicas: 3\n"}`, readAt: "Override.FindByInstanceAndChart"},
	{name: "set empty values at the override read", method: http.MethodPut, path: "/api/v1/stack-instances/i1/overrides/cc1", body: `{"values":""}`, readAt: "Override.FindByInstanceAndChart"},
	{name: "delete at the override read", method: http.MethodDelete, path: "/api/v1/stack-instances/i1/overrides/cc1", readAt: "Override.FindByInstanceAndChart"},
}

// TestValueOverrideRequests_CancelledReadChangesNothing checks that a client
// that leaves at a read gets 499, that the read got the request context and
// that no override and no audit entry is written (#656).
func TestValueOverrideRequests_CancelledReadChangesNothing(t *testing.T) {
	t.Parallel()

	for _, tt := range overrideReadCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newOverrideCtxFixture(t, nil)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			cancelFirst := cancelAt("before:"+tt.readAt, 1, cancel)
			readCtx := firstCtxErrAt("after:" + tt.readAt)
			f.hooks.setHook(func(point string, ctx context.Context) {
				cancelFirst(point, ctx)
				readCtx.record(point, ctx)
			})

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Client closed request"}`, w.Body.String())
			readCtx.assertCancelled(t)
			f.assertUnchanged(t)
			assert.Never(t, func() bool { return len(f.audit.snapshot()) > 0 },
				100*time.Millisecond, 10*time.Millisecond, "no audit entry")
		})
	}
}

// TestValueOverrideRequests_DeadlineAtRead checks that a passed deadline at a
// read answers 500 and writes nothing (#656).
func TestValueOverrideRequests_DeadlineAtRead(t *testing.T) {
	t.Parallel()

	for _, tt := range overrideReadCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newOverrideCtxFixture(t, nil)
			f.hooks.setHook(waitForDeadline("before:" + tt.readAt))

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, deadlineRequest(t, tt.method, tt.path, tt.body))

			assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
			f.assertUnchanged(t)
		})
	}
}

// overrideFindFails fails FindByInstanceAndChart with a database error and
// counts the writes.
type overrideFindFails struct {
	*hookValueOverrideRepo
	writes atomic.Int32
}

func (r *overrideFindFails) FindByInstanceAndChart(context.Context, string, string) (*models.ValueOverride, error) {
	return nil, errors.New("db down")
}

func (r *overrideFindFails) Create(ctx context.Context, v *models.ValueOverride) error {
	r.writes.Add(1)
	return r.hookValueOverrideRepo.Create(ctx, v)
}

func (r *overrideFindFails) Update(ctx context.Context, v *models.ValueOverride) error {
	r.writes.Add(1)
	return r.hookValueOverrideRepo.Update(ctx, v)
}

// TestSetOverride_FailedExistingReadStopsTheWrite checks that a failed read
// of the existing override stops the upsert: 500 and no write. Before, the
// handler fell through to a create.
func TestSetOverride_FailedExistingReadStopsTheWrite(t *testing.T) {
	t.Parallel()

	for _, chartID := range []string{"cc1", "cc2"} {
		chartID := chartID
		t.Run(chartID, func(t *testing.T) {
			t.Parallel()
			repo := &overrideFindFails{}
			f := newOverrideCtxFixture(t, repo)
			repo.hookValueOverrideRepo = f.ov

			w := serve(f.router, http.MethodPut, "/api/v1/stack-instances/i1/overrides/"+chartID, `{"values":"a: 2\n"}`)

			assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
			assert.Zero(t, repo.writes.Load(), "no write after the failed read")
			f.assertUnchanged(t)
		})
	}
}

// instanceOverrideReadRouter returns the read routes of an InstanceHandler
// over hook repositories, with instances i1 and i2 of definition d1 (chart
// cc1) on cluster cl-1, an override of i1 and shared values of cl-1.
func instanceOverrideReadRouter(t *testing.T) (*gin.Engine, *hookInstanceRepo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	inst := newHookInstanceRepo()
	def := &hookDefinitionRepo{MockStackDefinitionRepository: NewMockStackDefinitionRepository(), hooks: inst}
	seedDefinition(t, def.MockStackDefinitionRepository, "d1", "def-1", "uid-1")
	cc := newHookChartConfigRepo(inst)
	require.NoError(t, cc.Create(ctx, &models.ChartConfig{ID: "cc1", StackDefinitionID: "d1", ChartName: "web"}))
	for _, id := range []string{"i1", "i2"} {
		require.NoError(t, inst.MockStackInstanceRepository.Create(ctx, &models.StackInstance{
			ID: id, StackDefinitionID: "d1", Name: id, Namespace: "stack-" + id + "-alice", OwnerID: "uid-1",
			Branch: "master", Status: models.StackStatusRunning, ClusterID: "cl-1",
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}))
	}
	ov := newHookValueOverrideRepo(inst)
	require.NoError(t, ov.MockValueOverrideRepository.Create(ctx, &models.ValueOverride{ID: "ov-1", StackInstanceID: "i1", ChartConfigID: "cc1", Values: "replicas: 2\n"}))
	shared := newHookSharedValuesRepo(inst)
	require.NoError(t, shared.repo.Create(ctx, &models.SharedValues{ID: "sv-1", ClusterID: "cl-1", Name: "base", Values: "a: 1\n"}))
	h := NewInstanceHandler(inst, ov, newHookBranchOverrideRepo(inst), def, cc,
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), NewMockUserRepository(), 0).WithSharedValues(shared)
	r := gin.New()
	r.Use(injectAuthContext("uid-1", "user"))
	g := r.Group("/api/v1/stack-instances")
	g.GET("/compare", h.CompareInstances)
	g.GET("/:id/values", h.ExportAllValues)
	g.GET("/:id/values/:chartId", h.ExportChartValues)
	g.GET("/:id/deploy-preview", h.DeployPreview)
	return r, inst
}

// TestInstanceReads_CancelledAtOverrideRead checks that the values pipeline
// of the instance reads passes the request context to the value override and
// shared values reads (#656): a client that leaves gets 499, and a passed
// deadline gives 500.
func TestInstanceReads_CancelledAtOverrideRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   string
		readAt string
		n      int32
	}{
		{name: "export all values, overrides", path: "/api/v1/stack-instances/i1/values", readAt: "Override.ListByInstance", n: 1},
		{name: "export all values, shared values", path: "/api/v1/stack-instances/i1/values", readAt: "Shared.ListByCluster", n: 1},
		{name: "export chart values, overrides", path: "/api/v1/stack-instances/i1/values/cc1", readAt: "Override.ListByInstance", n: 1},
		{name: "export chart values, shared values", path: "/api/v1/stack-instances/i1/values/cc1", readAt: "Shared.ListByCluster", n: 1},
		{name: "deploy preview, overrides", path: "/api/v1/stack-instances/i1/deploy-preview", readAt: "Override.ListByInstance", n: 1},
		{name: "deploy preview, shared values", path: "/api/v1/stack-instances/i1/deploy-preview", readAt: "Shared.ListByCluster", n: 1},
		{name: "compare, left overrides", path: "/api/v1/stack-instances/compare?left=i1&right=i2", readAt: "Override.ListByInstance", n: 1},
		{name: "compare, right overrides", path: "/api/v1/stack-instances/compare?left=i1&right=i2", readAt: "Override.ListByInstance", n: 2},
		{name: "compare, right shared values", path: "/api/v1/stack-instances/compare?left=i1&right=i2", readAt: "Shared.ListByCluster", n: 2},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name+", live", func(t *testing.T) {
			t.Parallel()
			r, hooks := instanceOverrideReadRouter(t)
			readCtx := newNthCtxErrAt("after:"+tt.readAt, tt.n)
			hooks.setHook(readCtx.record)

			w := serve(r, http.MethodGet, tt.path, "")

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.NoError(t, readCtx.result(t), "the read ran")
		})
		t.Run(tt.name+", cancelled", func(t *testing.T) {
			t.Parallel()
			r, hooks := instanceOverrideReadRouter(t)
			req, cancel := liveRequest(t, http.MethodGet, tt.path, "")
			cancelNth := cancelAt("before:"+tt.readAt, tt.n, cancel)
			readCtx := newNthCtxErrAt("after:"+tt.readAt, tt.n)
			hooks.setHook(func(point string, ctx context.Context) {
				cancelNth(point, ctx)
				readCtx.record(point, ctx)
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Client closed request"}`, w.Body.String())
			assert.ErrorIs(t, readCtx.result(t), context.Canceled, "the read runs with the request context")
		})
		t.Run(tt.name+", deadline", func(t *testing.T) {
			t.Parallel()
			r, hooks := instanceOverrideReadRouter(t)
			var calls atomic.Int32
			hooks.setHook(func(point string, ctx context.Context) {
				if point == "before:"+tt.readAt && calls.Add(1) == tt.n {
					<-ctx.Done()
				}
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, deadlineRequest(t, http.MethodGet, tt.path, ""))

			assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
		})
	}
}

// TestDeploy_CancelledAtOverrideReadChangesNothing checks that a client that
// leaves at the value override or shared values read of the deploy values
// build gets 499 and that nothing changes: the build runs before the first
// write (#656).
func TestDeploy_CancelledAtOverrideReadChangesNothing(t *testing.T) {
	t.Parallel()

	for _, readAt := range []string{"Override.ListByInstance", "Shared.ListByCluster"} {
		readAt := readAt
		t.Run(readAt, func(t *testing.T) {
			t.Parallel()
			f := newWriteCtxFixture(t)
			inst := f.instance(t, "draft-1")
			inst.ClusterID = "cl-1"
			require.NoError(t, f.inst.MockStackInstanceRepository.Update(context.Background(), inst))
			require.NoError(t, f.shared.repo.Create(context.Background(), &models.SharedValues{ID: "sv-1", ClusterID: "cl-1", Name: "base", Values: "a: 1\n"}))
			req, cancel := liveRequest(t, http.MethodPost, "/api/v1/stack-instances/draft-1/deploy", "")
			cancelFirst := cancelAt("before:"+readAt, 1, cancel)
			readCtx := firstCtxErrAt("after:" + readAt)
			f.inst.setHook(func(point string, ctx context.Context) {
				cancelFirst(point, ctx)
				readCtx.record(point, ctx)
			})

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
			readCtx.assertCancelled(t)
			got := f.instance(t, "draft-1")
			assert.Equal(t, models.StackStatusDraft, got.Status)
			assert.Nil(t, got.ExpiresAt, "no TTL expiry is written")
			assert.Empty(t, f.logActions(t, "draft-1"), "no deploy log")
			assert.Never(t, func() bool { return len(f.audit.snapshot()) > 0 },
				100*time.Millisecond, 10*time.Millisecond, "no audit entry")
		})
	}
}

// TestCloneInstance_CancelledAtOverrideCopyStillCommits checks that a clone
// whose client left at the value override copy still copies the override and
// answers 201 (#656).
func TestCloneInstance_CancelledAtOverrideCopyStillCommits(t *testing.T) {
	t.Parallel()
	f := newWriteCtxFixture(t)
	require.NoError(t, f.ov.MockValueOverrideRepository.Create(context.Background(), &models.ValueOverride{
		ID: "ov-src", StackInstanceID: "draft-1", ChartConfigID: "cc1", Values: "replicas: 4\n",
	}))
	req, cancel := liveRequest(t, http.MethodPost, "/api/v1/stack-instances/draft-1/clone", `{"name":"copy-values"}`)
	cancelFirst := cancelAt("before:Override.Create", 1, cancel)
	writeCtx := firstCtxErrAt("after:Override.Create")
	f.inst.setHook(func(point string, ctx context.Context) {
		cancelFirst(point, ctx)
		writeCtx.record(point, ctx)
	})

	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Error(t, req.Context().Err())
	writeCtx.assertLive(t)
	var resp CloneInstanceResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	ovs, err := f.ov.MockValueOverrideRepository.ListByInstance(context.Background(), resp.ID)
	require.NoError(t, err)
	require.Len(t, ovs, 1)
	assert.Equal(t, "replicas: 4\n", ovs[0].Values)
	assert.Eventually(t, func() bool { return len(f.audit.snapshot()) == 1 },
		2*time.Second, 10*time.Millisecond, "one audit entry for the clone")
}

// sharedCtxFixture is a shared values handler over mocks that fail on a done
// context, with the audit middleware (#656).
type sharedCtxFixture struct {
	hooks  *hookInstanceRepo
	shared *hookSharedValuesRepo
	audit  *MockAuditLogRepository
	router *gin.Engine
}

// newSharedCtxFixture seeds sv-1 ("a: 1") of cluster-1.
func newSharedCtxFixture(t *testing.T) *sharedCtxFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &sharedCtxFixture{hooks: newHookInstanceRepo(), audit: NewMockAuditLogRepository()}
	f.shared = newHookSharedValuesRepo(f.hooks)
	require.NoError(t, f.shared.repo.Create(context.Background(), &models.SharedValues{
		ID: "sv-1", ClusterID: "cluster-1", Name: "base", Values: "a: 1\n",
	}))
	h := NewSharedValuesHandler(f.shared, newMockClusterRepoForSV())

	r := gin.New()
	r.Use(injectAuthContext("uid-admin", "admin"), middleware.NewAuditMiddleware(f.audit))
	g := r.Group("/api/v1/clusters/:id/shared-values")
	g.GET("", h.ListSharedValues)
	g.POST("", h.CreateSharedValues)
	g.PUT("/:valueId", h.UpdateSharedValues)
	g.DELETE("/:valueId", h.DeleteSharedValues)
	f.router = r
	return f
}

func (f *sharedCtxFixture) assertUnchanged(t *testing.T) {
	t.Helper()
	sv := f.shared.stored("sv-1")
	require.NotNil(t, sv, "sv-1 stays")
	assert.Equal(t, "a: 1\n", sv.Values)
	f.shared.mu.Lock()
	defer f.shared.mu.Unlock()
	assert.Len(t, f.shared.repo.items, 1, "no shared values are created")
}

// TestSharedValuesWrites_CancelledAtWriteStillCommit checks that a client
// that leaves at a shared values write cannot stop the write, the answer or
// the audit entry (#656).
func TestSharedValuesWrites_CancelledAtWriteStillCommit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		check      func(t *testing.T, f *sharedCtxFixture, w *httptest.ResponseRecorder)
		name       string
		method     string
		path       string
		body       string
		writeAt    string
		wantStatus int
	}{
		{
			name: "create", method: http.MethodPost, path: "/api/v1/clusters/cluster-1/shared-values",
			body: `{"name":"extra","values":"b: 2\n"}`, writeAt: "Shared.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *sharedCtxFixture, w *httptest.ResponseRecorder) {
				var resp models.SharedValues
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				sv := f.shared.stored(resp.ID)
				require.NotNil(t, sv)
				assert.Equal(t, "b: 2\n", sv.Values)
			},
		},
		{
			name: "update", method: http.MethodPut, path: "/api/v1/clusters/cluster-1/shared-values/sv-1",
			body: `{"name":"base","values":"a: 3\n"}`, writeAt: "Shared.Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *sharedCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, "a: 3\n", f.shared.stored("sv-1").Values)
			},
		},
		{
			name: "delete", method: http.MethodDelete, path: "/api/v1/clusters/cluster-1/shared-values/sv-1",
			writeAt: "Shared.Delete", wantStatus: http.StatusNoContent,
			check: func(t *testing.T, f *sharedCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Nil(t, f.shared.stored("sv-1"))
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSharedCtxFixture(t)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			cancelFirst := cancelAt("before:"+tt.writeAt, 1, cancel)
			writeCtx := firstCtxErrAt("after:" + tt.writeAt)
			f.hooks.setHook(func(point string, ctx context.Context) {
				cancelFirst(point, ctx)
				writeCtx.record(point, ctx)
			})

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			require.Error(t, req.Context().Err(), "the request was cancelled during the handler")
			writeCtx.assertLive(t)
			tt.check(t, f, w)
			assert.Eventually(t, func() bool { return len(f.audit.snapshot()) == 1 },
				2*time.Second, 10*time.Millisecond, "one audit entry for the write")
		})
	}
}

// sharedReadCases are the reads of the shared values handler, also the reads
// before a write.
var sharedReadCases = []struct {
	name   string
	method string
	path   string
	body   string
	readAt string
}{
	{name: "list", method: http.MethodGet, path: "/api/v1/clusters/cluster-1/shared-values", readAt: "Shared.ListByCluster"},
	{name: "update at the read", method: http.MethodPut, path: "/api/v1/clusters/cluster-1/shared-values/sv-1", body: `{"name":"base","values":"a: 3\n"}`, readAt: "Shared.FindByClusterAndID"},
	{name: "delete at the read", method: http.MethodDelete, path: "/api/v1/clusters/cluster-1/shared-values/sv-1", readAt: "Shared.FindByClusterAndID"},
}

// TestSharedValuesRequests_CancelledReadChangesNothing checks that a client
// that leaves at a read gets 499, that the read got the request context and
// that nothing and no audit entry is written (#656).
func TestSharedValuesRequests_CancelledReadChangesNothing(t *testing.T) {
	t.Parallel()

	for _, tt := range sharedReadCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSharedCtxFixture(t)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			cancelFirst := cancelAt("before:"+tt.readAt, 1, cancel)
			readCtx := firstCtxErrAt("after:" + tt.readAt)
			f.hooks.setHook(func(point string, ctx context.Context) {
				cancelFirst(point, ctx)
				readCtx.record(point, ctx)
			})

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Client closed request"}`, w.Body.String())
			readCtx.assertCancelled(t)
			f.assertUnchanged(t)
			assert.Never(t, func() bool { return len(f.audit.snapshot()) > 0 },
				100*time.Millisecond, 10*time.Millisecond, "no audit entry")
		})
	}
}

// TestSharedValuesRequests_DeadlineAtRead checks that a passed deadline at a
// read answers 500 and writes nothing (#656).
func TestSharedValuesRequests_DeadlineAtRead(t *testing.T) {
	t.Parallel()

	for _, tt := range sharedReadCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSharedCtxFixture(t)
			f.hooks.setHook(waitForDeadline("before:" + tt.readAt))

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, deadlineRequest(t, tt.method, tt.path, tt.body))

			assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
			f.assertUnchanged(t)
		})
	}
}
