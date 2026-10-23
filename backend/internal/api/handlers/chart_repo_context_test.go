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
	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nthCtxErrAt records the context error of call number n of a hook point.
type nthCtxErrAt struct {
	point string
	mu    sync.Mutex
	err   error
	calls int32
	n     int32
	seen  bool
}

func newNthCtxErrAt(point string, n int32) *nthCtxErrAt { return &nthCtxErrAt{point: point, n: n} }

func (r *nthCtxErrAt) record(point string, ctx context.Context) {
	if point != r.point {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.calls == r.n {
		r.seen, r.err = true, ctx.Err()
	}
}

func (r *nthCtxErrAt) result(t *testing.T) error {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.True(t, r.seen, "%s call %d ran", r.point, r.n)
	return r.err
}

// waitForDeadline returns a hook that blocks at point until ctx is done, so
// the call at point sees a passed deadline.
func waitForDeadline(point string) func(string, context.Context) {
	return func(p string, ctx context.Context) {
		if p == point {
			<-ctx.Done()
		}
	}
}

// deadlineRequest returns a request whose context ends with a deadline.
func deadlineRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)
	req, _ := liveRequest(t, method, path, body)
	return req.WithContext(ctx)
}

// chartOf returns the stored chart, or nil when it does not exist.
func (f *defCtxFixture) chartOf(t *testing.T, id string) *models.ChartConfig {
	t.Helper()
	c, err := f.defCharts.MockChartConfigRepository.FindByID(context.Background(), id)
	if err != nil {
		return nil
	}
	return c
}

func (f *defCtxFixture) chartsOf(t *testing.T, defID string) []models.ChartConfig {
	t.Helper()
	charts, err := f.defCharts.MockChartConfigRepository.ListByDefinition(context.Background(), defID)
	require.NoError(t, err)
	return charts
}

// TestChartConfigWrites_CancelledAtWriteStillCommit checks that a client that
// leaves at a chart write cannot stop the write, the 2xx answer or the audit
// entry (#656). The chart mock fails on a done context.
func TestChartConfigWrites_CancelledAtWriteStillCommit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		check      func(t *testing.T, f *defCtxFixture, w *httptest.ResponseRecorder)
		name       string
		method     string
		path       string
		body       string
		cancelAt   string
		wantStatus int
	}{
		{
			name: "add chart", method: http.MethodPost, path: "/api/v1/stack-definitions/d2/charts",
			body: `{"chart_name":"cache","repository_url":"oci://charts/cache"}`, cancelAt: "before:Chart.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Len(t, f.chartsOf(t, "d2"), 2)
			},
		},
		{
			name: "update chart", method: http.MethodPut, path: "/api/v1/stack-definitions/d2/charts/c2",
			body: `{"default_values":"replicas: 3"}`, cancelAt: "before:Chart.Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, "replicas: 3", f.chartOf(t, "c2").DefaultValues)
			},
		},
		{
			name: "delete chart", method: http.MethodDelete, path: "/api/v1/stack-definitions/d2/charts/c2",
			cancelAt: "before:Chart.Delete", wantStatus: http.StatusNoContent,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Nil(t, f.chartOf(t, "c2"))
			},
		},
		{
			name: "upgrade at the chart write", method: http.MethodPost, path: "/api/v1/stack-definitions/d1/upgrade",
			cancelAt: "before:Chart.Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, "1.1.0", f.definition(t, "d1").SourceTemplateVersion)
				assert.Equal(t, "replicas: 1", f.chartOf(t, "c1").DefaultValues, "the chart takes the template values")
			},
		},
		{
			name: "upgrade before the read-back", method: http.MethodPost, path: "/api/v1/stack-definitions/d1/upgrade",
			cancelAt: "after:Definition.Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *defCtxFixture, w *httptest.ResponseRecorder) {
				var resp DefinitionWithChartsResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Len(t, resp.Charts, 1, "the answer has the charts")
			},
		},
		{
			name: "import", method: http.MethodPost, path: "/api/v1/stack-definitions/import",
			body:     `{"schema_version":"1.0","definition":{"name":"imported"},"charts":[{"chart_name":"web","repository_url":"oci://charts/web"},{"chart_name":"api","repository_url":"oci://charts/api"}]}`,
			cancelAt: "before:Chart.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				defs := f.definitionsNamed(t, "imported")
				require.Len(t, defs, 1)
				assert.Len(t, f.chartsOf(t, defs[0].ID), 2, "both charts are stored")
			},
		},
		{
			name: "instantiate", method: http.MethodPost, path: "/api/v1/templates/t1/instantiate",
			body: `{"name":"from-template"}`, cancelAt: "before:Chart.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				defs := f.definitionsNamed(t, "from-template")
				require.Len(t, defs, 1)
				assert.Len(t, f.chartsOf(t, defs[0].ID), 1)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newDefCtxFixture(t)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			f.hooks.setHook(cancelAt(tt.cancelAt, 1, cancel))

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			require.Error(t, req.Context().Err(), "the request was cancelled during the handler")
			tt.check(t, f, w)
			assert.Eventually(t, func() bool { return len(f.audit.snapshot()) == 1 },
				2*time.Second, 10*time.Millisecond, "one audit entry for the write")
		})
	}
}

