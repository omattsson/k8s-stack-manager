package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"backend/internal/database"
	"backend/internal/helm"
	"backend/internal/models"

	"backend/internal/cluster"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// templateOf returns the stored template, or nil when it does not exist.
func (f *defCtxFixture) templateOf(t *testing.T, id string) *models.StackTemplate {
	t.Helper()
	tmpl, err := f.templates.MockStackTemplateRepository.FindByID(context.Background(), id)
	if err != nil {
		return nil
	}
	return tmpl
}

// templatesNamed returns the stored templates with the name.
func (f *defCtxFixture) templatesNamed(t *testing.T, name string) []models.StackTemplate {
	t.Helper()
	all, err := f.templates.MockStackTemplateRepository.List(context.Background())
	require.NoError(t, err)
	var out []models.StackTemplate
	for _, tmpl := range all {
		if tmpl.Name == name {
			out = append(out, tmpl)
		}
	}
	return out
}

// templateChartsOf returns the stored charts of the template.
func (f *defCtxFixture) templateChartsOf(t *testing.T, templateID string) []models.TemplateChartConfig {
	t.Helper()
	charts, err := f.charts.MockTemplateChartConfigRepository.ListByTemplate(context.Background(), templateID)
	require.NoError(t, err)
	return charts
}

// templateState is the JSON of all templates and their charts, in a stable
// order, for a before and after comparison.
func (f *defCtxFixture) templateState(t *testing.T) string {
	t.Helper()
	all, err := f.templates.MockStackTemplateRepository.List(context.Background())
	require.NoError(t, err)
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	type entry struct {
		Template models.StackTemplate
		Charts   []models.TemplateChartConfig
	}
	out := make([]entry, 0, len(all))
	for _, tmpl := range all {
		charts := f.templateChartsOf(t, tmpl.ID)
		sort.Slice(charts, func(i, j int) bool { return charts[i].ID < charts[j].ID })
		out = append(out, entry{Template: tmpl, Charts: charts})
	}
	b, err := json.Marshal(out)
	require.NoError(t, err)
	versions, err := f.versions.ListByTemplate(context.Background(), "t1")
	require.NoError(t, err)
	return string(b) + "|versions:" + string(rune('0'+len(versions)))
}

