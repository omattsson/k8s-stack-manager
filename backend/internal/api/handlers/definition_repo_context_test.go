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
	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// defCtxFixture is a definition and template handler over mocks that fail on
// a done context, with the audit middleware (#656). hooks dispatches the
// cancel hooks and is the instance repository.
type defCtxFixture struct {
	hooks     *hookInstanceRepo
	def       *hookDefinitionRepo
	defCharts *hookChartConfigRepo
	templates *MockStackTemplateRepository
	charts    *MockTemplateChartConfigRepository
	versions  *MockTemplateVersionRepository
	audit     *MockAuditLogRepository
	router    *gin.Engine
}

// newDefCtxFixture seeds template t1 (published 1.0.0 and 1.1.0, required
// chart "web"), the unpublished template t3, definition d1 (linked to t1
// 1.0.0, chart c1 "web") and definition d2 (no link, chart c2 "extra"), all
// of uid-dev. The caller is uid-dev with the devops role.
func newDefCtxFixture(t *testing.T) *defCtxFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &defCtxFixture{
		hooks:     newHookInstanceRepo(),
		templates: NewMockStackTemplateRepository(),
		charts:    NewMockTemplateChartConfigRepository(),
		versions:  NewMockTemplateVersionRepository(),
		audit:     NewMockAuditLogRepository(),
	}
	f.def = &hookDefinitionRepo{MockStackDefinitionRepository: NewMockStackDefinitionRepository(), hooks: f.hooks}
	f.defCharts = newHookChartConfigRepo(f.hooks)
	versions := ctxTemplateVersionRepo{f.versions}
	tx := &mockHandlerTxRunner{repos: database.TxRepos{
		StackTemplate:   f.templates,
		TemplateChart:   f.charts,
		TemplateVersion: versions,
		StackDefinition: f.def,
		ChartConfig:     f.defCharts,
		StackInstance:   f.hooks,
	}}
	th, err := NewTemplateHandlerWithVersions(f.templates, f.charts, f.def, f.defCharts, versions, tx)
	require.NoError(t, err)
	dh, err := NewDefinitionHandlerWithVersions(f.def, f.defCharts, f.hooks, f.templates, f.charts, versions, tx)
	require.NoError(t, err)

	r := gin.New()
	r.Use(injectAuthContext("uid-dev", "devops"), middleware.NewAuditMiddleware(f.audit))
	tpl := r.Group("/api/v1/templates")
	tpl.PUT("/:id", th.UpdateTemplate)
	tpl.DELETE("/:id", th.DeleteTemplate)
	tpl.POST("/:id/publish", th.PublishTemplate)
	tpl.POST("/:id/instantiate", th.InstantiateTemplate)
	tpl.POST("/bulk/delete", th.BulkDeleteTemplates)
	defs := r.Group("/api/v1/stack-definitions")
	defs.GET("", dh.ListDefinitions)
	defs.POST("", dh.CreateDefinition)
	defs.POST("/import", dh.ImportDefinition)
	defs.GET("/:id", dh.GetDefinition)
	defs.PUT("/:id", dh.UpdateDefinition)
	defs.DELETE("/:id", dh.DeleteDefinition)
	defs.GET("/:id/export", dh.ExportDefinition)
	defs.GET("/:id/check-upgrade", dh.CheckUpgrade)
	defs.POST("/:id/upgrade", dh.ApplyUpgrade)
	defs.PUT("/:id/template-link", dh.SetTemplateLink)
	defs.POST("/:id/charts", dh.AddChartConfig)
	defs.GET("/:id/charts/:chartId", dh.GetChartConfig)
	defs.PUT("/:id/charts/:chartId", dh.UpdateChartConfig)
	defs.DELETE("/:id/charts/:chartId", dh.DeleteChartConfig)
	f.router = r

	now := time.Now().UTC()
	require.NoError(t, f.templates.Create(&models.StackTemplate{
		ID: "t1", Name: "Web stack", Version: "1.0.0", OwnerID: "uid-dev", DefaultBranch: "main", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, f.charts.Create(&models.TemplateChartConfig{
		ID: "tc-web", StackTemplateID: "t1", ChartName: "web", RepositoryURL: "oci://charts/web",
		ChartVersion: "1.0.0", DefaultValues: "replicas: 1", Required: true, DeployOrder: 1, CreatedAt: now,
	}))
	f.mustServe(t, http.MethodPost, "/api/v1/templates/t1/publish", `{}`, http.StatusOK)
	f.mustServe(t, http.MethodPut, "/api/v1/templates/t1", `{"version":"1.1.0"}`, http.StatusOK)
	f.mustServe(t, http.MethodPost, "/api/v1/templates/t1/publish", `{}`, http.StatusOK)
	require.NoError(t, f.templates.Create(&models.StackTemplate{
		ID: "t3", Name: "Unused", Version: "0.1.0", OwnerID: "uid-dev", CreatedAt: now, UpdatedAt: now,
	}))
	for _, d := range []*models.StackDefinition{
		{ID: "d1", Name: "linked", OwnerID: "uid-dev", SourceTemplateID: "t1", SourceTemplateVersion: "1.0.0", DefaultBranch: "main", CreatedAt: now, UpdatedAt: now},
		{ID: "d2", Name: "plain", OwnerID: "uid-dev", DefaultBranch: "main", CreatedAt: now, UpdatedAt: now},
	} {
		require.NoError(t, f.def.MockStackDefinitionRepository.Create(context.Background(), d))
	}
	require.NoError(t, f.defCharts.Create(context.Background(), &models.ChartConfig{ID: "c1", StackDefinitionID: "d1", ChartName: "web", RepositoryURL: "oci://charts/web", CreatedAt: now}))
	require.NoError(t, f.defCharts.Create(context.Background(), &models.ChartConfig{ID: "c2", StackDefinitionID: "d2", ChartName: "extra", RepositoryURL: "oci://charts/extra", CreatedAt: now}))
	// The seed requests above wrote audit entries; the tests count their own.
	require.Eventually(t, func() bool { return len(f.audit.snapshot()) == 3 }, 2*time.Second, 10*time.Millisecond)
	f.audit.mu.Lock()
	f.audit.entries = nil
	f.audit.mu.Unlock()
	return f
}

func (f *defCtxFixture) mustServe(t *testing.T, method, path, body string, want int) {
	t.Helper()
	w := serve(f.router, method, path, body)
	require.Equal(t, want, w.Code, w.Body.String())
}

func (f *defCtxFixture) definition(t *testing.T, id string) *models.StackDefinition {
	t.Helper()
	d, err := f.def.MockStackDefinitionRepository.FindByID(context.Background(), id)
	require.NoError(t, err)
	return d
}

func (f *defCtxFixture) definitionsNamed(t *testing.T, name string) []models.StackDefinition {
	t.Helper()
	defs, err := f.def.MockStackDefinitionRepository.FindByName(context.Background(), name)
	require.NoError(t, err)
	return defs
}

// TestDefinitionWrites_CancelledAtWriteStillCommit checks that a client that
// leaves at the definition write cannot stop the write, nor the 2xx answer
// and the audit entry after it (#656). The mocks fail on a done context, so a
// write with the request context fails.
func TestDefinitionWrites_CancelledAtWriteStillCommit(t *testing.T) {
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
			name: "create", method: http.MethodPost, path: "/api/v1/stack-definitions",
			body: `{"name":"fresh"}`, cancelAt: "before:Definition.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Len(t, f.definitionsNamed(t, "fresh"), 1)
			},
		},
		{
			name: "update", method: http.MethodPut, path: "/api/v1/stack-definitions/d2",
			body: `{"name":"renamed","default_branch":"main"}`, cancelAt: "before:Definition.Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, "renamed", f.definition(t, "d2").Name)
			},
		},
		{
			name: "delete", method: http.MethodDelete, path: "/api/v1/stack-definitions/d2",
			cancelAt: "before:Definition.Delete", wantStatus: http.StatusNoContent,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				_, err := f.def.MockStackDefinitionRepository.FindByID(context.Background(), "d2")
				assert.Error(t, err, "the definition is deleted")
			},
		},
		{
			name: "upgrade", method: http.MethodPost, path: "/api/v1/stack-definitions/d1/upgrade",
			cancelAt: "before:Definition.Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, "1.1.0", f.definition(t, "d1").SourceTemplateVersion)
			},
		},
		{
			name: "template link", method: http.MethodPut, path: "/api/v1/stack-definitions/d2/template-link",
			body: `{"template_id":"t1"}`, cancelAt: "before:Definition.SetTemplateLink", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *defCtxFixture, w *httptest.ResponseRecorder) {
				var resp models.StackDefinition
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "t1", resp.SourceTemplateID, "the answer reads the stored link")
				assert.Equal(t, "1.1.0", f.definition(t, "d2").SourceTemplateVersion)
			},
		},
		{
			name: "import", method: http.MethodPost, path: "/api/v1/stack-definitions/import",
			body:     `{"schema_version":"1.0","definition":{"name":"imported"},"charts":[{"chart_name":"web","repository_url":"oci://charts/web"}]}`,
			cancelAt: "before:Definition.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				defs := f.definitionsNamed(t, "imported")
				require.Len(t, defs, 1)
				charts, err := f.defCharts.ListByDefinition(context.Background(), defs[0].ID)
				require.NoError(t, err)
				assert.Len(t, charts, 1)
			},
		},
		{
			name: "instantiate", method: http.MethodPost, path: "/api/v1/templates/t1/instantiate",
			body: `{"name":"from-template"}`, cancelAt: "before:Definition.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				defs := f.definitionsNamed(t, "from-template")
				require.Len(t, defs, 1)
				assert.Equal(t, "1.1.0", defs[0].SourceTemplateVersion)
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

// TestDefinitionWrites_CancelledBeforeWriteChangeNothing checks that a client
// that leaves during a read before the first write gets 499 and that nothing
// changes (#656).
func TestDefinitionWrites_CancelledBeforeWriteChangeNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		check    func(t *testing.T, f *defCtxFixture)
		name     string
		method   string
		path     string
		body     string
		cancelAt string
	}{
		{
			name: "create at the name check", method: http.MethodPost, path: "/api/v1/stack-definitions",
			body: `{"name":"fresh"}`, cancelAt: "before:Definition.FindByName",
			check: func(t *testing.T, f *defCtxFixture) { assert.Empty(t, f.definitionsNamed(t, "fresh")) },
		},
		{
			name: "update at the definition read", method: http.MethodPut, path: "/api/v1/stack-definitions/d2",
			body: `{"name":"renamed","default_branch":"main"}`, cancelAt: "before:Definition.FindByID",
			check: func(t *testing.T, f *defCtxFixture) { assert.Equal(t, "plain", f.definition(t, "d2").Name) },
		},
		{
			name: "update at the name check", method: http.MethodPut, path: "/api/v1/stack-definitions/d2",
			body: `{"name":"renamed","default_branch":"main"}`, cancelAt: "before:Definition.FindByName",
			check: func(t *testing.T, f *defCtxFixture) { assert.Equal(t, "plain", f.definition(t, "d2").Name) },
		},
		{
			name: "delete at the running-instance check", method: http.MethodDelete, path: "/api/v1/stack-definitions/d2",
			cancelAt: "after:Definition.FindByID",
			check:    func(t *testing.T, f *defCtxFixture) { f.definition(t, "d2") },
		},
		{
			name: "upgrade at the definition read", method: http.MethodPost, path: "/api/v1/stack-definitions/d1/upgrade",
			cancelAt: "before:Definition.FindByID",
			check: func(t *testing.T, f *defCtxFixture) {
				assert.Equal(t, "1.0.0", f.definition(t, "d1").SourceTemplateVersion)
			},
		},
		{
			name: "upgrade at the release read", method: http.MethodPost, path: "/api/v1/stack-definitions/d1/upgrade",
			cancelAt: "after:Definition.FindByID",
			check: func(t *testing.T, f *defCtxFixture) {
				assert.Equal(t, "1.0.0", f.definition(t, "d1").SourceTemplateVersion)
			},
		},
		{
			name: "template link at the definition read", method: http.MethodPut, path: "/api/v1/stack-definitions/d2/template-link",
			body: `{"template_id":"t1"}`, cancelAt: "before:Definition.FindByID",
			check: func(t *testing.T, f *defCtxFixture) { assert.Empty(t, f.definition(t, "d2").SourceTemplateID) },
		},
		{
			name: "import at the name check", method: http.MethodPost, path: "/api/v1/stack-definitions/import",
			body:     `{"schema_version":"1.0","definition":{"name":"imported"},"charts":[{"chart_name":"web"}]}`,
			cancelAt: "before:Definition.FindByName",
			check:    func(t *testing.T, f *defCtxFixture) { assert.Empty(t, f.definitionsNamed(t, "imported")) },
		},
		{
			name: "instantiate at the name check", method: http.MethodPost, path: "/api/v1/templates/t1/instantiate",
			body: `{"name":"from-template"}`, cancelAt: "before:Definition.FindByName",
			check: func(t *testing.T, f *defCtxFixture) { assert.Empty(t, f.definitionsNamed(t, "from-template")) },
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

			assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Client closed request"}`, w.Body.String())
			tt.check(t, f)
			assert.Never(t, func() bool { return len(f.audit.snapshot()) > 0 },
				100*time.Millisecond, 10*time.Millisecond, "no audit entry")
		})
	}
}