// chartReadCases are the chart reads of the definition handlers, also the
// reads before a chart write.
var chartReadCases = []struct {
	name     string
	method   string
	path     string
	body     string
	readAt   string
	readOnly bool
}{
	{name: "get definition", method: http.MethodGet, path: "/api/v1/stack-definitions/d1", readAt: "Chart.ListByDefinition", readOnly: true},
	{name: "export", method: http.MethodGet, path: "/api/v1/stack-definitions/d1/export", readAt: "Chart.ListByDefinition", readOnly: true},
	{name: "check-upgrade", method: http.MethodGet, path: "/api/v1/stack-definitions/d1/check-upgrade", readAt: "Chart.ListByDefinition", readOnly: true},
	{name: "get chart", method: http.MethodGet, path: "/api/v1/stack-definitions/d2/charts/c2", readAt: "Chart.FindByID", readOnly: true},
	{name: "upgrade at the chart list", method: http.MethodPost, path: "/api/v1/stack-definitions/d1/upgrade", readAt: "Chart.ListByDefinition"},
	{
		name: "update chart at the chart read", method: http.MethodPut, path: "/api/v1/stack-definitions/d2/charts/c2",
		body: `{"default_values":"replicas: 3"}`, readAt: "Chart.FindByID",
	},
	{name: "delete chart at the chart read", method: http.MethodDelete, path: "/api/v1/stack-definitions/d2/charts/c2", readAt: "Chart.FindByID"},
	{
		name: "add chart at the definition read", method: http.MethodPost, path: "/api/v1/stack-definitions/d2/charts",
		body: `{"chart_name":"cache","repository_url":"oci://charts/cache"}`, readAt: "Definition.FindByID",
	},
}

// assertDefChartsUnchanged checks the chart and definition seed of
// newDefCtxFixture.
func assertDefChartsUnchanged(t *testing.T, f *defCtxFixture) {
	t.Helper()
	assert.Equal(t, "1.0.0", f.definition(t, "d1").SourceTemplateVersion)
	require.Len(t, f.chartsOf(t, "d2"), 1)
	c2 := f.chartOf(t, "c2")
	require.NotNil(t, c2)
	assert.Empty(t, c2.DefaultValues)
}

// TestChartConfigRequests_CancelledReadChangesNothing checks that a client
// that leaves at a chart read gets 499, that the read got the request context
// and that nothing changes, also for a read before a chart write (#656).
func TestChartConfigRequests_CancelledReadChangesNothing(t *testing.T) {
	t.Parallel()

	for _, tt := range chartReadCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newDefCtxFixture(t)
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
			assertDefChartsUnchanged(t, f)
			assert.Never(t, func() bool { return len(f.audit.snapshot()) > 0 },
				100*time.Millisecond, 10*time.Millisecond, "no audit entry")
		})
	}
}

// TestChartConfigRequests_DeadlineAtChartRead checks that a passed deadline
// at a chart read answers 500, not 499, and changes nothing (#656).
func TestChartConfigRequests_DeadlineAtChartRead(t *testing.T) {
	t.Parallel()

	for _, tt := range chartReadCases {
		tt := tt
		if !strings.HasPrefix(tt.readAt, "Chart.") {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newDefCtxFixture(t)
			f.hooks.setHook(waitForDeadline("before:" + tt.readAt))

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, deadlineRequest(t, tt.method, tt.path, tt.body))

			assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
			assertDefChartsUnchanged(t, f)
		})
	}
}

