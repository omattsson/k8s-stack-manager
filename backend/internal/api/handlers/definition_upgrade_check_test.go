package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// upgradeBaseChart is the definition chart and the chart of its current
// source version (1.0) in the upgrade check tests.
func upgradeBaseChart() models.TemplateChartSnapshotData {
	return models.TemplateChartSnapshotData{
		ChartName:       "api",
		RepoURL:         "oci://registry.example.com/charts",
		DefaultValues:   "replicas: 1\n",
		LockedValues:    "tls: true\n",
		IsRequired:      true,
		SortOrder:       1,
		ChartPath:       "charts/api",
		ChartVersion:    "1.0.0",
		SourceRepoURL:   "https://github.com/example-org/api",
		BuildPipelineID: "pipeline-1",
	}
}

// schemaV1SnapshotJSON returns a schema version 1 snapshot, which stores the
// chart source fields.
func schemaV1SnapshotJSON(t *testing.T, version string, charts ...models.TemplateChartSnapshotData) string {
	t.Helper()
	b, err := json.Marshal(models.TemplateSnapshot{
		SchemaVersion: 1,
		Template:      models.TemplateSnapshotData{Name: "T", Version: version},
		Charts:        charts,
	})
	require.NoError(t, err)
	return string(b)
}

type upgradeCheckEnv struct {
	router http.Handler
	charts *MockChartConfigRepository
}

// newUpgradeCheckEnv seeds definition d1 (source template t1, version 1.0)
// with the base chart, the source version 1.0 and the latest version 2.0
// with the given chart.
func newUpgradeCheckEnv(t *testing.T, latest models.TemplateChartSnapshotData) upgradeCheckEnv {
	t.Helper()
	defRepo := NewMockStackDefinitionRepository()
	require.NoError(t, defRepo.Create(context.Background(), &models.StackDefinition{
		ID: "d1", Name: "My Def", OwnerID: "uid-1",
		SourceTemplateID: "t1", SourceTemplateVersion: "1.0",
	}))
	base := upgradeBaseChart()
	ccRepo := NewMockChartConfigRepository()
	require.NoError(t, ccRepo.Create(&models.ChartConfig{
		ID: "cc1", StackDefinitionID: "d1", ChartName: base.ChartName,
		RepositoryURL: base.RepoURL, DefaultValues: base.DefaultValues, DeployOrder: base.SortOrder,
		ChartPath: base.ChartPath, ChartVersion: base.ChartVersion,
		SourceRepoURL: base.SourceRepoURL, BuildPipelineID: base.BuildPipelineID,
	}))
	verRepo := NewMockTemplateVersionRepository()
	seedVersion(t, verRepo, "v1", "t1", "1.0", schemaV1SnapshotJSON(t, "1.0", base), time.Now().Add(-time.Hour))
	seedVersion(t, verRepo, "v2", "t1", "2.0", schemaV1SnapshotJSON(t, "2.0", latest), time.Now())

	tmplRepo := NewMockStackTemplateRepository()
	seedTemplate(t, tmplRepo, "t1", "T", "owner-1", true)
	router := setupDefinitionUpgradeRouter(t, defRepo, ccRepo, NewMockStackInstanceRepository(), tmplRepo,
		NewMockTemplateChartConfigRepository(), verRepo, "uid-1", "devops")
	return upgradeCheckEnv{router: router, charts: ccRepo}
}

func (e upgradeCheckEnv) check(t *testing.T) upgradeCheckResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-definitions/d1/check-upgrade", nil)
	e.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp upgradeCheckResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

// assertChangesAgreeWithDiffs checks that changes lists each chart of
// chart_diffs under its change_type, and no other chart.
func assertChangesAgreeWithDiffs(t *testing.T, resp upgradeCheckResponse) {
	t.Helper()
	require.NotNil(t, resp.Changes)
	byType := map[string][]string{}
	for _, d := range resp.ChartDiffs {
		byType[d.ChangeType] = append(byType[d.ChangeType], d.ChartName)
		assert.Equal(t, d.ChangeType != "unchanged", d.HasDifferences, "has_differences of %s", d.ChartName)
	}
	sorted := func(s []string) []string {
		out := append([]string{}, s...)
		sort.Strings(out)
		return out
	}
	assert.Equal(t, sorted(byType["added"]), sorted(resp.Changes.ChartsAdded), "charts_added")
	assert.Equal(t, sorted(byType["removed"]), sorted(resp.Changes.ChartsRemoved), "charts_removed")
	assert.Equal(t, sorted(byType["modified"]), sorted(resp.Changes.ChartsModified), "charts_modified")
	assert.Equal(t, sorted(byType["unchanged"]), sorted(resp.Changes.ChartsUnchanged), "charts_unchanged")
}