// TestTemplateWrites_CancelledAtWriteStillCommit checks that a client that
// leaves at a template or template chart write cannot stop the write, the
// 2xx answer or the audit entry (#656). The mocks fail on a done context, so
// a write with the request context fails.
func TestTemplateWrites_CancelledAtWriteStillCommit(t *testing.T) {
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
			name: "create", method: http.MethodPost, path: "/api/v1/templates",
			body: `{"name":"fresh","version":"1.0.0"}`, cancelAt: "before:Template.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Len(t, f.templatesNamed(t, "fresh"), 1)
			},
		},
		{
			name: "create with charts, at the template insert", method: http.MethodPost, path: "/api/v1/templates",
			body:     `{"name":"fresh","version":"1.0.0","charts":[{"chart_name":"web","repository_url":"oci://charts/web"},{"chart_name":"api","repository_url":"oci://charts/api"}]}`,
			cancelAt: "before:Template.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				created := f.templatesNamed(t, "fresh")
				require.Len(t, created, 1)
				assert.Len(t, f.templateChartsOf(t, created[0].ID), 2, "both charts are stored")
			},
		},
		{
			name: "create with charts, at the chart insert", method: http.MethodPost, path: "/api/v1/templates",
			body:     `{"name":"fresh","version":"1.0.0","charts":[{"chart_name":"web","repository_url":"oci://charts/web"},{"chart_name":"api","repository_url":"oci://charts/api"}]}`,
			cancelAt: "before:TemplateChart.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				created := f.templatesNamed(t, "fresh")
				require.Len(t, created, 1)
				assert.Len(t, f.templateChartsOf(t, created[0].ID), 2, "both charts are stored")
			},
		},
		{
			name: "create published", method: http.MethodPost, path: "/api/v1/templates",
			body:     `{"name":"fresh","version":"1.0.0","is_published":true,"charts":[{"chart_name":"web","repository_url":"oci://charts/web"}]}`,
			cancelAt: "before:Template.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *defCtxFixture, w *httptest.ResponseRecorder) {
				created := f.templatesNamed(t, "fresh")
				require.Len(t, created, 1)
				assert.True(t, created[0].IsPublished)
				versions, err := f.versions.ListByTemplate(context.Background(), created[0].ID)
				require.NoError(t, err)
				assert.Len(t, versions, 1, "the first snapshot is stored")
			},
		},
		{
			name: "update", method: http.MethodPut, path: "/api/v1/templates/t3",
			body: `{"name":"renamed"}`, cancelAt: "before:Template.Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Equal(t, "renamed", f.templateOf(t, "t3").Name)
			},
		},
		{
			name: "delete", method: http.MethodDelete, path: "/api/v1/templates/t3",
			cancelAt: "before:Template.Delete", wantStatus: http.StatusNoContent,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Nil(t, f.templateOf(t, "t3"))
			},
		},
		{
			name: "unpublish", method: http.MethodPost, path: "/api/v1/templates/t1/unpublish",
			cancelAt: "before:Template.Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				assert.False(t, f.templateOf(t, "t1").IsPublished)
			},
		},
		{
			name: "clone, at the template insert", method: http.MethodPost, path: "/api/v1/templates/t1/clone",
			body: `{"name":"copy"}`, cancelAt: "before:Template.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				copies := f.templatesNamed(t, "copy")
				require.Len(t, copies, 1)
				assert.Len(t, f.templateChartsOf(t, copies[0].ID), 1, "the chart is copied")
			},
		},
		{
			name: "clone, at the chart insert", method: http.MethodPost, path: "/api/v1/templates/t1/clone",
			body: `{"name":"copy"}`, cancelAt: "before:TemplateChart.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				copies := f.templatesNamed(t, "copy")
				require.Len(t, copies, 1)
				assert.Len(t, f.templateChartsOf(t, copies[0].ID), 1, "the chart is copied")
			},
		},
		{
			name: "add chart", method: http.MethodPost, path: "/api/v1/templates/t3/charts",
			body: `{"chart_name":"cache","repository_url":"oci://charts/cache"}`, cancelAt: "before:TemplateChart.Create", wantStatus: http.StatusCreated,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Len(t, f.templateChartsOf(t, "t3"), 1)
			},
		},
		{
			name: "update chart", method: http.MethodPut, path: "/api/v1/templates/t1/charts/tc-web",
			body: `{"chart_name":"web","repository_url":"oci://charts/web","default_values":"replicas: 3"}`, cancelAt: "before:TemplateChart.Update", wantStatus: http.StatusOK,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				charts := f.templateChartsOf(t, "t1")
				require.Len(t, charts, 1)
				assert.Equal(t, "replicas: 3", charts[0].DefaultValues)
			},
		},
		{
			name: "delete chart", method: http.MethodDelete, path: "/api/v1/templates/t1/charts/tc-web",
			cancelAt: "before:TemplateChart.Delete", wantStatus: http.StatusNoContent,
			check: func(t *testing.T, f *defCtxFixture, _ *httptest.ResponseRecorder) {
				assert.Empty(t, f.templateChartsOf(t, "t1"))
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

// TestPublishTemplate_RunsOnTheTransactionContext checks that a client that
// leaves during publish cannot fail the transaction (#656): every statement
// of publish (the row lock, the chart list, the snapshot and the template
// update) runs with the context of the transaction, not the request context.
func TestPublishTemplate_RunsOnTheTransactionContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		cancelAt   string
		points     []string
		wantStatus int
	}{
		{
			name: "publish, at the row lock", method: http.MethodPost, path: "/api/v1/templates/t3/publish", body: `{}`,
			cancelAt:   "before:Template.FindByIDForUpdate",
			points:     []string{"before:Template.FindByIDForUpdate", "before:TemplateChart.ListByTemplate", "before:Template.Update"},
			wantStatus: http.StatusOK,
		},
		{
			name: "publish, at the chart list", method: http.MethodPost, path: "/api/v1/templates/t3/publish", body: `{}`,
			cancelAt:   "before:TemplateChart.ListByTemplate",
			points:     []string{"before:TemplateChart.ListByTemplate", "before:Template.Update"},
			wantStatus: http.StatusOK,
		},
		{
			name: "publish, at the template update", method: http.MethodPost, path: "/api/v1/templates/t3/publish", body: `{}`,
			cancelAt:   "before:Template.Update",
			points:     []string{"before:Template.Update"},
			wantStatus: http.StatusOK,
		},
		{
			name: "create published, at the template insert", method: http.MethodPost, path: "/api/v1/templates",
			body:       `{"name":"fresh","version":"1.0.0","is_published":true,"charts":[{"chart_name":"web","repository_url":"oci://charts/web"}]}`,
			cancelAt:   "before:Template.Create",
			points:     []string{"before:TemplateChart.Create", "before:TemplateChart.ListByTemplate", "before:Template.Update"},
			wantStatus: http.StatusCreated,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newDefCtxFixture(t)
			req, cancel := liveRequest(t, tt.method, tt.path, tt.body)
			cancelFirst := cancelAt(tt.cancelAt, 1, cancel)
			seen := map[string]*ctxErrAt{}
			for _, p := range tt.points {
				seen[p] = firstCtxErrAt(p)
			}
			f.hooks.setHook(func(point string, ctx context.Context) {
				cancelFirst(point, ctx)
				if r, ok := seen[point]; ok {
					r.record(point, ctx)
				}
			})

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			require.Error(t, req.Context().Err(), "the request was cancelled during the handler")
			for _, p := range tt.points {
				seen[p].assertLive(t)
			}
			if tt.path == "/api/v1/templates/t3/publish" {
				assert.True(t, f.templateOf(t, "t3").IsPublished)
				versions, err := f.versions.ListByTemplate(context.Background(), "t3")
				require.NoError(t, err)
				assert.Len(t, versions, 1, "the snapshot is stored")
			}
		})
	}
}