// TestListDefinitions_ChartCountUsesRequestContext checks that the chart
// count of the definition list runs with the request context: a client that
// leaves at the count gets no chart counts, and the list does not fail.
func TestListDefinitions_ChartCountUsesRequestContext(t *testing.T) {
	t.Parallel()

	t.Run("live", func(t *testing.T) {
		t.Parallel()
		f := newDefCtxFixture(t)
		w := serve(f.router, http.MethodGet, "/api/v1/stack-definitions", "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), `"chart_count":1`)
	})

	t.Run("cancelled at the count", func(t *testing.T) {
		t.Parallel()
		f := newDefCtxFixture(t)
		req, cancel := liveRequest(t, http.MethodGet, "/api/v1/stack-definitions", "")
		cancelFirst := cancelAt("before:Chart.CountByDefinitionIDs", 1, cancel)
		readCtx := firstCtxErrAt("after:Chart.CountByDefinitionIDs")
		f.hooks.setHook(func(point string, ctx context.Context) {
			cancelFirst(point, ctx)
			readCtx.record(point, ctx)
		})
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)

		readCtx.assertCancelled(t)
		assert.NotContains(t, w.Body.String(), `"chart_count"`)
	})
}

// branchCtxFixture is a branch override handler over mocks that fail on a
// done context, with the audit middleware (#656).
type branchCtxFixture struct {
	hooks    *hookInstanceRepo
	charts   *hookChartConfigRepo
	branches *hookBranchOverrideRepo
	audit    *MockAuditLogRepository
	router   *gin.Engine
}

// newBranchCtxFixture seeds definition d1 with the charts cc1 and cc2,
// instance i1 of uid-1 and the override of i1 / cc1 (bo-1, "feature").
func newBranchCtxFixture(t *testing.T, branches *hookBranchOverrideRepo) *branchCtxFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &branchCtxFixture{hooks: newHookInstanceRepo(), audit: NewMockAuditLogRepository()}
	f.charts = newHookChartConfigRepo(f.hooks)
	if branches == nil {
		branches = newHookBranchOverrideRepo(f.hooks)
	}
	branches.hooks = f.hooks
	f.branches = branches
	ctx := context.Background()
	require.NoError(t, f.charts.Create(ctx, &models.ChartConfig{ID: "cc1", StackDefinitionID: "d1", ChartName: "web"}))
	require.NoError(t, f.charts.Create(ctx, &models.ChartConfig{ID: "cc2", StackDefinitionID: "d1", ChartName: "api"}))
	seedInstance(t, f.hooks.MockStackInstanceRepository, "i1", "one", "d1", "uid-1", models.StackStatusRunning)
	require.NoError(t, f.branches.MockChartBranchOverrideRepository.Set(ctx, &models.ChartBranchOverride{
		ID: "bo-1", StackInstanceID: "i1", ChartConfigID: "cc1", Branch: "feature",
	}))
	h := NewBranchOverrideHandler(f.branches, f.hooks, f.charts)

	r := gin.New()
	r.Use(injectAuthContext("uid-1", "user"), middleware.NewAuditMiddleware(f.audit))
	g := r.Group("/api/v1/stack-instances/:id/branches")
	g.GET("", h.ListBranchOverrides)
	g.GET("/:chartId", h.GetBranchOverride)
	g.PUT("/:chartId", h.SetBranchOverride)
	g.DELETE("/:chartId", h.DeleteBranchOverride)
	f.router = r
	return f
}

// stored returns the stored override, or nil when there is none.
func (f *branchCtxFixture) stored(t *testing.T, chartID string) *models.ChartBranchOverride {
	t.Helper()
	o, err := f.branches.MockChartBranchOverrideRepository.Get(context.Background(), "i1", chartID)
	if err != nil {
		return nil
	}
	return o
}

func (f *branchCtxFixture) assertUnchanged(t *testing.T) {
	t.Helper()
	bo := f.stored(t, "cc1")
	require.NotNil(t, bo, "the override of cc1 stays")
	assert.Equal(t, "feature", bo.Branch)
	assert.Nil(t, f.stored(t, "cc2"), "no override of cc2 is stored")
}