// TestCheckUpgrade_EachUpgradeFieldMarksTheChartModified covers each field
// that the upgrade writes (and the template rules of the chart): a change
// puts the chart in charts_modified, and changes agrees with chart_diffs.
func TestCheckUpgrade_EachUpgradeFieldMarksTheChartModified(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*models.TemplateChartSnapshotData)
		want   string
	}{
		{name: "nothing changed", mutate: func(*models.TemplateChartSnapshotData) {}, want: "unchanged"},
		{name: "default values", mutate: func(c *models.TemplateChartSnapshotData) { c.DefaultValues = "replicas: 3\n" }, want: "modified"},
		{name: "repository URL", mutate: func(c *models.TemplateChartSnapshotData) { c.RepoURL = "oci://registry.example.com/other" }, want: "modified"},
		{name: "chart version only (#618)", mutate: func(c *models.TemplateChartSnapshotData) { c.ChartVersion = "1.1.0" }, want: "modified"},
		{name: "chart path", mutate: func(c *models.TemplateChartSnapshotData) { c.ChartPath = "charts/api-v2" }, want: "modified"},
		{name: "source repository", mutate: func(c *models.TemplateChartSnapshotData) { c.SourceRepoURL = "https://github.com/example-org/api2" }, want: "modified"},
		{name: "build pipeline", mutate: func(c *models.TemplateChartSnapshotData) { c.BuildPipelineID = "pipeline-2" }, want: "modified"},
		{name: "deploy order", mutate: func(c *models.TemplateChartSnapshotData) { c.SortOrder = 2 }, want: "modified"},
		{name: "locked values (template rule)", mutate: func(c *models.TemplateChartSnapshotData) { c.LockedValues = "tls: false\n" }, want: "modified"},
		{name: "required flag (template rule)", mutate: func(c *models.TemplateChartSnapshotData) { c.IsRequired = false }, want: "modified"},
		{name: "empty chart version keeps the definition's", mutate: func(c *models.TemplateChartSnapshotData) { c.ChartVersion = "" }, want: "unchanged"},
		{name: "empty chart path keeps the definition's", mutate: func(c *models.TemplateChartSnapshotData) { c.ChartPath = "" }, want: "unchanged"},
		{name: "empty source repository keeps the definition's", mutate: func(c *models.TemplateChartSnapshotData) { c.SourceRepoURL = "" }, want: "unchanged"},
		{name: "empty build pipeline keeps the definition's", mutate: func(c *models.TemplateChartSnapshotData) { c.BuildPipelineID = "" }, want: "unchanged"},
		{name: "default values that differ only in trailing whitespace", mutate: func(c *models.TemplateChartSnapshotData) { c.DefaultValues = "replicas: 1  \n\n" }, want: "unchanged"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			latest := upgradeBaseChart()
			tt.mutate(&latest)
			resp := newUpgradeCheckEnv(t, latest).check(t)

			require.True(t, resp.UpgradeAvailable)
			assert.Empty(t, resp.Reason)
			require.Len(t, resp.ChartDiffs, 1)
			assert.Equal(t, tt.want, resp.ChartDiffs[0].ChangeType)
			assertChangesAgreeWithDiffs(t, resp)
			if tt.want == "modified" {
				assert.Equal(t, []string{"api"}, resp.Changes.ChartsModified)
			} else {
				assert.Equal(t, []string{"api"}, resp.Changes.ChartsUnchanged)
			}
		})
	}
}

