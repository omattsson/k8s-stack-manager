package handlers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// defReadErrs records the context error of each definition read of a hook.
type defReadErrs struct {
	mu   sync.Mutex
	errs []error
}

func (d *defReadErrs) record(point string, ctx context.Context) {
	if point != "after:Definition.FindByID" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.errs = append(d.errs, ctx.Err())
}

func (d *defReadErrs) snapshot() []error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]error(nil), d.errs...)
}

// TestInstanceReads_CancelledAtDefinitionRead checks that the instance reads
// that read the definition after the instance pass the request context to
// the definition read (#656): a client that leaves after the instance read
// gets 499.
func TestInstanceReads_CancelledAtDefinitionRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		cancelAt string
		n        int32
		wantRead int
	}{
		{name: "export all values", path: "/api/v1/stack-instances/i1/values", cancelAt: "after:FindByID", n: 1, wantRead: 1},
		{name: "export chart values", path: "/api/v1/stack-instances/i1/values/cc1", cancelAt: "after:Chart.FindByID", n: 1, wantRead: 1},
		{name: "deploy preview", path: "/api/v1/stack-instances/i1/deploy-preview", cancelAt: "after:FindByID", n: 1, wantRead: 1},
		{name: "compare, left definition", path: "/api/v1/stack-instances/compare?left=i1&right=i2", cancelAt: "after:FindByID", n: 2, wantRead: 1},
		{name: "compare, right definition", path: "/api/v1/stack-instances/compare?left=i1&right=i2", cancelAt: "after:Definition.FindByID", n: 1, wantRead: 2},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
			inst := newHookInstanceRepo()
			def := &hookDefinitionRepo{MockStackDefinitionRepository: NewMockStackDefinitionRepository(), hooks: inst}
			seedDefinition(t, def.MockStackDefinitionRepository, "d1", "def-1", "uid-1")
			cc := newHookChartConfigRepo(inst)
			require.NoError(t, cc.Create(context.Background(), &models.ChartConfig{ID: "cc1", StackDefinitionID: "d1", ChartName: "web"}))
			seedInstance(t, inst.MockStackInstanceRepository, "i1", "one", "d1", "uid-1", models.StackStatusRunning)
			seedInstance(t, inst.MockStackInstanceRepository, "i2", "two", "d1", "uid-1", models.StackStatusRunning)
			h := NewInstanceHandler(inst, NewMockValueOverrideRepository(), NewMockChartBranchOverrideRepository(),
				def, cc, NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
				helm.NewValuesGenerator(), NewMockUserRepository(), 0)
			r := gin.New()
			r.Use(injectAuthContext("uid-1", "user"))
			g := r.Group("/api/v1/stack-instances")
			g.GET("/compare", h.CompareInstances)
			g.GET("/:id/values", h.ExportAllValues)
			g.GET("/:id/values/:chartId", h.ExportChartValues)
			g.GET("/:id/deploy-preview", h.DeployPreview)

			req, cancel := liveRequest(t, http.MethodGet, tt.path, "")
			reads := &defReadErrs{}
			cancelHook := cancelAt(tt.cancelAt, tt.n, cancel)
			inst.setHook(func(point string, ctx context.Context) {
				cancelHook(point, ctx)
				reads.record(point, ctx)
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Client closed request"}`, w.Body.String())
			errs := reads.snapshot()
			require.Len(t, errs, tt.wantRead)
			assert.ErrorIs(t, errs[len(errs)-1], context.Canceled, "the definition read runs with the request context")
		})
	}
}