// TestBranchOverrideWrites_CancelledAtWriteStillCommit checks that a client
// that leaves at a branch override write cannot stop the write, the answer
// or the audit entry (#656).
func TestBranchOverrideWrites_CancelledAtWriteStillCommit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		check      func(t *testing.T, f *branchCtxFixture, w *httptest.ResponseRecorder)
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{
			name: "set new", method: http.MethodPut, path: "/api/v1/stack-instances/i1/branches/cc2",
			body: `{"branch":"hotfix"}`, wantStatus: http.StatusOK,
			check: func(t *testing.T, f *branchCtxFixture, _ *httptest.ResponseRecorder) {
				require.NotNil(t, f.stored(t, "cc2"))
				assert.Equal(t, "hotfix", f.stored(t, "cc2").Branch)
			},
		},
		{
			name: "set existing keeps the ID", method: http.MethodPut, path: "/api/v1/stack-instances/i1/branches/cc1",
			body: `{"branch":"changed"}`, wantStatus: http.StatusOK,
			check: func(t *testing.T, f *branchCtxFixture, w *httptest.ResponseRecorder) {
				var resp models.ChartBranchOverride
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "bo-1", resp.ID)
				assert.Equal(t, "changed", f.stored(t, "cc1").Branch)
			},
		},
		{
			name: "delete", method: http.MethodDelete, path: "/api/v1/stack-instances/i1/branches/cc1",
			wantStatus: http.StatusNoContent,
			check: func(t *testing.T, f *branchCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Nil(t, f.stored(t, "cc1"))
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newBranchCtxFixture(t, nil)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			writeAt := "before:Branch.Set"
			if tt.method == http.MethodDelete {
				writeAt = "before:Branch.Delete"
			}
			f.hooks.setHook(cancelAt(writeAt, 1, cancel))

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			require.Error(t, req.Context().Err(), "the request was cancelled during the handler")
			tt.check(t, f, w)
			assert.Eventually(t, func() bool { return len(f.audit.snapshot()) == 1 },
				2*time.Second, 10*time.Millisecond, "one audit entry for the write")
		})
	}
}

// branchReadCases are the reads of the branch override handler, also the
// reads before a write.
var branchReadCases = []struct {
	name   string
	method string
	path   string
	body   string
	readAt string
}{
	{name: "list", method: http.MethodGet, path: "/api/v1/stack-instances/i1/branches", readAt: "Branch.List"},
	{name: "get", method: http.MethodGet, path: "/api/v1/stack-instances/i1/branches/cc1", readAt: "Branch.Get"},
	{name: "get without override, chart check", method: http.MethodGet, path: "/api/v1/stack-instances/i1/branches/cc2", readAt: "Chart.FindByID"},
	{name: "set at the chart check", method: http.MethodPut, path: "/api/v1/stack-instances/i1/branches/cc2", body: `{"branch":"hotfix"}`, readAt: "Chart.FindByID"},
	{name: "set at the existing override read", method: http.MethodPut, path: "/api/v1/stack-instances/i1/branches/cc2", body: `{"branch":"hotfix"}`, readAt: "Branch.Get"},
	{name: "set existing at the override read", method: http.MethodPut, path: "/api/v1/stack-instances/i1/branches/cc1", body: `{"branch":"changed"}`, readAt: "Branch.Get"},
	{name: "delete without override, chart check", method: http.MethodDelete, path: "/api/v1/stack-instances/i1/branches/cc2", readAt: "Chart.FindByID"},
}

// TestBranchOverrideRequests_CancelledReadChangesNothing checks that a client
// that leaves at a read gets 499, that the read got the request context and
// that no override and no audit entry is written (#656).
func TestBranchOverrideRequests_CancelledReadChangesNothing(t *testing.T) {
	t.Parallel()

	for _, tt := range branchReadCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newBranchCtxFixture(t, nil)
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

// TestBranchOverrideRequests_DeadlineAtRead checks that a passed deadline at
// a read answers 500 and writes nothing (#656).
func TestBranchOverrideRequests_DeadlineAtRead(t *testing.T) {
	t.Parallel()

	for _, tt := range branchReadCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newBranchCtxFixture(t, nil)
			f.hooks.setHook(waitForDeadline("before:" + tt.readAt))

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, deadlineRequest(t, tt.method, tt.path, tt.body))

			assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
			f.assertUnchanged(t)
		})
	}
}

// branchGetFails fails Get with a database error.
type branchGetFails struct {
	*hookBranchOverrideRepo
	sets atomic.Int32
}

func (r *branchGetFails) Get(context.Context, string, string) (*models.ChartBranchOverride, error) {
	return nil, errors.New("db down")
}

func (r *branchGetFails) Set(ctx context.Context, o *models.ChartBranchOverride) error {
	r.sets.Add(1)
	return r.hookBranchOverrideRepo.Set(ctx, o)
}

