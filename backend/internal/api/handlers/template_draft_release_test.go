package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/database"
	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// draftReleaseEnv wires the template, version, definition and quick deploy
// handlers over shared mock repositories, to test the "draft and release"
// template life cycle end to end.
type draftReleaseEnv struct {
	router    *gin.Engine
	templates *MockStackTemplateRepository
	charts    *MockTemplateChartConfigRepository
	defs      *MockStackDefinitionRepository
	defCharts *MockChartConfigRepository
	versions  *MockTemplateVersionRepository
	users     *MockUserRepository
	instances *MockStackInstanceRepository
	th        *TemplateHandler
	vh        *TemplateVersionHandler
	dh        *DefinitionHandler
	qh        *QuickDeployHandler
}

func newDraftReleaseEnv(t *testing.T) *draftReleaseEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	env := &draftReleaseEnv{
		templates: NewMockStackTemplateRepository(),
		charts:    NewMockTemplateChartConfigRepository(),
		defs:      NewMockStackDefinitionRepository(),
		defCharts: NewMockChartConfigRepository(),
		versions:  NewMockTemplateVersionRepository(),
		users:     NewMockUserRepository(),
		instances: NewMockStackInstanceRepository(),
	}
	require.NoError(t, env.users.Create(&models.User{ID: "uid-dev", Username: "dana-devops", Role: "devops"}))

	tx := &mockHandlerTxRunner{repos: database.TxRepos{
		StackTemplate:   env.templates,
		TemplateChart:   env.charts,
		TemplateVersion: env.versions,
		StackDefinition: env.defs,
		ChartConfig:     env.defCharts,
		StackInstance:   env.instances,
	}}
	th, err := NewTemplateHandlerWithVersions(env.templates, env.charts, env.defs, env.defCharts, env.versions, tx)
	require.NoError(t, err)
	vh := NewTemplateVersionHandler(env.versions, env.templates).WithTemplateCharts(env.charts).WithUserRepo(env.users)
	dh, err := NewDefinitionHandlerWithVersions(env.defs, env.defCharts, env.instances, env.templates, env.charts, env.versions, tx)
	require.NoError(t, err)
	qh, err := NewQuickDeployHandler(
		env.templates, env.charts, env.defs, env.defCharts, env.instances,
		NewMockChartBranchOverrideRepository(), NewMockValueOverrideRepository(), helm.NewValuesGenerator(),
		nil, env.users, nil, nil, &MockBroadcastSender{}, nil, nil, 0, tx,
	)
	require.NoError(t, err)
	qh.WithTemplateVersions(env.versions)

	env.th, env.vh, env.dh, env.qh = th, vh, dh, qh
	env.router = env.routerAs("uid-dev", "devops")
	return env
}

// routerAs returns a router over the env handlers for the given caller.
func (e *draftReleaseEnv) routerAs(userID, role string) *gin.Engine {
	r := gin.New()
	r.Use(injectAuthContext(userID, role))
	tpl := r.Group("/api/v1/templates")
	tpl.POST("", e.th.CreateTemplate)
	tpl.GET("/:id", e.th.GetTemplate)
	tpl.PUT("/:id", e.th.UpdateTemplate)
	tpl.DELETE("/:id", e.th.DeleteTemplate)
	tpl.POST("/:id/publish", e.th.PublishTemplate)
	tpl.POST("/:id/unpublish", e.th.UnpublishTemplate)
	tpl.POST("/:id/instantiate", e.th.InstantiateTemplate)
	tpl.POST("/:id/quick-deploy", e.qh.QuickDeploy)
	tpl.POST("/:id/charts", e.th.AddTemplateChart)
	tpl.PUT("/:id/charts/:chartId", e.th.UpdateTemplateChart)
	tpl.DELETE("/:id/charts/:chartId", e.th.DeleteTemplateChart)
	tpl.POST("/bulk/publish", e.th.BulkPublishTemplates)
	tpl.GET("/:id/versions", e.vh.ListVersions)
	tpl.GET("/:id/versions/diff", e.vh.DiffVersions)
	tpl.GET("/:id/versions/:versionId", e.vh.GetVersion)
	defs := r.Group("/api/v1/stack-definitions")
	defs.GET("/:id/check-upgrade", e.dh.CheckUpgrade)
	defs.POST("/:id/upgrade", e.dh.ApplyUpgrade)
	return r
}