// TestDefinitionReads_CancelledRequest checks that definition reads for a
// client that left answer 499, and that a passed deadline answers 500 (#656).
func TestDefinitionReads_CancelledRequest(t *testing.T) {
	t.Parallel()

	paths := map[string]string{
		"get":           "/api/v1/stack-definitions/d1",
		"list":          "/api/v1/stack-definitions",
		"export":        "/api/v1/stack-definitions/d1/export",
		"check-upgrade": "/api/v1/stack-definitions/d1/check-upgrade",
	}
	ends := []struct {
		ctx        func(t *testing.T) context.Context
		name       string
		wantBody   string
		wantStatus int
	}{
		{
			name: "cancelled", wantStatus: statusClientClosedRequest, wantBody: `{"error":"Client closed request"}`,
			ctx: func(t *testing.T) context.Context {
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
	for name, path := range paths {
		for _, end := range ends {
			name, path, end := name, path, end
			t.Run(name+", "+end.name, func(t *testing.T) {
				t.Parallel()
				f := newDefCtxFixture(t)
				w := httptest.NewRecorder()
				f.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil).WithContext(end.ctx(t)))

				assert.Equal(t, end.wantStatus, w.Code, w.Body.String())
				assert.JSONEq(t, end.wantBody, w.Body.String())
			})
		}
	}

	t.Run("check-upgrade cancelled at the release read", func(t *testing.T) {
		t.Parallel()
		f := newDefCtxFixture(t)
		req, cancel := liveRequest(t, http.MethodGet, "/api/v1/stack-definitions/d1/check-upgrade", "")
		f.hooks.setHook(cancelAt("after:Definition.FindByID", 1, cancel))
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)

		assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
	})
}