// TestSetBranchOverride_FailedExistingReadStopsTheWrite checks that a failed
// read of the existing override stops the upsert: 500 and no write.
func TestSetBranchOverride_FailedExistingReadStopsTheWrite(t *testing.T) {
	t.Parallel()
	repo := &branchGetFails{hookBranchOverrideRepo: newHookBranchOverrideRepo(nil)}
	f := newBranchCtxFixture(t, repo.hookBranchOverrideRepo)
	h := NewBranchOverrideHandler(repo, f.hooks, f.charts)
	r := gin.New()
	r.Use(injectAuthContext("uid-1", "user"))
	r.PUT("/api/v1/stack-instances/:id/branches/:chartId", h.SetBranchOverride)

	w := serve(r, http.MethodPut, "/api/v1/stack-instances/i1/branches/cc2", `{"branch":"hotfix"}`)

	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	assert.Zero(t, repo.sets.Load(), "no write after the failed read")
	f.assertUnchanged(t)
}

// instanceChartReadRouter returns the read routes of an InstanceHandler over
// hook repositories, with instances i1 and i2 of definition d1 (chart cc1).
func instanceChartReadRouter(t *testing.T) (*gin.Engine, *hookInstanceRepo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	inst := newHookInstanceRepo()
	def := &hookDefinitionRepo{MockStackDefinitionRepository: NewMockStackDefinitionRepository(), hooks: inst}
	seedDefinition(t, def.MockStackDefinitionRepository, "d1", "def-1", "uid-1")
	cc := newHookChartConfigRepo(inst)
	require.NoError(t, cc.Create(context.Background(), &models.ChartConfig{ID: "cc1", StackDefinitionID: "d1", ChartName: "web"}))
	bo := newHookBranchOverrideRepo(inst)
	seedInstance(t, inst.MockStackInstanceRepository, "i1", "one", "d1", "uid-1", models.StackStatusRunning)
	seedInstance(t, inst.MockStackInstanceRepository, "i2", "two", "d1", "uid-1", models.StackStatusRunning)
	h := NewInstanceHandler(inst, NewMockValueOverrideRepository(), bo, def, cc,
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), NewMockUserRepository(), 0)
	r := gin.New()
	r.Use(injectAuthContext("uid-1", "user"))
	g := r.Group("/api/v1/stack-instances")
	g.GET("/compare", h.CompareInstances)
	g.GET("/:id/values", h.ExportAllValues)
	g.GET("/:id/values/:chartId", h.ExportChartValues)
	g.GET("/:id/deploy-preview", h.DeployPreview)
	return r, inst
}

// instanceChartReadCases are the chart and branch override reads of the
// instance read endpoints. n is the call number of readAt to end.
var instanceChartReadCases = []struct {
	name   string
	path   string
	readAt string
	n      int32
}{
	{name: "export all values", path: "/api/v1/stack-instances/i1/values", readAt: "Chart.ListByDefinition", n: 1},
	{name: "export all values, branch overrides", path: "/api/v1/stack-instances/i1/values", readAt: "Branch.List", n: 1},
	{name: "export chart values", path: "/api/v1/stack-instances/i1/values/cc1", readAt: "Chart.FindByID", n: 1},
	{name: "export chart values, branch overrides", path: "/api/v1/stack-instances/i1/values/cc1", readAt: "Branch.List", n: 1},
	{name: "deploy preview", path: "/api/v1/stack-instances/i1/deploy-preview", readAt: "Chart.ListByDefinition", n: 1},
	{name: "deploy preview, branch overrides", path: "/api/v1/stack-instances/i1/deploy-preview", readAt: "Branch.List", n: 1},
	{name: "compare, left charts", path: "/api/v1/stack-instances/compare?left=i1&right=i2", readAt: "Chart.ListByDefinition", n: 1},
	{name: "compare, right charts", path: "/api/v1/stack-instances/compare?left=i1&right=i2", readAt: "Chart.ListByDefinition", n: 2},
	{name: "compare, left branch overrides", path: "/api/v1/stack-instances/compare?left=i1&right=i2", readAt: "Branch.List", n: 1},
}