// TestInstantiateTemplate_ReleaseReadContext checks the release read of
// instantiate, before the first write (#656): a cancelled read answers 499,
// a passed deadline 500, and no definition is created.
func TestInstantiateTemplate_ReleaseReadContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ctx        func(t *testing.T) context.Context
		name       string
		wantBody   string
		wantStatus int
	}{
		{
			name: "cancelled", wantStatus: statusClientClosedRequest, wantBody: `{"error":"Client closed request"}`,
			ctx: func(*testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
		},
		{
			name: "deadline exceeded", wantStatus: http.StatusInternalServerError, wantBody: `{"error":"Internal server error"}`,
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				return ctx
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newDefCtxFixture(t)
			var nameChecked atomic.Bool
			f.hooks.setHook(func(point string, _ context.Context) {
				if point == "before:Definition.FindByName" {
					nameChecked.Store(true)
				}
			})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/templates/t1/instantiate", bytes.NewBufferString(`{"name":"from-template"}`)).WithContext(tt.ctx(t))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			assert.JSONEq(t, tt.wantBody, w.Body.String())
			assert.False(t, nameChecked.Load(), "the release read ends the request")
			assert.Empty(t, f.definitionsNamed(t, "from-template"))
		})
	}
}

// TestChartConfigWrites_DefinitionReadStopsTheWrite checks the definition
// read before a chart write: a cancelled read answers 499, a passed deadline
// or a failed read 500, and the chart is not changed (#656).
func TestChartConfigWrites_DefinitionReadStopsTheWrite(t *testing.T) {
	t.Parallel()

	cancelled := func(t *testing.T) context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	deadline := func(t *testing.T) context.Context {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		t.Cleanup(cancel)
		return ctx
	}
	live := func(*testing.T) context.Context { return context.Background() }

	tests := []struct {
		ctx        func(t *testing.T) context.Context
		name       string
		role       string
		method     string
		path       string
		body       string
		fetchErr   error
		wantStatus int
	}{
		{
			name: "add chart, cancelled", role: "devops", method: http.MethodPost, path: "/api/v1/stack-definitions/d1/charts",
			body: `{"chart_name":"api","repository_url":"oci://charts/api"}`, ctx: cancelled, wantStatus: statusClientClosedRequest,
		},
		{
			name: "add chart, deadline exceeded", role: "devops", method: http.MethodPost, path: "/api/v1/stack-definitions/d1/charts",
			body: `{"chart_name":"api","repository_url":"oci://charts/api"}`, ctx: deadline, wantStatus: http.StatusInternalServerError,
		},
		{
			name: "update chart as user, cancelled", role: "user", method: http.MethodPut, path: "/api/v1/stack-definitions/d1/charts/c1",
			body: `{"default_values":"replicas: 3"}`, ctx: cancelled, wantStatus: statusClientClosedRequest,
		},
		{
			name: "update chart as user, deadline exceeded", role: "user", method: http.MethodPut, path: "/api/v1/stack-definitions/d1/charts/c1",
			body: `{"default_values":"replicas: 3"}`, ctx: deadline, wantStatus: http.StatusInternalServerError,
		},
		{
			name: "update chart as user, definition read fails", role: "user", method: http.MethodPut, path: "/api/v1/stack-definitions/d1/charts/c1",
			body: `{"default_values":"replicas: 3"}`, ctx: live, fetchErr: errors.New("db down"), wantStatus: http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
			defRepo := NewMockStackDefinitionRepository()
			seedDefinition(t, defRepo, "d1", "def-1", "uid-1")
			chartRepo := NewMockChartConfigRepository()
			require.NoError(t, chartRepo.Create(context.Background(), &models.ChartConfig{ID: "c1", StackDefinitionID: "d1", ChartName: "web", RepositoryURL: "oci://charts/web", DefaultValues: "replicas: 1"}))
			if tt.fetchErr != nil {
				defRepo.SetFetchError(tt.fetchErr)
			}
			h := NewDefinitionHandler(defRepo, chartRepo, NewMockStackInstanceRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository())
			r := gin.New()
			r.Use(injectAuthContext("uid-1", tt.role))
			r.POST("/api/v1/stack-definitions/:id/charts", h.AddChartConfig)
			r.PUT("/api/v1/stack-definitions/:id/charts/:chartId", h.UpdateChartConfig)

			req := httptest.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.body)).WithContext(tt.ctx(t))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			charts, err := chartRepo.ListByDefinition(context.Background(), "d1")
			require.NoError(t, err)
			require.Len(t, charts, 1, "no chart is added")
			assert.Equal(t, "replicas: 1", charts[0].DefaultValues, "the chart is not changed")
		})
	}
}