// ctxTemplateVersionRepo fails the release read on a done context, as the
// GORM repository does.
type ctxTemplateVersionRepo struct {
	*MockTemplateVersionRepository
}

func (r ctxTemplateVersionRepo) GetLatestByTemplate(ctx context.Context, templateID string) (*models.TemplateVersion, error) {
	if err := ctxDone(ctx, "get_latest_by_template"); err != nil {
		return nil, err
	}
	return r.MockTemplateVersionRepository.GetLatestByTemplate(ctx, templateID)
}

// TestDefinitionGuards_FailedReadStopsTheWrite checks the guards before a
// write: a failed or cancelled read stops the write, it never counts as "no
// rule" (#656).
func TestDefinitionGuards_FailedReadStopsTheWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setup      func(t *testing.T, f *defCtxFixture, cancel context.CancelFunc)
		check      func(t *testing.T, f *defCtxFixture, w *httptest.ResponseRecorder)
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{
			name: "delete chart, definition read fails", method: http.MethodDelete, path: "/api/v1/stack-definitions/d1/charts/c1",
			wantStatus: http.StatusInternalServerError,
			setup:      func(_ *testing.T, f *defCtxFixture, _ context.CancelFunc) { f.def.SetFetchError(errors.New("db down")) },
			check:      chartKept("c1"),
		},
		{
			name: "delete chart, definition read cancelled", method: http.MethodDelete, path: "/api/v1/stack-definitions/d1/charts/c1",
			wantStatus: statusClientClosedRequest,
			setup: func(_ *testing.T, f *defCtxFixture, cancel context.CancelFunc) {
				f.hooks.setHook(cancelAt("before:Definition.FindByID", 1, cancel))
			},
			check: chartKept("c1"),
		},
		{
			name: "delete chart, required chart read fails", method: http.MethodDelete, path: "/api/v1/stack-definitions/d1/charts/c1",
			wantStatus: http.StatusInternalServerError,
			setup: func(_ *testing.T, f *defCtxFixture, _ context.CancelFunc) {
				f.versions.SetFetchError(errors.New("db down"))
			},
			check: chartKept("c1"),
		},
		{
			name: "delete chart, required chart", method: http.MethodDelete, path: "/api/v1/stack-definitions/d1/charts/c1",
			wantStatus: http.StatusConflict,
			setup:      func(*testing.T, *defCtxFixture, context.CancelFunc) {},
			check:      chartKept("c1"),
		},
		{
			name: "delete chart, gone definition has no template rule", method: http.MethodDelete, path: "/api/v1/stack-definitions/d2/charts/c2",
			wantStatus: http.StatusNoContent,
			setup: func(t *testing.T, f *defCtxFixture, _ context.CancelFunc) {
				require.NoError(t, f.def.MockStackDefinitionRepository.Delete(context.Background(), "d2"))
			},
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				_, err := f.defCharts.FindByID(context.Background(), "c2")
				assert.Error(t, err, "the chart is deleted")
			},
		},
		{
			name: "delete template, definition check fails", method: http.MethodDelete, path: "/api/v1/templates/t3",
			wantStatus: http.StatusInternalServerError,
			setup:      func(_ *testing.T, f *defCtxFixture, _ context.CancelFunc) { f.def.SetError(errors.New("db down")) },
			check:      templateKept("t3"),
		},
		{
			name: "delete template, definition check cancelled", method: http.MethodDelete, path: "/api/v1/templates/t3",
			wantStatus: statusClientClosedRequest,
			setup: func(_ *testing.T, f *defCtxFixture, cancel context.CancelFunc) {
				f.hooks.setHook(cancelAt("before:Definition.ListByTemplate", 1, cancel))
			},
			check: templateKept("t3"),
		},
		{
			name: "bulk delete templates, definition check cancelled", method: http.MethodPost, path: "/api/v1/templates/bulk/delete",
			body: `{"template_ids":["t3"]}`, wantStatus: http.StatusOK,
			setup: func(_ *testing.T, f *defCtxFixture, cancel context.CancelFunc) {
				f.hooks.setHook(cancelAt("before:Definition.ListByTemplate", 1, cancel))
			},
			check: func(t *testing.T, f *defCtxFixture, w *httptest.ResponseRecorder) {
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				require.Len(t, resp.Results, 1)
				assert.Equal(t, "error", resp.Results[0].Status)
				assert.Equal(t, "request cancelled", resp.Results[0].Error)
				templateKept("t3")(t, f, w)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newDefCtxFixture(t)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			tt.setup(t, f, cancel)

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			tt.check(t, f, w)
		})
	}
}