// TestCheckUpgrade_ChartVersionOnlyThenUpgrade follows the example of #618:
// only the chart version changes in the template. The check reports the
// chart as modified with both versions, the upgrade writes the version, and
// the next check reports up_to_date.
func TestCheckUpgrade_ChartVersionOnlyThenUpgrade(t *testing.T) {
	t.Parallel()

	latest := upgradeBaseChart()
	latest.ChartVersion = "1.1.0"
	env := newUpgradeCheckEnv(t, latest)

	resp := env.check(t)
	assert.Equal(t, []string{"api"}, resp.Changes.ChartsModified)
	require.Len(t, resp.ChartDiffs, 1)
	assert.Equal(t, "1.0.0", resp.ChartDiffs[0].LeftChartVersion)
	assert.Equal(t, "1.1.0", resp.ChartDiffs[0].RightChartVersion)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-definitions/d1/upgrade", bytes.NewBufferString("{}"))
	req.Header.Set("Content-Type", "application/json")
	env.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	charts, err := env.charts.ListByDefinition("d1")
	require.NoError(t, err)
	require.Len(t, charts, 1)
	assert.Equal(t, "1.1.0", charts[0].ChartVersion)

	after := env.check(t)
	assert.False(t, after.UpgradeAvailable)
	assert.Equal(t, upgradeReasonUpToDate, after.Reason)
	assert.Equal(t, "2.0", after.CurrentVersion)
	assert.Equal(t, "2.0", after.LatestVersion)
}

// TestCheckUpgrade_AddedAndRemovedCharts checks the lists for charts that
// are only in the template or only in the definition.
func TestCheckUpgrade_AddedAndRemovedCharts(t *testing.T) {
	t.Parallel()

	newChart := upgradeBaseChart()
	newChart.ChartName = "worker"
	newChart.ChartVersion = "2.0.0"

	defRepo := NewMockStackDefinitionRepository()
	require.NoError(t, defRepo.Create(context.Background(), &models.StackDefinition{
		ID: "d1", Name: "My Def", OwnerID: "uid-1", SourceTemplateID: "t1", SourceTemplateVersion: "1.0",
	}))
	ccRepo := NewMockChartConfigRepository()
	require.NoError(t, ccRepo.Create(&models.ChartConfig{ID: "cc1", StackDefinitionID: "d1", ChartName: "legacy"}))
	verRepo := NewMockTemplateVersionRepository()
	seedVersion(t, verRepo, "v2", "t1", "2.0", schemaV1SnapshotJSON(t, "2.0", newChart), time.Now())
	tmplRepo := NewMockStackTemplateRepository()
	seedTemplate(t, tmplRepo, "t1", "T", "owner-1", true)
	router := setupDefinitionUpgradeRouter(t, defRepo, ccRepo, NewMockStackInstanceRepository(), tmplRepo,
		NewMockTemplateChartConfigRepository(), verRepo, "uid-1", "devops")
	resp := upgradeCheckEnv{router: router, charts: ccRepo}.check(t)

	assert.Equal(t, []string{"worker"}, resp.Changes.ChartsAdded)
	assert.Equal(t, []string{"legacy"}, resp.Changes.ChartsRemoved)
	assertChangesAgreeWithDiffs(t, resp)
	for _, d := range resp.ChartDiffs {
		if d.ChartName == "worker" {
			assert.Equal(t, "2.0.0", d.RightChartVersion, "an added chart shows its chart source fields")
			assert.Equal(t, "pipeline-1", d.RightBuildPipelineID)
		}
	}
}