// TestInstanceReads_CancelledAtChartRead checks that the instance reads pass
// the request context to the chart and branch override reads (#656): a
// client that leaves gets 499, and a passed deadline gives 500.
func TestInstanceReads_CancelledAtChartRead(t *testing.T) {
	t.Parallel()

	for _, tt := range instanceChartReadCases {
		tt := tt
		t.Run(tt.name+", cancelled", func(t *testing.T) {
			t.Parallel()
			r, hooks := instanceChartReadRouter(t)
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
			r, hooks := instanceChartReadRouter(t)
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

// TestInstanceWrites_CancelledAtChartReadChangesNothing checks that a client
// that leaves at the chart or branch override read before the first write of
// an instance operation gets 499 and that nothing changes: no status change,
// no deploy log, no TTL expiry (#656).
func TestInstanceWrites_CancelledAtChartReadChangesNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   string
		id     string
		status string
		readAt string
	}{
		{name: "deploy at the chart list", path: "/api/v1/stack-instances/draft-1/deploy", id: "draft-1", status: models.StackStatusDraft, readAt: "Chart.ListByDefinition"},
		{name: "deploy at the branch overrides", path: "/api/v1/stack-instances/draft-1/deploy", id: "draft-1", status: models.StackStatusDraft, readAt: "Branch.List"},
		{name: "stop", path: "/api/v1/stack-instances/run-1/stop", id: "run-1", status: models.StackStatusRunning, readAt: "Chart.ListByDefinition"},
		{name: "clean", path: "/api/v1/stack-instances/stopped-1/clean", id: "stopped-1", status: models.StackStatusStopped, readAt: "Chart.ListByDefinition"},
		{name: "rollback", path: "/api/v1/stack-instances/run-1/rollback", id: "run-1", status: models.StackStatusRunning, readAt: "Chart.ListByDefinition"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWriteCtxFixture(t)
			req, cancel := liveRequest(t, http.MethodPost, tt.path, "")
			cancelFirst := cancelAt("before:"+tt.readAt, 1, cancel)
			readCtx := firstCtxErrAt("after:" + tt.readAt)
			f.inst.setHook(func(point string, ctx context.Context) {
				cancelFirst(point, ctx)
				readCtx.record(point, ctx)
			})

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
			readCtx.assertCancelled(t)
			got := f.instance(t, tt.id)
			assert.Equal(t, tt.status, got.Status)
			assert.Nil(t, got.ExpiresAt, "no TTL expiry is written")
			assert.Empty(t, f.logActions(t, tt.id), "no deploy log")
			assert.Never(t, func() bool { return len(f.audit.snapshot()) > 0 },
				100*time.Millisecond, 10*time.Millisecond, "no audit entry")
		})
	}

	// A bulk request answers 200 with a result for each instance.
	bulk := []struct {
		name string
		path string
		id   string
	}{
		{name: "bulk deploy", path: "/api/v1/stack-instances/bulk/deploy", id: "draft-1"},
		{name: "bulk stop", path: "/api/v1/stack-instances/bulk/stop", id: "run-1"},
		{name: "bulk clean", path: "/api/v1/stack-instances/bulk/clean", id: "stopped-1"},
	}
	for _, tt := range bulk {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWriteCtxFixture(t)
			req, cancel := liveRequest(t, http.MethodPost, tt.path, `{"instance_ids":["`+tt.id+`"]}`)
			cancelFirst := cancelAt("before:Chart.ListByDefinition", 1, cancel)
			readCtx := firstCtxErrAt("after:Chart.ListByDefinition")
			f.inst.setHook(func(point string, ctx context.Context) {
				cancelFirst(point, ctx)
				readCtx.record(point, ctx)
			})

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			readCtx.assertCancelled(t)
			results := bulkResults(t, w)
			require.Len(t, results, 1)
			assert.Equal(t, "error", results[0].Status)
			assert.Equal(t, "request cancelled", results[0].Error)
			assert.Empty(t, f.logActions(t, tt.id), "no deploy log")
		})
		t.Run(tt.name+", database error", func(t *testing.T) {
			t.Parallel()
			f := newWriteCtxFixture(t)
			f.charts.SetError(errors.New("db down"))

			w := serve(f.router, http.MethodPost, tt.path, `{"instance_ids":["`+tt.id+`"]}`)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			results := bulkResults(t, w)
			require.Len(t, results, 1)
			assert.Equal(t, "error", results[0].Status)
			assert.Equal(t, "failed to list chart configs", results[0].Error, "the cause is not exposed")
			assert.Empty(t, f.logActions(t, tt.id), "no deploy log")
		})
	}
}