// templateReadCases are the template reads of the template handlers, also
// the reads before a template write.
var templateReadCases = []struct {
	name   string
	method string
	path   string
	body   string
	readAt string
}{
	{name: "list", method: http.MethodGet, path: "/api/v1/templates", readAt: "Template.ListPaged"},
	{name: "list by name", method: http.MethodGet, path: "/api/v1/templates?name=Web+stack", readAt: "Template.ListPaged"},
	{name: "get at the template", method: http.MethodGet, path: "/api/v1/templates/t1", readAt: "Template.FindByID"},
	{name: "get at the charts", method: http.MethodGet, path: "/api/v1/templates/t1", readAt: "TemplateChart.ListByTemplate"},
	{name: "update", method: http.MethodPut, path: "/api/v1/templates/t3", body: `{"name":"renamed"}`, readAt: "Template.FindByID"},
	{name: "delete", method: http.MethodDelete, path: "/api/v1/templates/t3", readAt: "Template.FindByID"},
	{name: "publish", method: http.MethodPost, path: "/api/v1/templates/t3/publish", body: `{}`, readAt: "Template.FindByID"},
	{name: "unpublish", method: http.MethodPost, path: "/api/v1/templates/t1/unpublish", readAt: "Template.FindByID"},
	{name: "clone at the template", method: http.MethodPost, path: "/api/v1/templates/t1/clone", body: `{"name":"copy"}`, readAt: "Template.FindByID"},
	{name: "clone at the charts", method: http.MethodPost, path: "/api/v1/templates/t1/clone", body: `{"name":"copy"}`, readAt: "TemplateChart.ListByTemplate"},
	{
		name: "add chart", method: http.MethodPost, path: "/api/v1/templates/t3/charts",
		body: `{"chart_name":"cache","repository_url":"oci://charts/cache"}`, readAt: "Template.FindByID",
	},
	{
		name: "update chart at the chart read", method: http.MethodPut, path: "/api/v1/templates/t1/charts/tc-web",
		body: `{"chart_name":"web","repository_url":"oci://charts/web","default_values":"replicas: 3"}`, readAt: "TemplateChart.FindByID",
	},
	{name: "delete chart at the chart read", method: http.MethodDelete, path: "/api/v1/templates/t1/charts/tc-web", readAt: "TemplateChart.FindByID"},
	{name: "instantiate", method: http.MethodPost, path: "/api/v1/templates/t1/instantiate", body: `{"name":"from-template"}`, readAt: "Template.FindByID"},
}