// chartFindFailsAt fails FindByID at call number n (1-based).
type chartFindFailsAt struct {
	*MockChartConfigRepository
	calls atomic.Int32
	n     int32
}

func (r *chartFindFailsAt) FindByID(ctx context.Context, id string) (*models.ChartConfig, error) {
	if r.calls.Add(1) == r.n {
		return nil, errors.New("db down")
	}
	return r.MockChartConfigRepository.FindByID(ctx, id)
}

// TestSetOverride_LockedValuesReadsFailClosed checks the reads of the locked
// values check after the definition read: a failed chart or template read
// answers 500 (a cancelled one 499) and stores no override; a definition that
// is gone has no locked values (#656).
func TestSetOverride_LockedValuesReadsFailClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup      func(t *testing.T, defs *MockStackDefinitionRepository, tmplCharts *MockTemplateChartConfigRepository)
		versionErr error
		name       string
		cancelAt   string
		chartFail  int32
		versions   bool
		wantStatus int
		wantStored bool
	}{
		{
			name: "chart read fails", chartFail: 2, wantStatus: http.StatusInternalServerError,
		},
		{
			// No version repository: the chart read is the last ctx-aware read.
			name: "chart read cancelled", cancelAt: "after:Definition.FindByID",
			wantStatus: statusClientClosedRequest,
		},
		{
			name: "template chart read fails", wantStatus: http.StatusInternalServerError,
			setup: func(_ *testing.T, _ *MockStackDefinitionRepository, tmplCharts *MockTemplateChartConfigRepository) {
				tmplCharts.SetError(errors.New("db down"))
			},
		},
		{
			name: "template version read fails", versions: true, versionErr: errors.New("db down"),
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "template version read cancelled", versions: true, cancelAt: "after:Definition.FindByID",
			wantStatus: statusClientClosedRequest,
		},
		{
			name: "definition gone", wantStatus: http.StatusOK, wantStored: true,
			setup: func(t *testing.T, defs *MockStackDefinitionRepository, _ *MockTemplateChartConfigRepository) {
				require.NoError(t, defs.Delete(context.Background(), "def-1"))
			},
		},
		{
			name: "no conflict", wantStatus: http.StatusOK, wantStored: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
			instRepo := newHookInstanceRepo()
			seedInstance(t, instRepo.MockStackInstanceRepository, "inst-1", "my-stack", "def-1", "uid-1", models.StackStatusDraft)
			defRepo := &hookDefinitionRepo{MockStackDefinitionRepository: NewMockStackDefinitionRepository(), hooks: instRepo}
			require.NoError(t, defRepo.MockStackDefinitionRepository.Create(context.Background(), &models.StackDefinition{
				ID: "def-1", Name: "My Def", OwnerID: "uid-1", DefaultBranch: "master", SourceTemplateID: "tmpl-1",
			}))
			ccMock := NewMockChartConfigRepository()
			seedChartConfig(t, ccMock, "chart-1", "def-1", "nginx")
			ccRepo := &chartFindFailsAt{MockChartConfigRepository: ccMock, n: tt.chartFail}
			tmplCharts := NewMockTemplateChartConfigRepository()
			require.NoError(t, tmplCharts.Create(&models.TemplateChartConfig{
				ID: "tc-1", StackTemplateID: "tmpl-1", ChartName: "nginx", LockedValues: "replicaCount: 3",
			}))
			if tt.setup != nil {
				tt.setup(t, defRepo.MockStackDefinitionRepository, tmplCharts)
			}
			ovRepo := NewMockValueOverrideRepository()
			h := NewInstanceHandler(instRepo, ovRepo, nil, defRepo, ccRepo, NewMockStackTemplateRepository(), tmplCharts,
				helm.NewValuesGenerator(), NewMockUserRepository(), 0)
			if tt.versions {
				versions := NewMockTemplateVersionRepository()
				versions.SetFetchError(tt.versionErr)
				h.WithTemplateVersions(ctxTemplateVersionRepo{versions})
			}
			r := gin.New()
			r.Use(injectAuthContext("uid-1", "user"))
			r.PUT("/api/v1/stack-instances/:id/overrides/:chartId", h.SetOverride)

			req, cancel := liveRequest(t, http.MethodPut, "/api/v1/stack-instances/inst-1/overrides/chart-1", `{"values":"image: v2"}`)
			if tt.cancelAt != "" {
				instRepo.setHook(cancelAt(tt.cancelAt, 1, cancel))
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			_, err := ovRepo.FindByInstanceAndChart("inst-1", "chart-1")
			if tt.wantStored {
				assert.NoError(t, err, "the override is stored")
				return
			}
			assert.Error(t, err, "no override is stored")
			assert.NotContains(t, w.Body.String(), "db down", "no internal detail in the answer")
		})
	}
}