// TestRollbackToTarget_CancelledAtDriftBuildChangesNothing checks that a
// client that leaves during the values build of the drift check gets 499 and
// that no rollback starts: the build is the last read before the first write
// (#656). Before, a failed build only skipped the drift warning.
func TestRollbackToTarget_CancelledAtDriftBuildChangesNothing(t *testing.T) {
	t.Parallel()
	f := newWriteCtxFixture(t)
	require.NoError(t, f.logs.Create(context.Background(), &models.DeploymentLog{
		ID: "log-ok", StackInstanceID: "run-1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess,
		ValuesSnapshot: `{"web":"replicas: 1\n"}`, StartedAt: time.Now().UTC().Add(-time.Hour),
	}))
	req, cancel := liveRequest(t, http.MethodPost, "/api/v1/stack-instances/run-1/rollback", `{"target_log_id":"log-ok"}`)
	cancelFirst := cancelAt("before:Branch.List", 1, cancel)
	readCtx := firstCtxErrAt("after:Branch.List")
	f.inst.setHook(func(point string, ctx context.Context) {
		cancelFirst(point, ctx)
		readCtx.record(point, ctx)
	})

	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)

	assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
	readCtx.assertCancelled(t)
	assert.Equal(t, []string{models.DeployActionDeploy}, f.logActions(t, "run-1"), "no rollback log")
	assert.Equal(t, models.StackStatusRunning, f.instance(t, "run-1").Status)
	assert.Never(t, func() bool { return len(f.audit.snapshot()) > 0 },
		100*time.Millisecond, 10*time.Millisecond, "no audit entry")
}

// TestRollbackToTarget_FailedDriftBuildStillRollsBack checks that a database
// error in the drift values build only skips the drift warning: the drift
// check is not a guard, so the rollback starts.
func TestRollbackToTarget_FailedDriftBuildStillRollsBack(t *testing.T) {
	t.Parallel()
	f := newWriteCtxFixture(t)
	require.NoError(t, f.logs.Create(context.Background(), &models.DeploymentLog{
		ID: "log-ok", StackInstanceID: "run-1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess,
		ValuesSnapshot: `{"web":"replicas: 1\n"}`, StartedAt: time.Now().UTC().Add(-time.Hour),
	}))
	f.branches.SetError(errors.New("db down"))

	w := serve(f.router, http.MethodPost, "/api/v1/stack-instances/run-1/rollback", `{"target_log_id":"log-ok"}`)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotContains(t, resp, "values_drift", "no drift result without a values build")
	assert.NotContains(t, resp, "warning")
	assert.ElementsMatch(t, []string{models.DeployActionDeploy, models.DeployActionRollback}, f.logActions(t, "run-1"), "the mock lists in map order")
}

// TestCloneInstance_CancelledAtBranchCopyStillCommits checks that a clone
// whose client left at the branch override copy still copies the override
// and answers 201 (#656).
func TestCloneInstance_CancelledAtBranchCopyStillCommits(t *testing.T) {
	t.Parallel()
	f := newWriteCtxFixture(t)
	require.NoError(t, f.branches.MockChartBranchOverrideRepository.Set(context.Background(), &models.ChartBranchOverride{
		ID: "bo-src", StackInstanceID: "draft-1", ChartConfigID: "cc1", Branch: "feature",
	}))
	req, cancel := liveRequest(t, http.MethodPost, "/api/v1/stack-instances/draft-1/clone", `{"name":"copy-branch"}`)
	f.inst.setHook(cancelAt("before:Branch.Set", 1, cancel))

	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Error(t, req.Context().Err())
	var resp CloneInstanceResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	bos, err := f.branches.MockChartBranchOverrideRepository.List(context.Background(), resp.ID)
	require.NoError(t, err)
	require.Len(t, bos, 1)
	assert.Equal(t, "feature", bos[0].Branch)
	assert.Eventually(t, func() bool { return len(f.audit.snapshot()) == 1 },
		2*time.Second, 10*time.Millisecond, "one audit entry for the clone")
}