// seedDraft creates template t1 (version 1.0.0, unpublished) owned by
// uid-dev with one chart "web" (ID tc-web).
func (e *draftReleaseEnv) seedDraft(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, e.templates.Create(&models.StackTemplate{
		ID: "t1", Name: "Web stack", Version: "1.0.0", OwnerID: "uid-dev", DefaultBranch: "main",
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, e.charts.Create(&models.TemplateChartConfig{
		ID: "tc-web", StackTemplateID: "t1", ChartName: "web", RepositoryURL: "oci://charts/web",
		ChartVersion: "1.0.0", DefaultValues: "replicas: 1", LockedValues: "tls: true", DeployOrder: 1, CreatedAt: now,
	}))
}

// editChart changes the working copy of chart tc-web.
func (e *draftReleaseEnv) editChart(t *testing.T, defaults, chartVersion string) {
	t.Helper()
	body := `{"chart_name":"web","repository_url":"oci://charts/web","chart_version":"` + chartVersion +
		`","default_values":"` + defaults + `","locked_values":"tls: true","deploy_order":1}`
	w := serve(e.router, http.MethodPut, "/api/v1/templates/t1/charts/tc-web", body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// setWorkingVersion changes the version field of the working copy.
func (e *draftReleaseEnv) setWorkingVersion(t *testing.T, version string) {
	t.Helper()
	w := serve(e.router, http.MethodPut, "/api/v1/templates/t1",
		`{"name":"Web stack","version":"`+version+`","default_branch":"main"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func (e *draftReleaseEnv) publish(t *testing.T, body string) (*httptest.ResponseRecorder, publishTemplateResponse) {
	t.Helper()
	w := serve(e.router, http.MethodPost, "/api/v1/templates/t1/publish", body)
	var resp publishTemplateResponse
	if w.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	}
	return w, resp
}

func (e *draftReleaseEnv) versionCount(t *testing.T) int {
	t.Helper()
	list, err := e.versions.ListByTemplate(t.Context(), "t1")
	require.NoError(t, err)
	return len(list)
}

func (e *draftReleaseEnv) getTemplate(t *testing.T) TemplateDetailResponse {
	t.Helper()
	w := serve(e.router, http.MethodGet, "/api/v1/templates/t1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp TemplateDetailResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

// instantiate runs Use Template and returns the new definition with charts.
func (e *draftReleaseEnv) instantiate(t *testing.T, name, extra string) (*httptest.ResponseRecorder, DefinitionWithChartsResponse) {
	t.Helper()
	w := serve(e.router, http.MethodPost, "/api/v1/templates/t1/instantiate", `{"name":"`+name+`"`+extra+`}`)
	var resp DefinitionWithChartsResponse
	if w.Code == http.StatusCreated {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	}
	return w, resp
}

func TestDraftRelease_Publish(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T, e *draftReleaseEnv)
	}{
		{
			name: "first publish without body creates snapshot of working copy version",
			run: func(t *testing.T, e *draftReleaseEnv) {
				w, resp := e.publish(t, "")
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				assert.True(t, resp.SnapshotCreated)
				assert.True(t, resp.IsPublished)
				assert.Equal(t, "1.0.0", resp.PublishedVersion)
				assert.NotEmpty(t, resp.PublishedVersionID)
				assert.Equal(t, 1, e.versionCount(t))
			},
		},
		{
			name: "publish without changes is idempotent",
			run: func(t *testing.T, e *draftReleaseEnv) {
				_, first := e.publish(t, "")
				w, resp := e.publish(t, "")
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				assert.False(t, resp.SnapshotCreated)
				assert.Equal(t, first.PublishedVersionID, resp.PublishedVersionID)
				assert.Equal(t, 1, e.versionCount(t))
				// Unpublish + publish without changes: still no new snapshot.
				require.Equal(t, http.StatusOK, serve(e.router, http.MethodPost, "/api/v1/templates/t1/unpublish", "").Code)
				w, resp = e.publish(t, `{"version":"1.0.0"}`)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				assert.False(t, resp.SnapshotCreated)
				assert.True(t, resp.IsPublished)
				assert.Equal(t, 1, e.versionCount(t))
			},
		},
		{
			name: "changed content with an existing version gives 409",
			run: func(t *testing.T, e *draftReleaseEnv) {
				e.publish(t, "")
				e.editChart(t, "replicas: 2", "1.0.0")
				w, _ := e.publish(t, "")
				assert.Equal(t, http.StatusConflict, w.Code)
				assert.JSONEq(t, `{"error":"Version 1.0.0 already exists"}`, w.Body.String())
				assert.Equal(t, 1, e.versionCount(t))
			},
		},
		{
			name: "an older version string gives 409",
			run: func(t *testing.T, e *draftReleaseEnv) {
				e.publish(t, "")
				e.editChart(t, "replicas: 2", "1.0.0")
				w, _ := e.publish(t, `{"version":"1.1.0"}`)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				e.editChart(t, "replicas: 3", "1.0.0")
				w, _ = e.publish(t, `{"version":"1.0.0"}`)
				assert.Equal(t, http.StatusConflict, w.Code)
				assert.Equal(t, 2, e.versionCount(t))
			},
		},
		{
			name: "publish with body version sets the working copy version and stores the summary",
			run: func(t *testing.T, e *draftReleaseEnv) {
				e.publish(t, "")
				e.editChart(t, "replicas: 2", "1.0.0")
				w, resp := e.publish(t, `{"version":"2.0.0","change_summary":"two replicas"}`)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				assert.True(t, resp.SnapshotCreated)
				assert.Equal(t, "2.0.0", resp.Version)
				assert.Equal(t, "2.0.0", resp.PublishedVersion)
				latest, err := e.versions.GetLatestByTemplate(t.Context(), "t1")
				require.NoError(t, err)
				assert.Equal(t, "two replicas", latest.ChangeSummary)
				assert.Equal(t, "uid-dev", latest.CreatedBy)
				assert.False(t, e.getTemplate(t).HasUnpublishedChanges)
			},
		},
		{
			name: "invalid JSON body gives 400",
			run: func(t *testing.T, e *draftReleaseEnv) {
				w, _ := e.publish(t, `{"version":`)
				assert.Equal(t, http.StatusBadRequest, w.Code)
			},
		},
		{
			name: "too long version gives 400",
			run: func(t *testing.T, e *draftReleaseEnv) {
				long := make([]byte, maxPublishVersionLength+1)
				for i := range long {
					long[i] = '1'
				}
				w, _ := e.publish(t, `{"version":"`+string(long)+`"}`)
				assert.Equal(t, http.StatusBadRequest, w.Code)
				assert.Equal(t, 0, e.versionCount(t))
			},
		},
		{
			name: "unpublish keeps history and blocks use",
			run: func(t *testing.T, e *draftReleaseEnv) {
				e.publish(t, "")
				w := serve(e.router, http.MethodPost, "/api/v1/templates/t1/unpublish", "")
				require.Equal(t, http.StatusOK, w.Code)
				assert.Equal(t, 1, e.versionCount(t))
				w, _ = e.instantiate(t, "after-unpublish", "")
				assert.Equal(t, http.StatusConflict, w.Code)
				assert.JSONEq(t, `{"error":"Template has no published version"}`, w.Body.String())
				// GET still shows the latest snapshot.
				detail := e.getTemplate(t)
				require.NotNil(t, detail.PublishedVersion)
				assert.Equal(t, "1.0.0", *detail.PublishedVersion)
				assert.False(t, detail.IsPublished)
			},
		},
		{
			name: "snapshot write error gives 500 and leaves the template unpublished",
			run: func(t *testing.T, e *draftReleaseEnv) {
				e.versions.SetError(errors.New("db down"))
				w, _ := e.publish(t, "")
				assert.Equal(t, http.StatusInternalServerError, w.Code)
				assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
				tmpl, err := e.templates.FindByID("t1")
				require.NoError(t, err)
				assert.False(t, tmpl.IsPublished)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newDraftReleaseEnv(t)
			e.seedDraft(t)
			tt.run(t, e)
		})
	}
}

func TestDraftRelease_UsersGetLatestSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T, e *draftReleaseEnv)
	}{
		{
			name: "never published template cannot be instantiated",
			run: func(t *testing.T, e *draftReleaseEnv) {
				w, _ := e.instantiate(t, "d", "")
				assert.Equal(t, http.StatusConflict, w.Code)
			},
		},
		{
			name: "edit of a published template does not change instantiate",
			run: func(t *testing.T, e *draftReleaseEnv) {
				e.publish(t, "")
				e.editChart(t, "replicas: 5", "9.9.9")
				e.setWorkingVersion(t, "1.1.0")
				w, def := e.instantiate(t, "old-content", "")
				require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
				assert.Equal(t, "1.0.0", def.SourceTemplateVersion)
				require.Len(t, def.Charts, 1)
				assert.Equal(t, "replicas: 1", def.Charts[0].DefaultValues)
				assert.Equal(t, "1.0.0", def.Charts[0].ChartVersion)
			},
		},
		{
			name: "publish of a new version is used by instantiate",
			run: func(t *testing.T, e *draftReleaseEnv) {
				e.publish(t, "")
				e.editChart(t, "replicas: 5", "2.0.0")
				w, _ := e.publish(t, `{"version":"1.1.0"}`)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				w, def := e.instantiate(t, "new-content", "")
				require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
				assert.Equal(t, "1.1.0", def.SourceTemplateVersion)
				require.Len(t, def.Charts, 1)
				assert.Equal(t, "replicas: 5", def.Charts[0].DefaultValues)
				assert.Equal(t, "2.0.0", def.Charts[0].ChartVersion)
			},
		},
		{
			name: "chart_overrides by published chart ID, working chart ID or name; unknown keys ignored",
			run: func(t *testing.T, e *draftReleaseEnv) {
				e.publish(t, "")
				for _, key := range []string{"tc-web", "web"} {
					w, def := e.instantiate(t, "ovr-"+key, `,"chart_overrides":{"`+key+`":"replicas: 7","nope":"x"}`)
					require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
					require.Len(t, def.Charts, 1)
					assert.Equal(t, "replicas: 7", def.Charts[0].DefaultValues, key)
				}
			},
		},
		{
			name: "chart_overrides keyed by a working copy chart ID map by chart name",
			run: func(t *testing.T, e *draftReleaseEnv) {
				// Legacy snapshot (no chart IDs): the working copy ID maps by name.
				snap := makeSnapshotJSON(t, models.TemplateSnapshotData{Name: "Web stack", Version: "1.0.0"},
					[]models.TemplateChartSnapshotData{{ChartName: "web", DefaultValues: "replicas: 1", SortOrder: 1}})
				seedVersion(t, e.versions, "v-legacy", "t1", "1.0.0", snap, time.Now())
				tmpl, _ := e.templates.FindByID("t1")
				tmpl.IsPublished = true
				require.NoError(t, e.templates.Update(tmpl))
				detail := e.getTemplate(t)
				require.Len(t, detail.PublishedCharts, 1)
				assert.Equal(t, "tc-web", detail.PublishedCharts[0].ID, "legacy snapshot charts take the working copy ID")
				w, def := e.instantiate(t, "ovr-working", `,"chart_overrides":{"tc-web":"replicas: 4"}`)
				require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
				assert.Equal(t, "replicas: 4", def.Charts[0].DefaultValues)
				assert.Equal(t, "1.0.0", def.Charts[0].ChartVersion, "legacy snapshot fills chart_version from the working copy")
			},
		},
		{
			name: "quick deploy uses the latest snapshot",
			run: func(t *testing.T, e *draftReleaseEnv) {
				e.publish(t, "")
				e.editChart(t, "replicas: 5", "9.9.9")
				w := serve(e.router, http.MethodPost, "/api/v1/templates/t1/quick-deploy", `{"instance_name":"qd-one"}`)
				require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
				var resp quickDeployResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "1.0.0", resp.Definition.SourceTemplateVersion)
				assert.Equal(t, "main", resp.Instance.Branch)
				charts, err := e.defCharts.ListByDefinition(resp.Definition.ID)
				require.NoError(t, err)
				require.Len(t, charts, 1)
				assert.Equal(t, "replicas: 1", charts[0].DefaultValues)
				assert.Equal(t, "1.0.0", charts[0].ChartVersion)
			},
		},
		{
			name: "quick deploy of an unpublished template gives 409",
			run: func(t *testing.T, e *draftReleaseEnv) {
				w := serve(e.router, http.MethodPost, "/api/v1/templates/t1/quick-deploy", `{"instance_name":"qd-two"}`)
				assert.Equal(t, http.StatusConflict, w.Code)
			},
		},
		{
			name: "check-upgrade and upgrade read the snapshot, not the working copy",
			run: func(t *testing.T, e *draftReleaseEnv) {
				e.publish(t, "")
				_, def := e.instantiate(t, "upgrade-me", "")
				// Draft edits only: no upgrade.
				e.editChart(t, "replicas: 3", "3.0.0")
				w := serve(e.router, http.MethodGet, "/api/v1/stack-definitions/"+def.ID+"/check-upgrade", "")
				require.Equal(t, http.StatusOK, w.Code)
				assert.JSONEq(t, `{"upgrade_available":false}`, w.Body.String())

				w, _ = e.publish(t, `{"version":"1.1.0"}`)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				// More draft edits after the release must not leak.
				e.editChart(t, "replicas: 99", "99.0.0")

				w = serve(e.router, http.MethodGet, "/api/v1/stack-definitions/"+def.ID+"/check-upgrade", "")
				require.Equal(t, http.StatusOK, w.Code)
				var check upgradeCheckResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &check))
				assert.True(t, check.UpgradeAvailable)
				assert.Equal(t, "1.1.0", check.LatestVersion)
				require.Len(t, check.ChartDiffs, 1)
				assert.Equal(t, "modified", check.ChartDiffs[0].ChangeType)
				assert.Equal(t, "replicas: 1", check.ChartDiffs[0].LeftValues)
				assert.Equal(t, "replicas: 3", check.ChartDiffs[0].RightValues)
				assert.Equal(t, "tls: true", check.ChartDiffs[0].LeftLocked)
				assert.Equal(t, "tls: true", check.ChartDiffs[0].RightLocked)
				assert.Equal(t, "3.0.0", check.ChartDiffs[0].RightChartVersion)

				w = serve(e.router, http.MethodPost, "/api/v1/stack-definitions/"+def.ID+"/upgrade", "{}")
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				var up DefinitionWithChartsResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &up))
				assert.Equal(t, "1.1.0", up.SourceTemplateVersion)
				require.Len(t, up.Charts, 1)
				assert.Equal(t, "replicas: 3", up.Charts[0].DefaultValues)
				assert.Equal(t, "3.0.0", up.Charts[0].ChartVersion)
			},
		},
		{
			name: "upgrade of an unpublished template gives 409",
			run: func(t *testing.T, e *draftReleaseEnv) {
				e.publish(t, "")
				_, def := e.instantiate(t, "upgrade-blocked", "")
				e.editChart(t, "replicas: 3", "3.0.0")
				e.publish(t, `{"version":"1.1.0"}`)
				require.Equal(t, http.StatusOK, serve(e.router, http.MethodPost, "/api/v1/templates/t1/unpublish", "").Code)
				w := serve(e.router, http.MethodGet, "/api/v1/stack-definitions/"+def.ID+"/check-upgrade", "")
				assert.JSONEq(t, `{"upgrade_available":false}`, w.Body.String())
				w = serve(e.router, http.MethodPost, "/api/v1/stack-definitions/"+def.ID+"/upgrade", "{}")
				assert.Equal(t, http.StatusConflict, w.Code)
			},
		},
		{
			name: "locked values come from the latest snapshot",
			run: func(t *testing.T, e *draftReleaseEnv) {
				e.publish(t, "")
				body := `{"chart_name":"web","repository_url":"oci://charts/web","chart_version":"1.0.0","default_values":"replicas: 1","locked_values":"tls: false","deploy_order":1}`
				require.Equal(t, http.StatusOK, serve(e.router, http.MethodPut, "/api/v1/templates/t1/charts/tc-web", body).Code)
				b := &valuesBuilder{templateChartRepo: e.charts, versionRepo: e.versions}
				locked, err := b.lockedValues(t.Context(), &models.StackDefinition{SourceTemplateID: "t1"})
				require.NoError(t, err)
				assert.Equal(t, "tls: true", locked["web"])
				// Without a version repository the working copy is used (legacy).
				legacy := &valuesBuilder{templateChartRepo: e.charts}
				locked, err = legacy.lockedValues(t.Context(), &models.StackDefinition{SourceTemplateID: "t1"})
				require.NoError(t, err)
				assert.Equal(t, "tls: false", locked["web"])
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newDraftReleaseEnv(t)
			e.seedDraft(t)
			tt.run(t, e)
		})
	}
}

func TestDraftRelease_TemplateDetailAndDiff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T, e *draftReleaseEnv)
	}{
		{
			name: "never published: published_version null, has_unpublished_changes true",
			run: func(t *testing.T, e *draftReleaseEnv) {
				w := serve(e.router, http.MethodGet, "/api/v1/templates/t1", "")
				require.Equal(t, http.StatusOK, w.Code)
				var raw map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
				assert.Nil(t, raw["published_version"])
				assert.Nil(t, raw["published_version_id"])
				assert.Nil(t, raw["published_charts"])
				assert.Equal(t, true, raw["has_unpublished_changes"])
			},
		},
		{
			name: "after publish: no unpublished changes; after edit: changes",
			run: func(t *testing.T, e *draftReleaseEnv) {
				_, pub := e.publish(t, "")
				d := e.getTemplate(t)
				require.NotNil(t, d.PublishedVersion)
				assert.Equal(t, "1.0.0", *d.PublishedVersion)
				assert.Equal(t, pub.PublishedVersionID, *d.PublishedVersionID)
				assert.False(t, d.HasUnpublishedChanges)
				require.Len(t, d.PublishedCharts, 1)
				assert.Equal(t, "tc-web", d.PublishedCharts[0].ID)

				e.editChart(t, "replicas: 2", "1.0.0")
				d = e.getTemplate(t)
				assert.True(t, d.HasUnpublishedChanges)
				assert.Equal(t, "replicas: 2", d.Charts[0].DefaultValues, "charts are the working copy")
				assert.Equal(t, "replicas: 1", d.PublishedCharts[0].DefaultValues, "published_charts are the snapshot")
			},
		},
		{
			name: "version-only change is an unpublished change",
			run: func(t *testing.T, e *draftReleaseEnv) {
				e.publish(t, "")
				e.setWorkingVersion(t, "1.0.1")
				assert.True(t, e.getTemplate(t).HasUnpublishedChanges)
			},
		},
		{
			name: "diff latest vs working copy",
			run: func(t *testing.T, e *draftReleaseEnv) {
				_, pub := e.publish(t, "")
				e.editChart(t, "replicas: 2", "1.0.0")
				w := serve(e.router, http.MethodGet, "/api/v1/templates/t1/versions/diff?left="+pub.PublishedVersionID+"&right=working", "")
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				var resp versionDiffResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "1.0.0", resp.Left.Version)
				assert.Equal(t, pub.PublishedVersionID, resp.Left.ID)
				assert.Equal(t, "dana-devops", resp.Left.CreatedByUsername)
				assert.False(t, resp.Left.IsWorkingCopy)
				assert.Equal(t, "working", resp.Right.ID)
				assert.True(t, resp.Right.IsWorkingCopy)
				require.Len(t, resp.ChartDiffs, 1)
				assert.Equal(t, "modified", resp.ChartDiffs[0].ChangeType)
				assert.Equal(t, "replicas: 1", resp.ChartDiffs[0].LeftValues)
				assert.Equal(t, "replicas: 2", resp.ChartDiffs[0].RightValues)
				assert.Empty(t, resp.TemplateDiffs, "no template field changed")
				assert.NotNil(t, resp.TemplateDiffs, "template_diffs is a list, not null")
			},
		},
		{
			name: "diff latest vs working copy shows changed template fields (#499)",
			run: func(t *testing.T, e *draftReleaseEnv) {
				_, pub := e.publish(t, "")
				w := serve(e.router, http.MethodPut, "/api/v1/templates/t1",
					`{"description":"New text","category":"Web"}`)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.True(t, e.getTemplate(t).HasUnpublishedChanges)

				w = serve(e.router, http.MethodGet, "/api/v1/templates/t1/versions/diff?left="+pub.PublishedVersionID+"&right=working", "")
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				var resp versionDiffResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, []templateFieldDiff{
					{Field: "description", Left: "", Right: "New text"},
					{Field: "category", Left: "", Right: "Web"},
				}, resp.TemplateDiffs)
				require.Len(t, resp.ChartDiffs, 1)
				assert.Equal(t, "unchanged", resp.ChartDiffs[0].ChangeType)
			},
		},
		{
			name: "diff left.version stays a string",
			run: func(t *testing.T, e *draftReleaseEnv) {
				_, pub := e.publish(t, "")
				w := serve(e.router, http.MethodGet, "/api/v1/templates/t1/versions/diff?left=working&right="+pub.PublishedVersionID, "")
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				var raw struct {
					Left  map[string]any `json:"left"`
					Right map[string]any `json:"right"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
				assert.IsType(t, "", raw.Left["version"])
				assert.IsType(t, "", raw.Right["version"])
				assert.Equal(t, true, raw.Left["is_working_copy"])
			},
		},
		{
			name: "diff unknown version gives 404",
			run: func(t *testing.T, e *draftReleaseEnv) {
				w := serve(e.router, http.MethodGet, "/api/v1/templates/t1/versions/diff?left=missing&right=working", "")
				assert.Equal(t, http.StatusNotFound, w.Code)
			},
		},
		{
			name: "version list and detail include created_by_username",
			run: func(t *testing.T, e *draftReleaseEnv) {
				_, pub := e.publish(t, "")
				seedVersion(t, e.versions, "v-ghost", "t1", "0.9.0", makeSnapshotJSON(t, models.TemplateSnapshotData{}, nil), time.Now().Add(-time.Hour))
				w := serve(e.router, http.MethodGet, "/api/v1/templates/t1/versions", "")
				require.Equal(t, http.StatusOK, w.Code)
				var list []map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
				require.Len(t, list, 2)
				assert.Equal(t, "dana-devops", list[0]["created_by_username"])
				_, has := list[1]["created_by_username"]
				assert.False(t, has, "unknown author: field omitted")

				w = serve(e.router, http.MethodGet, "/api/v1/templates/t1/versions/"+pub.PublishedVersionID, "")
				require.Equal(t, http.StatusOK, w.Code)
				var detail versionDetailResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &detail))
				assert.Equal(t, "dana-devops", detail.CreatedByUsername)
				assert.Equal(t, models.TemplateSnapshotSchemaVersion, detail.Snapshot.SchemaVersion)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newDraftReleaseEnv(t)
			e.seedDraft(t)
			tt.run(t, e)
		})
	}
}