// ctxSpyDefinitionRepo records the context error of the count reads and
// can cancel the request after one of them.
type ctxSpyDefinitionRepo struct {
	*MockStackDefinitionRepository
	cancelAfter string
	cancel      context.CancelFunc
	mu          sync.Mutex
	seen        map[string]error
}

func (r *ctxSpyDefinitionRepo) note(method string, ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]error{}
	}
	r.seen[method] = ctx.Err()
	if method == r.cancelAfter && r.cancel != nil {
		r.cancel()
	}
}

func (r *ctxSpyDefinitionRepo) saw(method string) (error, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	err, ok := r.seen[method]
	return err, ok
}

func (r *ctxSpyDefinitionRepo) Count(ctx context.Context) (int64, error) {
	n, err := r.MockStackDefinitionRepository.Count(ctx)
	r.note("Count", ctx)
	return n, err
}

func (r *ctxSpyDefinitionRepo) CountByTemplateIDs(ctx context.Context, ids []string) (map[string]int, error) {
	out, err := r.MockStackDefinitionRepository.CountByTemplateIDs(ctx, ids)
	r.note("CountByTemplateIDs", ctx)
	return out, err
}

func (r *ctxSpyDefinitionRepo) ListIDsByTemplateIDs(ctx context.Context, ids []string) (map[string][]string, error) {
	out, err := r.MockStackDefinitionRepository.ListIDsByTemplateIDs(ctx, ids)
	r.note("ListIDsByTemplateIDs", ctx)
	return out, err
}