func chartKept(id string) func(t *testing.T, f *defCtxFixture, w *httptest.ResponseRecorder) {
	return func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
		_, err := f.defCharts.FindByID(context.Background(), id)
		assert.NoError(t, err, "the chart is kept")
	}
}

func templateKept(id string) func(t *testing.T, f *defCtxFixture, w *httptest.ResponseRecorder) {
	return func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
		_, err := f.templates.FindByID(id)
		assert.NoError(t, err, "the template is kept")
	}
}

// TestSetOverride_FailedDefinitionReadStopsTheWrite checks that the locked
// values check fails closed: a failed definition read answers 500 and stores
// no override (#656).
func TestSetOverride_FailedDefinitionReadStopsTheWrite(t *testing.T) {
	t.Parallel()
	f := newWriteCtxFixture(t)
	f.def.SetFetchError(errors.New("db down"))

	w := serve(f.router, http.MethodPut, "/api/v1/stack-instances/draft-1/overrides/cc1", `{"values":"replicas: 2\n"}`)

	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
	_, err := f.ov.FindByInstanceAndChart("draft-1", "cc1")
	assert.Error(t, err, "no override is stored")
}

// TestInstanceWrites_CancelledDefinitionReadChangesNothing checks that an
// instance operation whose client left at the definition read, before the
// first write, answers 499 (bulk: "request cancelled") and changes nothing
// (#656).
func TestInstanceWrites_CancelledDefinitionReadChangesNothing(t *testing.T) {
	t.Parallel()

	noLog := func(id string) func(t *testing.T, f *writeCtxFixture, w *httptest.ResponseRecorder) {
		return func(t *testing.T, f *writeCtxFixture, _ *httptest.ResponseRecorder) {
			assert.Empty(t, f.logActions(t, id), "no operation started")
		}
	}
	bulkCancelled := func(id string) func(t *testing.T, f *writeCtxFixture, w *httptest.ResponseRecorder) {
		return func(t *testing.T, f *writeCtxFixture, w *httptest.ResponseRecorder) {
			results := bulkResults(t, w)
			require.Len(t, results, 1)
			assert.Equal(t, "error", results[0].Status)
			assert.Equal(t, "request cancelled", results[0].Error)
			noLog(id)(t, f, w)
		}
	}
	tests := []struct {
		check      func(t *testing.T, f *writeCtxFixture, w *httptest.ResponseRecorder)
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{
			name: "create", method: http.MethodPost, path: "/api/v1/stack-instances",
			body: `{"stack_definition_id":"d1","name":"new-one"}`, wantStatus: statusClientClosedRequest,
			check: func(t *testing.T, f *writeCtxFixture, _ *httptest.ResponseRecorder) {
				all, err := f.inst.List(context.Background())
				require.NoError(t, err)
				assert.Len(t, all, 4, "no instance is created")
			},
		},
		{
			name: "deploy", method: http.MethodPost, path: "/api/v1/stack-instances/draft-1/deploy", wantStatus: statusClientClosedRequest,
			check: func(t *testing.T, f *writeCtxFixture, w *httptest.ResponseRecorder) {
				assert.Nil(t, f.instance(t, "draft-1").ExpiresAt, "no TTL expiry is written")
				noLog("draft-1")(t, f, w)
			},
		},
		{name: "stop", method: http.MethodPost, path: "/api/v1/stack-instances/run-1/stop", wantStatus: statusClientClosedRequest, check: noLog("run-1")},
		{name: "clean", method: http.MethodPost, path: "/api/v1/stack-instances/stopped-1/clean", wantStatus: statusClientClosedRequest, check: noLog("stopped-1")},
		{name: "rollback", method: http.MethodPost, path: "/api/v1/stack-instances/run-1/rollback", wantStatus: statusClientClosedRequest, check: noLog("run-1")},
		{
			name: "value override", method: http.MethodPut, path: "/api/v1/stack-instances/draft-1/overrides/cc1",
			body: `{"values":"replicas: 2\n"}`, wantStatus: statusClientClosedRequest,
			check: func(t *testing.T, f *writeCtxFixture, _ *httptest.ResponseRecorder) {
				_, err := f.ov.FindByInstanceAndChart("draft-1", "cc1")
				assert.Error(t, err, "no override is stored")
			},
		},
		{name: "bulk deploy", method: http.MethodPost, path: "/api/v1/stack-instances/bulk/deploy", body: `{"instance_ids":["draft-1"]}`, wantStatus: http.StatusOK, check: bulkCancelled("draft-1")},
		{name: "bulk stop", method: http.MethodPost, path: "/api/v1/stack-instances/bulk/stop", body: `{"instance_ids":["run-1"]}`, wantStatus: http.StatusOK, check: bulkCancelled("run-1")},
		{name: "bulk clean", method: http.MethodPost, path: "/api/v1/stack-instances/bulk/clean", body: `{"instance_ids":["stopped-1"]}`, wantStatus: http.StatusOK, check: bulkCancelled("stopped-1")},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWriteCtxFixture(t)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			cancelFirst := cancelAt("before:Definition.FindByID", 1, cancel)
			var readSawCancel atomic.Bool
			f.inst.setHook(func(point string, ctx context.Context) {
				cancelFirst(point, ctx)
				if point == "after:Definition.FindByID" && ctx.Err() != nil {
					readSawCancel.Store(true)
				}
			})

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			assert.True(t, readSawCancel.Load(), "the definition read runs with the request context")
			tt.check(t, f, w)
		})
	}
}