func TestDraftRelease_CreateAndBulkPublish(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T, e *draftReleaseEnv)
	}{
		{
			name: "create with is_published creates the first snapshot",
			run: func(t *testing.T, e *draftReleaseEnv) {
				w := serve(e.router, http.MethodPost, "/api/v1/templates",
					`{"name":"New","version":"0.1.0","is_published":true,"charts":[{"chart_name":"api","default_values":"a: 1"}]}`)
				require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
				var resp TemplateDetailResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.True(t, resp.IsPublished)
				require.NotNil(t, resp.PublishedVersion)
				assert.Equal(t, "0.1.0", *resp.PublishedVersion)
				assert.False(t, resp.HasUnpublishedChanges)
				list, err := e.versions.ListByTemplate(t.Context(), resp.ID)
				require.NoError(t, err)
				assert.Len(t, list, 1)
			},
		},
		{
			name: "create with is_published and no version gives 400",
			run: func(t *testing.T, e *draftReleaseEnv) {
				w := serve(e.router, http.MethodPost, "/api/v1/templates", `{"name":"New","is_published":true}`)
				assert.Equal(t, http.StatusBadRequest, w.Code)
			},
		},
		{
			name: "bulk publish follows the publish rules",
			run: func(t *testing.T, e *draftReleaseEnv) {
				body := `{"template_ids":["t1"]}`
				w := serve(e.router, http.MethodPost, "/api/v1/templates/bulk/publish", body)
				require.Equal(t, http.StatusOK, w.Code)
				assert.Equal(t, 1, e.versionCount(t))
				// No change: success, no new snapshot.
				w = serve(e.router, http.MethodPost, "/api/v1/templates/bulk/publish", body)
				var resp BulkTemplateResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Succeeded)
				assert.Equal(t, 1, e.versionCount(t))
				// Change with the same version: per-template error.
				e.editChart(t, "replicas: 2", "1.0.0")
				w = serve(e.router, http.MethodPost, "/api/v1/templates/bulk/publish", body)
				resp = BulkTemplateResponse{}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, 1, resp.Failed)
				assert.Equal(t, "Version 1.0.0 already exists", resp.Results[0].Error)
				assert.Equal(t, 1, e.versionCount(t))
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newDraftReleaseEnv(t)
			e.seedDraft(t)
			tt.run(t, e)
		})
	}
}