// TestQuickDeploy_CancelledAtChartWritesStillDeploys checks that a quick
// deploy whose client left at the chart create or at the branch override
// write still stores them and starts the deploy (#656).
func TestQuickDeploy_CancelledAtChartWritesStillDeploys(t *testing.T) {
	t.Parallel()

	for _, cancelPoint := range []string{"before:Chart.Create", "before:Branch.Set"} {
		cancelPoint := cancelPoint
		t.Run(cancelPoint, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
			tmplRepo := NewMockStackTemplateRepository()
			tmplChartRepo := NewMockTemplateChartConfigRepository()
			defRepo := NewMockStackDefinitionRepository()
			instRepo := newHookInstanceRepo()
			ccRepo := newHookChartConfigRepo(instRepo)
			boRepo := newHookBranchOverrideRepo(instRepo)
			ovRepo := NewMockValueOverrideRepository()
			logRepo := NewMockDeploymentLogRepository()
			audit := NewMockAuditLogRepository()
			seedTemplate(t, tmplRepo, "t1", "My Template", "owner-1", true)
			require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
				ID: "tc1", StackTemplateID: "t1", ChartName: "nginx", RepositoryURL: "oci://example.com/charts/nginx", DeployOrder: 1,
			}))
			mgr := newTestManager(instRepo, logRepo)
			t.Cleanup(mgr.Shutdown)
			h, err := NewQuickDeployHandler(
				tmplRepo, tmplChartRepo, defRepo, ccRepo, instRepo, boRepo, ovRepo, helm.NewValuesGenerator(),
				mgr, NewMockUserRepository(), logRepo, audit,
				&MockBroadcastSender{}, cluster.NewRegistryForTest("test-cluster", nil, &noopHelmExecutor{}), nil, 0,
				&mockHandlerTxRunner{repos: database.TxRepos{
					StackDefinition: defRepo, ChartConfig: ccRepo, StackInstance: instRepo,
					ValueOverride: ovRepo, BranchOverride: boRepo,
				}},
			)
			require.NoError(t, err)
			h.WithTemplateVersions(newWorkingCopyReleaseRepo(tmplRepo, tmplChartRepo)).WithNotifier(&followerCtxNotifier{})
			r := gin.New()
			r.Use(injectAuthContext("uid-1", "user"))
			r.POST("/api/v1/templates/:id/quick-deploy", h.QuickDeploy)

			req, cancel := liveRequest(t, http.MethodPost, "/api/v1/templates/t1/quick-deploy",
				`{"instance_name":"qd-charts","branch_overrides":{"tc1":"feature"}}`)
			instRepo.setHook(cancelAt(cancelPoint, 1, cancel))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
			require.Error(t, req.Context().Err())
			var resp quickDeployResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.NotEmpty(t, resp.LogID, "the deploy started")
			charts, err := ccRepo.MockChartConfigRepository.ListByDefinition(context.Background(), resp.Instance.StackDefinitionID)
			require.NoError(t, err)
			assert.Len(t, charts, 1, "the chart is stored")
			bos, err := boRepo.MockChartBranchOverrideRepository.List(context.Background(), resp.Instance.ID)
			require.NoError(t, err)
			assert.Len(t, bos, 1, "the branch override is stored")
			assert.Len(t, audit.snapshot(), 1, "quick deploy writes its own audit entry")
		})
	}
}

// ctxBlindDefinitionRepo ignores the context of FindByID, so a cancelled
// context reaches the chart read after it.
type ctxBlindDefinitionRepo struct {
	*MockStackDefinitionRepository
}

func (r ctxBlindDefinitionRepo) FindByID(_ context.Context, id string) (*models.StackDefinition, error) {
	return r.MockStackDefinitionRepository.FindByID(context.Background(), id)
}

// TestInstanceValuesDrift_ChartLookupUsesRequestContext checks that the chart
// read of the values drift check gets the caller context: a cancelled request
// logs at debug, another error at warn; the drift is false (#656).
// Not parallel: it replaces the default slog logger.
func TestInstanceValuesDrift_ChartLookupUsesRequestContext(t *testing.T) {
	logs := captureLogs(t)

	tests := []struct {
		ctx      func() context.Context
		chartErr error
		name     string
		wantLine string
	}{
		{name: "cancelled", ctx: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, wantLine: `level=DEBUG msg="values drift: chart lookup failed" instance_id=i1`},
		{name: "database error", ctx: context.Background, chartErr: errors.New("db down"),
			wantLine: `level=WARN msg="values drift: chart lookup failed" instance_id=i1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defRepo := NewMockStackDefinitionRepository()
			seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
			charts := NewMockChartConfigRepository()
			seedChartConfig(t, charts, "cc1", "d1", "web")
			charts.SetError(tt.chartErr)
			logRepo := NewMockDeploymentLogRepository()
			require.NoError(t, logRepo.Create(context.Background(), &models.DeploymentLog{
				ID: "log-1", StackInstanceID: "i1", Action: models.DeployActionRollback, Status: models.DeployLogSuccess,
			}))
			h := &InstanceHandler{definitionRepo: ctxBlindDefinitionRepo{defRepo}, chartConfigRepo: charts, deployLogRepo: logRepo}
			inst := &models.StackInstance{ID: "i1", StackDefinitionID: "d1", LastDeployedValues: `{"web":"replicas: 1"}`}
			logs()

			assert.False(t, h.instanceValuesDrift(tt.ctx(), inst))
			out := logs()
			assert.Equal(t, 1, strings.Count(out, tt.wantLine), out)
		})
	}
}