// TestDefinitionCountReads_UseRequestContext checks the definition counts of
// the analytics and template list reads (#656): they run with the request
// context, a cancelled count answers 499 and a passed deadline 500. The
// template list keeps its answer without the counts.
func TestDefinitionCountReads_UseRequestContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		path        string
		end         string
		cancelAfter string
		method      string
		wantErr     error
		wantStatus  int
	}{
		{name: "overview, cancelled", path: "/api/v1/analytics/overview", end: "cancel", method: "Count", wantErr: context.Canceled, wantStatus: statusClientClosedRequest},
		{name: "overview, deadline exceeded", path: "/api/v1/analytics/overview", end: "deadline", method: "Count", wantErr: context.DeadlineExceeded, wantStatus: http.StatusInternalServerError},
		{name: "template stats, cancelled", path: "/api/v1/analytics/templates", end: "cancel", method: "CountByTemplateIDs", wantErr: context.Canceled, wantStatus: statusClientClosedRequest},
		{name: "template stats, cancelled after the count", path: "/api/v1/analytics/templates", cancelAfter: "CountByTemplateIDs", method: "ListIDsByTemplateIDs", wantErr: context.Canceled, wantStatus: statusClientClosedRequest},
		{name: "template stats, deadline exceeded", path: "/api/v1/analytics/templates", end: "deadline", method: "CountByTemplateIDs", wantErr: context.DeadlineExceeded, wantStatus: http.StatusInternalServerError},
		{name: "template list, cancelled", path: "/api/v1/templates", end: "cancel", method: "CountByTemplateIDs", wantErr: context.Canceled, wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
			tmplRepo := NewMockStackTemplateRepository()
			seedTemplate(t, tmplRepo, "t1", "tmpl", "uid-1", true)
			defs := &ctxSpyDefinitionRepo{MockStackDefinitionRepository: NewMockStackDefinitionRepository(), cancelAfter: tt.cancelAfter}
			require.NoError(t, defs.MockStackDefinitionRepository.Create(context.Background(), &models.StackDefinition{ID: "d1", Name: "def", OwnerID: "uid-1", SourceTemplateID: "t1"}))
			ah := NewAnalyticsHandler(tmplRepo, defs, NewMockStackInstanceRepository(), newMockDeployLogRepo(), NewMockUserRepository())
			th := NewTemplateHandler(tmplRepo, NewMockTemplateChartConfigRepository(), defs, NewMockChartConfigRepository())
			r := gin.New()
			r.Use(injectAuthContext("admin-1", "admin"))
			r.GET("/api/v1/analytics/overview", ah.GetOverview)
			r.GET("/api/v1/analytics/templates", ah.GetTemplateStats)
			r.GET("/api/v1/templates", th.ListTemplates)

			var ctx context.Context
			switch tt.end {
			case "cancel":
				c, cancel := context.WithCancel(context.Background())
				cancel()
				ctx = c
			case "deadline":
				c, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				ctx = c
			default:
				c, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				defs.cancel = cancel
				ctx = c
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil).WithContext(ctx))

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			err, called := defs.saw(tt.method)
			require.True(t, called, "%s ran", tt.method)
			assert.ErrorIs(t, err, tt.wantErr, "%s runs with the request context", tt.method)
		})
	}
}

// TestNotificationChannelFilterWarnings_DefinitionLookupContext checks the
// definition check of the channel filters (#656): it runs with the request
// context; a cancelled lookup logs one debug line and gives no warning; a
// passed deadline logs an error. Not parallel: it replaces the default slog
// logger.
func TestNotificationChannelFilterWarnings_DefinitionLookupContext(t *testing.T) {
	var buf bytes.Buffer
	var bufMu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{mu: &bufMu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	defs := NewMockStackDefinitionRepository()
	require.NoError(t, defs.Create(context.Background(), &models.StackDefinition{ID: "d1", Name: "full-stack"}))
	h := NewNotificationChannelHandler(NewMockNotificationChannelRepository()).WithFilterLookups(nil, defs, nil)
	filters := models.NotificationChannelFilters{DefinitionIDs: []string{"d1", "gone-def"}}

	tests := []struct {
		ctx          func(t *testing.T) context.Context
		name         string
		wantWarnings []string
		wantLog      string
		wantNoLog    string
	}{
		{
			name:         "live",
			ctx:          func(*testing.T) context.Context { return context.Background() },
			wantWarnings: []string{"definition_ids: stack definition gone-def does not exist"},
		},
		{
			name: "cancelled",
			ctx: func(*testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantLog:   `level=DEBUG msg="Channel filter definition check cancelled"`,
			wantNoLog: "Failed to check channel filter definitions",
		},
		{
			name: "deadline exceeded",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				return ctx
			},
			wantLog:   `level=ERROR msg="Failed to check channel filter definitions"`,
			wantNoLog: "Channel filter definition check cancelled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bufMu.Lock()
			buf.Reset()
			bufMu.Unlock()

			got := h.filterWarnings(tt.ctx(t), filters)

			assert.Equal(t, tt.wantWarnings, got)
			bufMu.Lock()
			logs := buf.String()
			bufMu.Unlock()
			if tt.wantLog != "" {
				assert.Equal(t, 1, strings.Count(logs, tt.wantLog), logs)
			}
			if tt.wantNoLog != "" {
				assert.NotContains(t, logs, tt.wantNoLog)
			}
		})
	}
}

// lockedWriter serializes writes to w: goroutines of other tests may log.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