// TestTemplateRequests_CancelledReadChangesNothing checks that a client that
// leaves at a template read gets 499, that the read got the request context
// and that nothing changes, also for a read before a write (#656).
func TestTemplateRequests_CancelledReadChangesNothing(t *testing.T) {
	t.Parallel()

	for _, tt := range templateReadCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newDefCtxFixture(t)
			before := f.templateState(t)
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
			assert.Equal(t, before, f.templateState(t), "nothing changed")
			assert.Never(t, func() bool { return len(f.audit.snapshot()) > 0 },
				100*time.Millisecond, 10*time.Millisecond, "no audit entry")
		})
	}
}

// TestTemplateRequests_DeadlineAtTemplateRead checks that a passed deadline
// at a template read answers 500, not 499, and changes nothing (#656).
func TestTemplateRequests_DeadlineAtTemplateRead(t *testing.T) {
	t.Parallel()

	for _, tt := range templateReadCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newDefCtxFixture(t)
			before := f.templateState(t)
			req := deadlineRequest(t, tt.method, tt.path, tt.body)
			wait := waitForDeadline("before:" + tt.readAt)
			f.hooks.setHook(wait)

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
			assert.Equal(t, before, f.templateState(t), "nothing changed")
		})
	}
}

// TestBulkTemplateOperations_CancelledAtWriteStillCommit checks that a client
// that leaves at the write of a bulk template operation cannot stop the write
// or the audit entry (#656).
func TestBulkTemplateOperations_CancelledAtWriteStillCommit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		check    func(t *testing.T, f *defCtxFixture)
		name     string
		path     string
		cancelAt string
	}{
		{
			name: "delete", path: "/api/v1/templates/bulk/delete", cancelAt: "before:Template.Delete",
			check: func(t *testing.T, f *defCtxFixture) { assert.Nil(t, f.templateOf(t, "t3")) },
		},
		{
			name: "publish", path: "/api/v1/templates/bulk/publish", cancelAt: "before:Template.FindByIDForUpdate",
			check: func(t *testing.T, f *defCtxFixture) { assert.True(t, f.templateOf(t, "t3").IsPublished) },
		},
		{
			name: "unpublish", path: "/api/v1/templates/bulk/unpublish", cancelAt: "before:Template.Update",
			check: func(t *testing.T, f *defCtxFixture) { assert.False(t, f.templateOf(t, "t1").IsPublished) },
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newDefCtxFixture(t)
			id := "t3"
			if tt.name == "unpublish" {
				id = "t1"
			}
			req, cancel := liveRequest(t, http.MethodPost, tt.path, `{"template_ids":["`+id+`"]}`)
			f.hooks.setHook(cancelAt(tt.cancelAt, 1, cancel))

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Error(t, req.Context().Err(), "the request was cancelled during the handler")
			var resp BulkTemplateResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, 1, resp.Succeeded, w.Body.String())
			tt.check(t, f)
			assert.Eventually(t, func() bool { return len(f.audit.snapshot()) == 1 },
				2*time.Second, 10*time.Millisecond, "one audit entry for the operation")
		})
	}
}