func TestSameTemplateContent(t *testing.T) {
	t.Parallel()
	base := func() models.TemplateSnapshot {
		return models.TemplateSnapshot{
			SchemaVersion: models.TemplateSnapshotSchemaVersion,
			Template:      models.TemplateSnapshotData{Name: "T", Version: "1.0.0", IsPublished: true},
			Charts: []models.TemplateChartSnapshotData{
				{ID: "a", ChartName: "a", SortOrder: 1, DefaultValues: "x: 1\n", ChartVersion: "1"},
				{ID: "b", ChartName: "b", SortOrder: 2},
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(s *models.TemplateSnapshot)
		want   bool
	}{
		{"identical", func(_ *models.TemplateSnapshot) {}, true},
		{"publish state is not content", func(s *models.TemplateSnapshot) { s.Template.IsPublished = false }, true},
		{"chart IDs are not content", func(s *models.TemplateSnapshot) { s.Charts[0].ID = "other" }, true},
		{"chart order is normalized", func(s *models.TemplateSnapshot) { s.Charts[0], s.Charts[1] = s.Charts[1], s.Charts[0] }, true},
		{"trailing space is ignored", func(s *models.TemplateSnapshot) { s.Charts[0].DefaultValues = "x: 1 \r\n\n" }, true},
		{"leading space is content", func(s *models.TemplateSnapshot) { s.Charts[0].DefaultValues = "  x: 1\n" }, false},
		{"version differs", func(s *models.TemplateSnapshot) { s.Template.Version = "1.0.1" }, false},
		{"values differ", func(s *models.TemplateSnapshot) { s.Charts[0].DefaultValues = "x: 2" }, false},
		{"chart version differs", func(s *models.TemplateSnapshot) { s.Charts[0].ChartVersion = "2" }, false},
		{"legacy snapshot ignores chart version", func(s *models.TemplateSnapshot) {
			s.SchemaVersion = 0
			s.Charts[0].ChartVersion = ""
		}, true},
		{"chart removed", func(s *models.TemplateSnapshot) { s.Charts = s.Charts[:1] }, false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			other := base()
			tt.mutate(&other)
			assert.Equal(t, tt.want, models.SameTemplateContent(base(), other))
		})
	}
}

func TestTemplateOwnerOrAdmin(t *testing.T) {
	t.Parallel()

	// t1 is owned by uid-dev (devops).
	calls := []struct {
		name, method, path, body string
		okStatus                 int
	}{
		{"update", http.MethodPut, "/api/v1/templates/t1", `{"description":"x"}`, http.StatusOK},
		{"publish", http.MethodPost, "/api/v1/templates/t1/publish", "", http.StatusOK},
		{"unpublish", http.MethodPost, "/api/v1/templates/t1/unpublish", "", http.StatusOK},
		{"add chart", http.MethodPost, "/api/v1/templates/t1/charts", `{"chart_name":"api"}`, http.StatusCreated},
		{"update chart", http.MethodPut, "/api/v1/templates/t1/charts/tc-web", `{"chart_name":"web","default_values":"replicas: 2"}`, http.StatusOK},
		{"delete chart", http.MethodDelete, "/api/v1/templates/t1/charts/tc-web", "", http.StatusNoContent},
		{"delete", http.MethodDelete, "/api/v1/templates/t1", "", http.StatusNoContent},
	}
	callers := []struct {
		name, userID, role string
		allowed            bool
	}{
		{"owner devops", "uid-dev", "devops", true},
		{"other devops", "uid-other", "devops", false},
		{"admin", "uid-admin", "admin", true},
	}
	for _, call := range calls {
		for _, caller := range callers {
			call, caller := call, caller
			t.Run(call.name+"/"+caller.name, func(t *testing.T) {
				t.Parallel()
				e := newDraftReleaseEnv(t)
				e.seedDraft(t)
				w := serve(e.routerAs(caller.userID, caller.role), call.method, call.path, call.body)
				if caller.allowed {
					assert.Equal(t, call.okStatus, w.Code, w.Body.String())
					return
				}
				assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
				assert.JSONEq(t, `{"error":"`+msgTemplateForbidden+`"}`, w.Body.String())
				// Nothing changed.
				tmpl, err := e.templates.FindByID("t1")
				require.NoError(t, err)
				assert.False(t, tmpl.IsPublished)
				assert.Equal(t, 0, e.versionCount(t))
				charts, err := e.charts.ListByTemplate("t1")
				require.NoError(t, err)
				assert.Len(t, charts, 1)
			})
		}
	}

	t.Run("chart of another template gives 404", func(t *testing.T) {
		t.Parallel()
		e := newDraftReleaseEnv(t)
		e.seedDraft(t)
		require.NoError(t, e.templates.Create(&models.StackTemplate{ID: "t2", Name: "Other", OwnerID: "uid-dev"}))
		w := serve(e.router, http.MethodDelete, "/api/v1/templates/t2/charts/tc-web", "")
		assert.Equal(t, http.StatusNotFound, w.Code)
		w = serve(e.router, http.MethodPut, "/api/v1/templates/t2/charts/tc-web", `{"chart_name":"web"}`)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestUpdateTemplate_Partial(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		body  string
		check func(t *testing.T, got models.StackTemplate)
		want  int
	}{
		{
			name: "only description changes",
			body: `{"description":"new text"}`,
			want: http.StatusOK,
			check: func(t *testing.T, got models.StackTemplate) {
				assert.Equal(t, "new text", got.Description)
				assert.Equal(t, "Web stack", got.Name)
				assert.Equal(t, "1.0.0", got.Version)
				assert.Equal(t, "main", got.DefaultBranch)
				assert.Equal(t, "Web", got.Category)
			},
		},
		{
			name: "version is trimmed",
			body: `{"version":"  1.2.0 \n"}`,
			want: http.StatusOK,
			check: func(t *testing.T, got models.StackTemplate) {
				assert.Equal(t, "1.2.0", got.Version)
				assert.Equal(t, "Web", got.Category)
			},
		},
		{
			name: "explicit empty field is set",
			body: `{"category":""}`,
			want: http.StatusOK,
			check: func(t *testing.T, got models.StackTemplate) {
				assert.Equal(t, "", got.Category)
				assert.Equal(t, "1.0.0", got.Version)
			},
		},
		{
			name: "empty name is rejected",
			body: `{"name":""}`,
			want: http.StatusBadRequest,
		},
		{
			name: "publish state is not writable",
			body: `{"is_published":true}`,
			want: http.StatusOK,
			check: func(t *testing.T, got models.StackTemplate) {
				assert.False(t, got.IsPublished)
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newDraftReleaseEnv(t)
			e.seedDraft(t)
			tmpl, err := e.templates.FindByID("t1")
			require.NoError(t, err)
			tmpl.Category = "Web"
			require.NoError(t, e.templates.Update(tmpl))

			w := serve(e.router, http.MethodPut, "/api/v1/templates/t1", tt.body)
			require.Equal(t, tt.want, w.Code, w.Body.String())
			if tt.check != nil {
				got, err := e.templates.FindByID("t1")
				require.NoError(t, err)
				tt.check(t, *got)
			}
		})
	}
}

func TestPublishAndCreate_TrimAndRollback(t *testing.T) {
	t.Parallel()

	t.Run("publish trims the version", func(t *testing.T) {
		t.Parallel()
		e := newDraftReleaseEnv(t)
		e.seedDraft(t)
		w, resp := e.publish(t, `{"version":" 1.5.0 "}`)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "1.5.0", resp.PublishedVersion)
		assert.Equal(t, "1.5.0", resp.Version)
	})

	t.Run("blank version gives 400", func(t *testing.T) {
		t.Parallel()
		e := newDraftReleaseEnv(t)
		e.seedDraft(t)
		e.setWorkingVersion(t, "  ")
		w, _ := e.publish(t, "")
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("create with is_published returns the publish error", func(t *testing.T) {
		t.Parallel()
		e := newDraftReleaseEnv(t)
		e.versions.SetError(errors.New("db down"))
		w := serve(e.router, http.MethodPost, "/api/v1/templates", `{"name":"New","version":"0.1.0","is_published":true}`)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
	})

	t.Run("create with is_published trims the version", func(t *testing.T) {
		t.Parallel()
		e := newDraftReleaseEnv(t)
		w := serve(e.router, http.MethodPost, "/api/v1/templates", `{"name":"New","version":" 0.2.0 ","is_published":true}`)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		var resp TemplateDetailResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "0.2.0", resp.Version)
		require.NotNil(t, resp.PublishedVersion)
		assert.Equal(t, "0.2.0", *resp.PublishedVersion)
		assert.NotNil(t, resp.PublishedCharts)
	})
}

func TestComputeTemplateFieldDiffs(t *testing.T) {
	t.Parallel()

	base := models.TemplateSnapshotData{
		Name: "Web", Description: "d", Category: "c", DefaultBranch: "main", IsPublished: true, Version: "1.0.0",
	}
	tests := []struct {
		name  string
		right models.TemplateSnapshotData
		want  []templateFieldDiff
	}{
		{name: "equal", right: base, want: []templateFieldDiff{}},
		{
			name:  "publish state is not content",
			right: func() models.TemplateSnapshotData { r := base; r.IsPublished = false; return r }(),
			want:  []templateFieldDiff{},
		},
		{
			name: "all fields in fixed order",
			right: models.TemplateSnapshotData{
				Name: "Web 2", Description: "", Category: "x", DefaultBranch: "develop", IsPublished: true, Version: "1.1.0",
			},
			want: []templateFieldDiff{
				{Field: "name", Left: "Web", Right: "Web 2"},
				{Field: "description", Left: "d", Right: ""},
				{Field: "category", Left: "c", Right: "x"},
				{Field: "default_branch", Left: "main", Right: "develop"},
				{Field: "version", Left: "1.0.0", Right: "1.1.0"},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := computeTemplateFieldDiffs(base, tt.right)
			assert.NotNil(t, got)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestComputeChartDiffs_TrailingWhitespace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		left, right string
		want        string
	}{
		{"trailing newline only", "a: 1", "a: 1\n\n", "unchanged"},
		{"trailing spaces and CRLF", "a: 1 \r\n", "a: 1", "unchanged"},
		{"leading space differs", "a: 1", "  a: 1", "modified"},
		{"value differs", "a: 1", "a: 2", "modified"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			diffs := computeChartDiffs(
				[]models.TemplateChartSnapshotData{{ChartName: "web", DefaultValues: tt.left, LockedValues: tt.left}},
				[]models.TemplateChartSnapshotData{{ChartName: "web", DefaultValues: tt.right, LockedValues: tt.right}},
				true,
			)
			require.Len(t, diffs, 1)
			assert.Equal(t, tt.want, diffs[0].ChangeType)
			// Same rule as SameTemplateContent.
			l := models.TemplateSnapshot{Charts: []models.TemplateChartSnapshotData{{ChartName: "web", DefaultValues: tt.left}}}
			r := models.TemplateSnapshot{Charts: []models.TemplateChartSnapshotData{{ChartName: "web", DefaultValues: tt.right}}}
			assert.Equal(t, tt.want == "unchanged", models.SameTemplateContent(l, r))
		})
	}
}

// seedSchemaSnapshot stores a snapshot of the given format for t1.
func seedSchemaSnapshot(t *testing.T, e *draftReleaseEnv, id, version string, schemaVersion int, chart models.TemplateChartSnapshotData, createdAt time.Time) {
	t.Helper()
	snap := models.TemplateSnapshot{
		SchemaVersion: schemaVersion,
		Template:      models.TemplateSnapshotData{Name: "Web stack", Version: version, DefaultBranch: "main"},
		Charts:        []models.TemplateChartSnapshotData{chart},
	}
	b, err := json.Marshal(snap)
	require.NoError(t, err)
	seedVersion(t, e.versions, id, "t1", version, string(b), createdAt)
}

func TestDraftRelease_SecondReview(t *testing.T) {
	t.Parallel()

	webChart := models.TemplateChartSnapshotData{
		ID: "tc-web", ChartName: "web", RepoURL: "oci://charts/web", DefaultValues: "replicas: 1", LockedValues: "tls: true", SortOrder: 1,
	}

	tests := []struct {
		name string
		run  func(t *testing.T, e *draftReleaseEnv)
	}{
		{
			name: "new snapshot sorts after a latest snapshot with a future timestamp",
			run: func(t *testing.T, e *draftReleaseEnv) {
				future := time.Now().UTC().Add(time.Hour)
				old := webChart
				old.DefaultValues = "replicas: 9"
				seedSchemaSnapshot(t, e, "v-future", "0.9.0", 1, old, future)
				w, resp := e.publish(t, `{"version":"1.0.0"}`)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.True(t, resp.SnapshotCreated)
				latest, err := e.versions.GetLatestByTemplate(t.Context(), "t1")
				require.NoError(t, err)
				assert.Equal(t, resp.PublishedVersionID, latest.ID)
				assert.True(t, latest.CreatedAt.After(future))
			},
		},
		{
			name: "legacy latest snapshot counts as changed and its version may be reused",
			run: func(t *testing.T, e *draftReleaseEnv) {
				seedSchemaSnapshot(t, e, "v-legacy", "1.0.0", 0, webChart, time.Now().Add(-time.Minute))
				tmpl, err := e.templates.FindByID("t1")
				require.NoError(t, err)
				tmpl.IsPublished = true
				require.NoError(t, e.templates.Update(tmpl))
				assert.True(t, e.getTemplate(t).HasUnpublishedChanges)

				w, resp := e.publish(t, "")
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				assert.True(t, resp.SnapshotCreated)
				assert.Equal(t, "1.0.0", resp.PublishedVersion)
				assert.Equal(t, 2, e.versionCount(t))
				assert.False(t, e.getTemplate(t).HasUnpublishedChanges)

				// Now the latest is full format: the same version with changes is 409.
				w, resp = e.publish(t, "")
				require.Equal(t, http.StatusOK, w.Code)
				assert.False(t, resp.SnapshotCreated)
				e.editChart(t, "replicas: 2", "1.0.0")
				w, _ = e.publish(t, "")
				assert.Equal(t, http.StatusConflict, w.Code)
			},
		},
		{
			name: "legacy latest does not allow reuse of an older version string",
			run: func(t *testing.T, e *draftReleaseEnv) {
				seedSchemaSnapshot(t, e, "v-old", "0.9.0", 1, webChart, time.Now().Add(-2*time.Minute))
				seedSchemaSnapshot(t, e, "v-legacy", "1.0.0", 0, webChart, time.Now().Add(-time.Minute))
				w, _ := e.publish(t, `{"version":"0.9.0"}`)
				assert.Equal(t, http.StatusConflict, w.Code)
			},
		},
		{
			name: "upgrade diff: empty target chart_version and chart_path are unchanged",
			run: func(t *testing.T, e *draftReleaseEnv) {
				require.NoError(t, e.defs.Create(&models.StackDefinition{
					ID: "d1", Name: "d1", OwnerID: "uid-dev", SourceTemplateID: "t1", SourceTemplateVersion: "1.0.0",
				}))
				require.NoError(t, e.defCharts.Create(&models.ChartConfig{
					ID: "cc1", StackDefinitionID: "d1", ChartName: "web", RepositoryURL: "oci://charts/web",
					DefaultValues: "replicas: 1", ChartVersion: "3.1.0", ChartPath: "charts/web", DeployOrder: 1,
				}))
				target := webChart // schema 1, chart_version and chart_path empty
				seedSchemaSnapshot(t, e, "v-target", "1.1.0", 1, target, time.Now())
				tmpl, err := e.templates.FindByID("t1")
				require.NoError(t, err)
				tmpl.IsPublished = true
				require.NoError(t, e.templates.Update(tmpl))

				w := serve(e.router, http.MethodGet, "/api/v1/stack-definitions/d1/check-upgrade", "")
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				var check upgradeCheckResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &check))
				require.Len(t, check.ChartDiffs, 1)
				assert.Equal(t, "unchanged", check.ChartDiffs[0].ChangeType)
				assert.Equal(t, "3.1.0", check.ChartDiffs[0].RightChartVersion)

				// ApplyUpgrade keeps the values too.
				w = serve(e.router, http.MethodPost, "/api/v1/stack-definitions/d1/upgrade", "{}")
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				var up DefinitionWithChartsResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &up))
				require.Len(t, up.Charts, 1)
				assert.Equal(t, "3.1.0", up.Charts[0].ChartVersion)
				assert.Equal(t, "charts/web", up.Charts[0].ChartPath)
			},
		},
		{
			name: "non-managers get published charts, not the working copy",
			run: func(t *testing.T, e *draftReleaseEnv) {
				other := e.routerAs("uid-user", "user")
				// Never published: empty charts.
				w := serve(other, http.MethodGet, "/api/v1/templates/t1", "")
				require.Equal(t, http.StatusOK, w.Code)
				var d TemplateDetailResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &d))
				assert.Empty(t, d.Charts)
				assert.NotNil(t, d.Charts)
				assert.Equal(t, "", d.Version, "draft version is hidden")
				assert.False(t, d.HasUnpublishedChanges)

				_, pub := e.publish(t, "")
				e.editChart(t, "secret: draft", "1.0.0")
				e.setWorkingVersion(t, "2.0.0-draft")
				w = serve(other, http.MethodGet, "/api/v1/templates/t1", "")
				require.Equal(t, http.StatusOK, w.Code)
				d = TemplateDetailResponse{}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &d))
				require.Len(t, d.Charts, 1)
				assert.Equal(t, "replicas: 1", d.Charts[0].DefaultValues)
				assert.NotContains(t, w.Body.String(), "secret: draft")
				assert.NotContains(t, w.Body.String(), "2.0.0-draft")
				assert.Equal(t, "1.0.0", d.Version)
				assert.False(t, d.HasUnpublishedChanges)

				// Owner and admin see the working copy.
				for _, r := range []*gin.Engine{e.router, e.routerAs("uid-admin", "admin")} {
					w = serve(r, http.MethodGet, "/api/v1/templates/t1", "")
					assert.Contains(t, w.Body.String(), "secret: draft")
					d = TemplateDetailResponse{}
					require.NoError(t, json.Unmarshal(w.Body.Bytes(), &d))
					assert.Equal(t, "2.0.0-draft", d.Version)
					assert.True(t, d.HasUnpublishedChanges)
				}

				// The working copy diff is for managers only.
				w = serve(other, http.MethodGet, "/api/v1/templates/t1/versions/diff?left="+pub.PublishedVersionID+"&right=working", "")
				assert.Equal(t, http.StatusForbidden, w.Code)
				w = serve(e.routerAs("uid-admin", "admin"), http.MethodGet, "/api/v1/templates/t1/versions/diff?left="+pub.PublishedVersionID+"&right=working", "")
				assert.Equal(t, http.StatusOK, w.Code)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newDraftReleaseEnv(t)
			e.seedDraft(t)
			tt.run(t, e)
		})
	}
}