func TestCheckUpgrade_Reason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		def         models.StackDefinition
		published   bool
		withVersion bool
		wantReason  string
		wantCurrent string
		wantLatest  string
	}{
		{
			name:       "no source template",
			def:        models.StackDefinition{ID: "d1", Name: "D", OwnerID: "uid-1"},
			wantReason: upgradeReasonNoSourceTemplate,
		},
		{
			name:        "template not published",
			def:         models.StackDefinition{ID: "d1", Name: "D", OwnerID: "uid-1", SourceTemplateID: "t1", SourceTemplateVersion: "1.0"},
			published:   false,
			withVersion: true,
			wantReason:  upgradeReasonNoPublishedVersion,
			wantCurrent: "1.0",
		},
		{
			name:        "published template without a snapshot",
			def:         models.StackDefinition{ID: "d1", Name: "D", OwnerID: "uid-1", SourceTemplateID: "t1", SourceTemplateVersion: "1.0"},
			published:   true,
			wantReason:  upgradeReasonNoPublishedVersion,
			wantCurrent: "1.0",
		},
		{
			name:        "at the latest version",
			def:         models.StackDefinition{ID: "d1", Name: "D", OwnerID: "uid-1", SourceTemplateID: "t1", SourceTemplateVersion: "2.0"},
			published:   true,
			withVersion: true,
			wantReason:  upgradeReasonUpToDate,
			wantCurrent: "2.0",
			wantLatest:  "2.0",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defRepo := NewMockStackDefinitionRepository()
			def := tt.def
			require.NoError(t, defRepo.Create(context.Background(), &def))
			verRepo := NewMockTemplateVersionRepository()
			if tt.withVersion {
				seedVersion(t, verRepo, "v2", "t1", "2.0", schemaV1SnapshotJSON(t, "2.0", upgradeBaseChart()), time.Now())
			}
			tmplRepo := NewMockStackTemplateRepository()
			seedTemplate(t, tmplRepo, "t1", "T", "owner-1", tt.published)
			router := setupDefinitionUpgradeRouter(t, defRepo, NewMockChartConfigRepository(), NewMockStackInstanceRepository(),
				tmplRepo, NewMockTemplateChartConfigRepository(), verRepo, "uid-1", "devops")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-definitions/d1/check-upgrade", nil)
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			var resp map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, false, resp["upgrade_available"])
			assert.Equal(t, tt.wantReason, resp["reason"])
			if tt.wantCurrent == "" {
				assert.NotContains(t, resp, "current_version")
			} else {
				assert.Equal(t, tt.wantCurrent, resp["current_version"])
			}
			if tt.wantLatest == "" {
				assert.NotContains(t, resp, "latest_version")
			} else {
				assert.Equal(t, tt.wantLatest, resp["latest_version"])
			}
			assert.NotContains(t, resp, "changes")
			assert.NotContains(t, resp, "chart_diffs")
		})
	}
}

func TestUpgradeTargetChart(t *testing.T) {
	t.Parallel()

	existing := models.ChartConfig{
		ID: "cc1", StackDefinitionID: "d1", ChartName: "api",
		RepositoryURL: "r1", DefaultValues: "a: 1", DeployOrder: 1,
		ChartVersion: "1.0.0", ChartPath: "p1", SourceRepoURL: "s1", BuildPipelineID: "b1",
	}
	full := models.TemplateChartConfig{
		ChartName: "api", RepositoryURL: "r2", DefaultValues: "a: 2", DeployOrder: 2,
		ChartVersion: "2.0.0", ChartPath: "p2", SourceRepoURL: "s2", BuildPipelineID: "b2",
		LockedValues: "x: 1", Required: true,
	}
	empty := models.TemplateChartConfig{ChartName: "api"}

	tests := []struct {
		name     string
		existing models.ChartConfig
		tch      models.TemplateChartConfig
		want     models.ChartConfig
	}{
		{
			name: "all template fields set", existing: existing, tch: full,
			want: models.ChartConfig{
				ID: "cc1", StackDefinitionID: "d1", ChartName: "api",
				RepositoryURL: "r2", DefaultValues: "a: 2", DeployOrder: 2,
				ChartVersion: "2.0.0", ChartPath: "p2", SourceRepoURL: "s2", BuildPipelineID: "b2",
			},
		},
		{
			name:     "empty source fields keep the definition's; values, URL and order always follow",
			existing: existing, tch: empty,
			want: models.ChartConfig{
				ID: "cc1", StackDefinitionID: "d1", ChartName: "api",
				ChartVersion: "1.0.0", ChartPath: "p1", SourceRepoURL: "s1", BuildPipelineID: "b1",
			},
		},
		{
			name: "new chart", existing: models.ChartConfig{}, tch: full,
			want: models.ChartConfig{
				ChartName: "api", RepositoryURL: "r2", DefaultValues: "a: 2", DeployOrder: 2,
				ChartVersion: "2.0.0", ChartPath: "p2", SourceRepoURL: "s2", BuildPipelineID: "b2",
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, upgradeTargetChart(tt.existing, tt.tch))
		})
	}
}