// TestQuickDeploy_DefinitionContext checks quick deploy around the
// definition: a client that leaves at the definition create still gets the
// instance, the deploy and the audit entry; a client that leaves at the name
// check before the first write gets 499 and nothing is created (#656).
func TestQuickDeploy_DefinitionContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cancelAt   string
		wantStatus int
	}{
		{name: "cancelled at the definition create", cancelAt: "before:Definition.Create", wantStatus: http.StatusAccepted},
		{name: "cancelled at the name check", cancelAt: "before:Definition.FindByName", wantStatus: statusClientClosedRequest},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
			tmplRepo := NewMockStackTemplateRepository()
			tmplChartRepo := NewMockTemplateChartConfigRepository()
			instRepo := newHookInstanceRepo()
			defRepo := &hookDefinitionRepo{MockStackDefinitionRepository: NewMockStackDefinitionRepository(), hooks: instRepo}
			ccRepo := NewMockChartConfigRepository()
			boRepo := NewMockChartBranchOverrideRepository()
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
			r.Use(func(c *gin.Context) {
				c.Set("userID", "uid-1")
				c.Set("username", "alice")
				c.Set("role", "user")
				c.Next()
			})
			r.POST("/api/v1/templates/:id/quick-deploy", h.QuickDeploy)

			req, cancel := liveRequest(t, http.MethodPost, "/api/v1/templates/t1/quick-deploy", `{"instance_name":"qd-def"}`)
			instRepo.setHook(cancelAt(tt.cancelAt, 1, cancel))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			all, err := instRepo.List(context.Background())
			require.NoError(t, err)
			defs, err := defRepo.MockStackDefinitionRepository.List(context.Background())
			require.NoError(t, err)
			if tt.wantStatus != http.StatusAccepted {
				assert.Empty(t, all, "no instance is created")
				assert.Empty(t, defs, "no definition is created")
				assert.Empty(t, audit.snapshot())
				return
			}
			var resp quickDeployResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.NotEmpty(t, resp.LogID, "the deploy started")
			require.Len(t, defs, 1, "the definition is stored")
			assert.Equal(t, resp.Instance.ID, defs[0].OwnerInstanceID)
			assert.Len(t, all, 1)
			assert.Len(t, audit.snapshot(), 1, "quick deploy writes its own audit entry")
		})
	}
}

// TestSetInstanceNames_DefinitionLookupUsesContext checks that the definition
// names of instance responses are read with the request context: a cancelled
// lookup leaves the names out instead of running after the client left.
func TestSetInstanceNames_DefinitionLookupUsesContext(t *testing.T) {
	t.Parallel()
	defRepo := NewMockStackDefinitionRepository()
	seedDefinition(t, defRepo, "d1", "def-1", "uid-1")
	h := NewInstanceHandler(NewMockStackInstanceRepository(), NewMockValueOverrideRepository(), NewMockChartBranchOverrideRepository(),
		defRepo, NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), NewMockUserRepository(), 0)

	live := &models.StackInstance{ID: "i1", StackDefinitionID: "d1"}
	h.setInstanceNames(context.Background(), live)
	assert.Equal(t, "def-1", live.DefinitionName)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := &models.StackInstance{ID: "i1", StackDefinitionID: "d1"}
	h.setInstanceNames(ctx, cancelled)
	assert.Empty(t, cancelled.DefinitionName)
}