func TestListTemplates_NameFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		role      string
		query     string
		wantIDs   []string
		wantTotal int
	}{
		{"exact name for devops", "devops", "?name=Web%20stack", []string{"t1"}, 1},
		{"exact name, other match", "devops", "?name=api", []string{"t3"}, 1},
		{"no partial match", "devops", "?name=Web", nil, 0},
		{"users see only published matches", "user", "?name=Web%20stack", nil, 0},
		{"users see published match", "user", "?name=api", []string{"t3"}, 1},
		{"without name all templates", "devops", "", []string{"t1", "t2", "t3"}, 3},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newDraftReleaseEnv(t)
			e.seedDraft(t) // t1 "Web stack", unpublished
			require.NoError(t, e.templates.Create(&models.StackTemplate{ID: "t2", Name: "Web stack 2", OwnerID: "uid-dev"}))
			require.NoError(t, e.templates.Create(&models.StackTemplate{ID: "t3", Name: "api", OwnerID: "uid-dev", IsPublished: true}))
			r := e.routerAs("uid-x", tt.role)
			r.GET("/api/v1/templates", e.th.ListTemplates)

			w := serve(r, http.MethodGet, "/api/v1/templates"+tt.query, "")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var resp struct {
				Data  []TemplateListItem `json:"data"`
				Total int                `json:"total"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantTotal, resp.Total)
			ids := make([]string, 0, len(resp.Data))
			for _, item := range resp.Data {
				ids = append(ids, item.ID)
			}
			assert.ElementsMatch(t, tt.wantIDs, ids)
		})
	}
}