// TestBulkTemplateOperations_CancelledReadReportsCancel checks that a client
// that leaves at the template read of a bulk operation gets a per-template
// "request cancelled" and no write (#656).
func TestBulkTemplateOperations_CancelledReadReportsCancel(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/api/v1/templates/bulk/delete", "/api/v1/templates/bulk/publish", "/api/v1/templates/bulk/unpublish"} {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			f := newDefCtxFixture(t)
			before := f.templateState(t)
			req, cancel := liveRequest(t, http.MethodPost, path, `{"template_ids":["t3"]}`)
			f.hooks.setHook(cancelAt("before:Template.FindByID", 1, cancel))

			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var resp BulkTemplateResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.Len(t, resp.Results, 1)
			assert.Equal(t, 1, resp.Failed)
			assert.Equal(t, "request cancelled", resp.Results[0].Error)
			assert.Equal(t, before, f.templateState(t), "nothing changed")
		})
	}
}

// TestQuickDeploy_CancelledTemplateReadChangesNothing checks that a client
// that leaves at the template read of a quick deploy gets 499 and creates no
// instance or definition (#656).
func TestQuickDeploy_CancelledTemplateReadChangesNothing(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	hooks := newHookInstanceRepo()
	tmplRepo := newHookTemplateRepo(hooks)
	tmplChartRepo := newHookTemplateChartRepo(hooks)
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	ovRepo := NewMockValueOverrideRepository()
	logRepo := NewMockDeploymentLogRepository()
	audit := NewMockAuditLogRepository()
	seedTemplate(t, tmplRepo.MockStackTemplateRepository, "t1", "My Template", "owner-1", true)
	require.NoError(t, tmplChartRepo.Create(context.Background(), &models.TemplateChartConfig{
		ID: "tc1", StackTemplateID: "t1", ChartName: "nginx", RepositoryURL: "oci://example.com/charts/nginx", DeployOrder: 1,
	}))
	mgr := newTestManager(hooks, logRepo)
	t.Cleanup(mgr.Shutdown)
	h, err := NewQuickDeployHandler(
		tmplRepo, tmplChartRepo, defRepo, ccRepo, hooks, boRepo, ovRepo, helm.NewValuesGenerator(),
		mgr, NewMockUserRepository(), logRepo, audit,
		&MockBroadcastSender{}, cluster.NewRegistryForTest("test-cluster", nil, &noopHelmExecutor{}), nil, 0,
		&mockHandlerTxRunner{repos: database.TxRepos{
			StackDefinition: defRepo, ChartConfig: ccRepo, StackInstance: hooks,
			ValueOverride: ovRepo, BranchOverride: boRepo,
		}},
	)
	require.NoError(t, err)
	h.WithTemplateVersions(newWorkingCopyReleaseRepo(tmplRepo.MockStackTemplateRepository, tmplChartRepo.MockTemplateChartConfigRepository))
	r := gin.New()
	r.Use(injectAuthContext("uid-1", "user"))
	r.POST("/api/v1/templates/:id/quick-deploy", h.QuickDeploy)

	req, cancel := liveRequest(t, http.MethodPost, "/api/v1/templates/t1/quick-deploy", `{"instance_name":"qd-cancel"}`)
	cancelFirst := cancelAt("before:Template.FindByID", 1, cancel)
	readCtx := firstCtxErrAt("after:Template.FindByID")
	hooks.setHook(func(point string, ctx context.Context) {
		cancelFirst(point, ctx)
		readCtx.record(point, ctx)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
	readCtx.assertCancelled(t)
	instances, err := hooks.MockStackInstanceRepository.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, instances, "no instance is created")
	defs, err := defRepo.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, defs, "no definition is created")
	assert.Empty(t, audit.snapshot(), "no audit entry")
}
