package handlers

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Tests for issue 451: cluster shared values are merged into every values
// generation path. Merge order: shared (by priority) <- chart defaults <-
// instance overrides <- locked template values.

// failingSharedValuesRepo returns an error from ListByCluster.
type failingSharedValuesRepo struct {
	*mockSharedValuesRepo
}

func (f *failingSharedValuesRepo) ListByCluster(string) ([]models.SharedValues, error) {
	return nil, errors.New("connection refused")
}

// recordingHelmExecutor records the values file content passed to Install,
// keyed by release name.
type recordingHelmExecutor struct {
	noopHelmExecutor
	mu     sync.Mutex
	values map[string]string
}

func newRecordingHelmExecutor() *recordingHelmExecutor {
	return &recordingHelmExecutor{values: make(map[string]string)}
}

func (r *recordingHelmExecutor) Install(_ context.Context, req deployer.InstallRequest) (string, error) {
	data, _ := os.ReadFile(req.ValuesFile)
	r.mu.Lock()
	r.values[req.ReleaseName] = string(data)
	r.mu.Unlock()
	return "ok", nil
}

func (r *recordingHelmExecutor) get(release string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.values[release]
	return v, ok
}

// svFixture holds the repositories of a shared values pipeline test.
type svFixture struct {
	instRepo    *MockStackInstanceRepository
	defRepo     *MockStackDefinitionRepository
	ccRepo      *MockChartConfigRepository
	ovRepo      *MockValueOverrideRepository
	boRepo      *MockChartBranchOverrideRepository
	tcRepo      *MockTemplateChartConfigRepository
	svRepo      *mockSharedValuesRepo
	clusterRepo *MockClusterRepository
}

// newSVFixture seeds instance inst-1 (cluster clusterID, may be empty) of
// definition d1 with charts app (c1) and worker (c2).
func newSVFixture(t *testing.T, clusterID string) *svFixture {
	t.Helper()
	f := &svFixture{
		instRepo:    NewMockStackInstanceRepository(),
		defRepo:     NewMockStackDefinitionRepository(),
		ccRepo:      NewMockChartConfigRepository(),
		ovRepo:      NewMockValueOverrideRepository(),
		boRepo:      NewMockChartBranchOverrideRepository(),
		tcRepo:      NewMockTemplateChartConfigRepository(),
		svRepo:      newMockSharedValuesRepo(),
		clusterRepo: NewMockClusterRepository(),
	}
	inst := seedInstance(t, f.instRepo, "inst-1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
	inst.ClusterID = clusterID
	require.NoError(t, f.instRepo.Update(inst))
	seedDefinition(t, f.defRepo, "d1", "My Def", "uid-1")
	require.NoError(t, f.ccRepo.Create(&models.ChartConfig{
		ID: "c1", StackDefinitionID: "d1", ChartName: "app", DeployOrder: 1,
		RepositoryURL: "oci://example.com/charts/app",
		DefaultValues: "fromDefault: app\ndefaultBeatsShared: default\n",
	}))
	require.NoError(t, f.ccRepo.Create(&models.ChartConfig{
		ID: "c2", StackDefinitionID: "d1", ChartName: "worker", DeployOrder: 2,
		RepositoryURL: "oci://example.com/charts/worker",
	}))
	return f
}

func (f *svFixture) addShared(t *testing.T, id, clusterID string, priority int, values string) {
	t.Helper()
	require.NoError(t, f.svRepo.Create(&models.SharedValues{
		ID: id, ClusterID: clusterID, Name: id, Priority: priority, Values: values,
	}))
}

// lockOnTemplate makes d1 come from template tmpl-1 with locked values for app.
func (f *svFixture) lockOnTemplate(t *testing.T, chartName, locked string) {
	t.Helper()
	def, err := f.defRepo.FindByID("d1")
	require.NoError(t, err)
	def.SourceTemplateID = "tmpl-1"
	require.NoError(t, f.defRepo.Update(def))
	require.NoError(t, f.tcRepo.Create(&models.TemplateChartConfig{
		ID: "tc-1", StackTemplateID: "tmpl-1", ChartName: chartName, LockedValues: locked,
	}))
}

// handler builds an InstanceHandler with the given registry and deployer.
func (f *svFixture) handler(t *testing.T, sv models.SharedValuesRepository, registry *cluster.Registry, mgr *deployer.Manager) *InstanceHandler {
	t.Helper()
	h, err := NewInstanceHandlerWithDeployer(
		f.instRepo, f.ovRepo, f.boRepo, f.defRepo, f.ccRepo,
		NewMockStackTemplateRepository(), f.tcRepo,
		helm.NewValuesGenerator(), NewMockUserRepository(),
		mgr, nil, registry, NewMockDeploymentLogRepository(), f.clusterRepo,
		0,
		&mockHandlerTxRunner{repos: database.TxRepos{StackInstance: f.instRepo, ValueOverride: f.ovRepo, BranchOverride: f.boRepo}},
	)
	require.NoError(t, err)
	return h.WithSharedValues(sv)
}

func svRouter(method, path string, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext("uid-1", "devops"))
	r.Handle(method, path, handler)
	return r
}

func parseValues(t *testing.T, s string) map[string]any {
	t.Helper()
	out := map[string]any{}
	require.NoError(t, yaml.Unmarshal([]byte(s), &out))
	return out
}

// previewValues calls deploy-preview and returns chart name -> parsed values.
func previewValues(t *testing.T, h *InstanceHandler) (int, map[string]map[string]any) {
	t.Helper()
	r := svRouter(http.MethodGet, "/api/v1/stack-instances/:id/deploy-preview", h.DeployPreview)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/stack-instances/inst-1/deploy-preview", nil))
	if w.Code != http.StatusOK {
		return w.Code, nil
	}
	var resp DeployPreviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	out := make(map[string]map[string]any, len(resp.Charts))
	for _, ch := range resp.Charts {
		out[ch.ChartName] = parseValues(t, ch.PendingValues)
	}
	return w.Code, out
}

func TestDeployPreview_SharedValuesMergeOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		clusterID  string
		setup      func(t *testing.T, f *svFixture)
		registry   func(f *svFixture) *cluster.Registry
		failRepo   bool
		wantStatus int
		want       map[string]map[string]any // chart -> key -> value
		absent     map[string][]string       // chart -> keys that must be missing
	}{
		{
			name:      "shared value applies to every chart",
			clusterID: "cl-1",
			setup: func(t *testing.T, f *svFixture) {
				f.addShared(t, "sv-1", "cl-1", 0, "sharedValuesOnly: shared\n")
			},
			wantStatus: http.StatusOK,
			want: map[string]map[string]any{
				"app":    {"sharedValuesOnly": "shared", "fromDefault": "app"},
				"worker": {"sharedValuesOnly": "shared"},
			},
		},
		{
			name:      "instance override wins over shared value",
			clusterID: "cl-1",
			setup: func(t *testing.T, f *svFixture) {
				f.addShared(t, "sv-1", "cl-1", 0, "sharedValuesTest: from-shared\n")
				seedValueOverride(t, f.ovRepo, "ov-1", "inst-1", "c1", "sharedValuesTest: from-instance\n")
			},
			wantStatus: http.StatusOK,
			want: map[string]map[string]any{
				"app":    {"sharedValuesTest": "from-instance"},
				"worker": {"sharedValuesTest": "from-shared"},
			},
		},
		{
			name:      "chart default wins over shared value",
			clusterID: "cl-1",
			setup: func(t *testing.T, f *svFixture) {
				f.addShared(t, "sv-1", "cl-1", 0, "defaultBeatsShared: shared\n")
			},
			wantStatus: http.StatusOK,
			want: map[string]map[string]any{
				"app":    {"defaultBeatsShared": "default"},
				"worker": {"defaultBeatsShared": "shared"},
			},
		},
		{
			name:      "locked value wins over shared value and override",
			clusterID: "cl-1",
			setup: func(t *testing.T, f *svFixture) {
				f.addShared(t, "sv-1", "cl-1", 0, "lockedKey: from-shared\n")
				seedValueOverride(t, f.ovRepo, "ov-1", "inst-1", "c1", "lockedKey: from-instance\n")
				f.lockOnTemplate(t, "app", "lockedKey: locked\n")
			},
			wantStatus: http.StatusOK,
			want: map[string]map[string]any{
				"app":    {"lockedKey": "locked"},
				"worker": {"lockedKey": "from-shared"},
			},
		},
		{
			name:      "higher priority shared value wins, keys merge",
			clusterID: "cl-1",
			setup: func(t *testing.T, f *svFixture) {
				// Created high first so the merge order cannot come from insertion order.
				f.addShared(t, "sv-high", "cl-1", 10, "level: high\nonlyHigh: yes-high\n")
				f.addShared(t, "sv-low", "cl-1", 1, "level: low\nonlyLow: yes-low\n")
			},
			wantStatus: http.StatusOK,
			want: map[string]map[string]any{
				"app": {"level": "high", "onlyHigh": "yes-high", "onlyLow": "yes-low"},
			},
		},
		{
			name:      "other cluster shared values do not apply",
			clusterID: "cl-1",
			setup: func(t *testing.T, f *svFixture) {
				f.addShared(t, "sv-other", "cl-2", 0, "otherCluster: x\n")
			},
			wantStatus: http.StatusOK,
			absent:     map[string][]string{"app": {"otherCluster"}, "worker": {"otherCluster"}},
		},
		{
			name:      "empty cluster ID resolves the default cluster",
			clusterID: "",
			setup: func(t *testing.T, f *svFixture) {
				require.NoError(t, f.clusterRepo.Create(&models.Cluster{ID: "cl-default", Name: "default", IsDefault: true}))
				f.addShared(t, "sv-1", "cl-default", 0, "fromDefaultCluster: yes\n")
				f.addShared(t, "sv-2", "cl-2", 0, "otherCluster: x\n")
			},
			registry: func(f *svFixture) *cluster.Registry {
				return cluster.NewRegistry(cluster.RegistryOptions{ClusterRepo: f.clusterRepo})
			},
			wantStatus: http.StatusOK,
			want:       map[string]map[string]any{"app": {"fromDefaultCluster": "yes"}},
			absent:     map[string][]string{"app": {"otherCluster"}},
		},
		{
			name:      "empty cluster ID without registry uses the default cluster from the repository",
			clusterID: "",
			setup: func(t *testing.T, f *svFixture) {
				require.NoError(t, f.clusterRepo.Create(&models.Cluster{ID: "cl-default", Name: "default", IsDefault: true}))
				f.addShared(t, "sv-1", "cl-default", 0, "fromDefaultCluster: yes\n")
			},
			wantStatus: http.StatusOK,
			want:       map[string]map[string]any{"worker": {"fromDefaultCluster": "yes"}},
		},
		{
			name:      "no default cluster means no shared values",
			clusterID: "",
			setup: func(t *testing.T, f *svFixture) {
				f.addShared(t, "sv-1", "cl-1", 0, "sharedValuesOnly: shared\n")
			},
			registry: func(f *svFixture) *cluster.Registry {
				return cluster.NewRegistry(cluster.RegistryOptions{ClusterRepo: f.clusterRepo})
			},
			wantStatus: http.StatusOK,
			absent:     map[string][]string{"app": {"sharedValuesOnly"}},
		},
		{
			name:       "shared values load error fails closed",
			clusterID:  "cl-1",
			setup:      func(t *testing.T, f *svFixture) {},
			failRepo:   true,
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSVFixture(t, tt.clusterID)
			tt.setup(t, f)

			var sv models.SharedValuesRepository = f.svRepo
			if tt.failRepo {
				sv = &failingSharedValuesRepo{f.svRepo}
			}
			var registry *cluster.Registry
			if tt.registry != nil {
				registry = tt.registry(f)
			}

			status, charts := previewValues(t, f.handler(t, sv, registry, nil))
			require.Equal(t, tt.wantStatus, status)
			for chart, kv := range tt.want {
				require.Contains(t, charts, chart)
				for k, v := range kv {
					assert.Equal(t, v, charts[chart][k], "chart %s key %s", chart, k)
				}
			}
			for chart, keys := range tt.absent {
				for _, k := range keys {
					assert.NotContains(t, charts[chart], k, "chart %s key %s", chart, k)
				}
			}
		})
	}
}

// TestExportAndCompare_IncludeSharedValues checks the export endpoints and
// compare render the same shared values as deploy preview.
func TestExportAndCompare_IncludeSharedValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		route string
		url   string
		get   func(h *InstanceHandler) gin.HandlerFunc
		check func(t *testing.T, w *httptest.ResponseRecorder)
	}{
		{
			name:  "export chart values",
			route: "/api/v1/stack-instances/:id/values/:chartId",
			url:   "/api/v1/stack-instances/inst-1/values/c1",
			get:   func(h *InstanceHandler) gin.HandlerFunc { return h.ExportChartValues },
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				assert.Equal(t, `attachment; filename="stack-a-app-values.yaml"; filename*=UTF-8''stack-a-app-values.yaml`, w.Header().Get("Content-Disposition"))
				vals := parseValues(t, w.Body.String())
				assert.Equal(t, "shared", vals["sharedValuesOnly"])
				assert.Equal(t, "from-instance", vals["sharedValuesTest"])
			},
		},
		{
			name:  "export all values",
			route: "/api/v1/stack-instances/:id/values",
			url:   "/api/v1/stack-instances/inst-1/values",
			get:   func(h *InstanceHandler) gin.HandlerFunc { return h.ExportAllValues },
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				assert.Equal(t, "application/zip", w.Header().Get("Content-Type"))
				assert.Equal(t, `attachment; filename="stack-a-values.zip"; filename*=UTF-8''stack-a-values.zip`, w.Header().Get("Content-Disposition"))
				zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
				require.NoError(t, err)
				files := map[string]map[string]any{}
				for _, zf := range zr.File {
					rc, err := zf.Open()
					require.NoError(t, err)
					data, err := io.ReadAll(rc)
					require.NoError(t, err)
					require.NoError(t, rc.Close())
					files[zf.Name] = parseValues(t, string(data))
				}
				assert.Equal(t, "from-instance", files["app/values.yaml"]["sharedValuesTest"])
				assert.Equal(t, "from-shared", files["worker/values.yaml"]["sharedValuesTest"])
				assert.Equal(t, "shared", files["worker/values.yaml"]["sharedValuesOnly"])
			},
		},
		{
			name:  "export chart values of another definition is 404",
			route: "/api/v1/stack-instances/:id/values/:chartId",
			url:   "/api/v1/stack-instances/inst-1/values/c-foreign",
			get:   func(h *InstanceHandler) gin.HandlerFunc { return h.ExportChartValues },
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				assert.Equal(t, http.StatusNotFound, w.Code)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSVFixture(t, "cl-1")
			f.addShared(t, "sv-1", "cl-1", 0, "sharedValuesOnly: shared\nsharedValuesTest: from-shared\n")
			seedValueOverride(t, f.ovRepo, "ov-1", "inst-1", "c1", "sharedValuesTest: from-instance\n")
			require.NoError(t, f.ccRepo.Create(&models.ChartConfig{ID: "c-foreign", StackDefinitionID: "d-other", ChartName: "foreign"}))
			h := f.handler(t, f.svRepo, nil, nil)

			r := svRouter(http.MethodGet, tt.route, tt.get(h))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.url, nil))
			if tt.name != "export chart values of another definition is 404" {
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			}
			tt.check(t, w)
		})
	}
}

// TestCompareInstances_ValueOverrideOnOneSide reproduces issue 445 item 3:
// two instances of the same definition, only one has a value override.
// Compare must report a difference for that chart and none for the other.
// It also checks that compare includes shared values of each instance's cluster.
func TestCompareInstances_ValueOverrideOnOneSide(t *testing.T) {
	t.Parallel()

	f := newSVFixture(t, "cl-1")
	right := seedInstance(t, f.instRepo, "inst-2", "stack-a", "d1", "uid-1", models.StackStatusDraft)
	// Same namespace and name vars on both sides so only the override differs.
	left, err := f.instRepo.FindByID("inst-1")
	require.NoError(t, err)
	right.ClusterID = "cl-1"
	right.Namespace = left.Namespace
	require.NoError(t, f.instRepo.Update(right))
	f.addShared(t, "sv-1", "cl-1", 0, "sharedValuesOnly: shared\n")
	seedValueOverride(t, f.ovRepo, "ov-1", "inst-1", "c1", "replicaCount: 3\n")

	h := f.handler(t, f.svRepo, nil, nil)
	r := svRouter(http.MethodGet, "/api/v1/stack-instances/compare", h.CompareInstances)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/stack-instances/compare?left=inst-1&right=inst-2", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp CompareInstancesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	byName := map[string]CompareChartDiff{}
	for _, ch := range resp.Charts {
		byName[ch.ChartName] = ch
	}
	require.Contains(t, byName, "app")
	require.Contains(t, byName, "worker")
	assert.True(t, byName["app"].HasDifferences, "override on one side must differ")
	assert.False(t, byName["worker"].HasDifferences)
	require.NotNil(t, byName["app"].LeftValues)
	assert.Equal(t, 3, parseValues(t, *byName["app"].LeftValues)["replicaCount"])
	assert.Equal(t, "shared", parseValues(t, *byName["app"].RightValues)["sharedValuesOnly"])
}

// TestDeployPaths_PassSharedValuesToDeployer checks that single deploy, bulk
// deploy and quick deploy hand the shared values to Helm.
func TestDeployPaths_PassSharedValuesToDeployer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		body any
		call func(h *InstanceHandler) gin.HandlerFunc
		want int
	}{
		{name: "single deploy", path: "/api/v1/stack-instances/:id/deploy", call: func(h *InstanceHandler) gin.HandlerFunc { return h.DeployInstance }, want: http.StatusAccepted},
		{name: "bulk deploy", path: "/api/v1/stack-instances/bulk/deploy", body: BulkOperationRequest{InstanceIDs: []string{"inst-1"}}, call: func(h *InstanceHandler) gin.HandlerFunc { return h.BulkDeploy }, want: http.StatusOK},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSVFixture(t, "test-cluster")
			f.addShared(t, "sv-1", "test-cluster", 0, "sharedValuesOnly: shared\nsharedValuesTest: from-shared\n")
			seedValueOverride(t, f.ovRepo, "ov-1", "inst-1", "c1", "sharedValuesTest: from-instance\n")

			rec := newRecordingHelmExecutor()
			mgr := deployer.NewManager(deployer.ManagerConfig{
				Registry:      cluster.NewRegistryForTest("test-cluster", nil, rec),
				InstanceRepo:  f.instRepo,
				DeployLogRepo: NewMockDeploymentLogRepository(),
				Hub:           &MockBroadcastSender{},
				MaxConcurrent: 2,
			})
			h := f.handler(t, f.svRepo, nil, mgr)

			r := svRouter(http.MethodPost, tt.path, tt.call(h))
			var body []byte
			if tt.body != nil {
				body, _ = json.Marshal(tt.body)
			}
			url := "/api/v1/stack-instances/inst-1/deploy"
			if tt.body != nil {
				url = "/api/v1/stack-instances/bulk/deploy"
			}
			req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, tt.want, w.Code, w.Body.String())

			require.Eventually(t, func() bool {
				_, okApp := rec.get("app")
				_, okWorker := rec.get("worker")
				return okApp && okWorker
			}, 3*time.Second, 20*time.Millisecond)

			app, _ := rec.get("app")
			worker, _ := rec.get("worker")
			assert.Equal(t, "shared", parseValues(t, app)["sharedValuesOnly"])
			assert.Equal(t, "from-instance", parseValues(t, app)["sharedValuesTest"])
			assert.Equal(t, "from-shared", parseValues(t, worker)["sharedValuesTest"])
		})
	}
}

// TestDeployInstance_SharedValuesLoadErrorFailsClosed checks that deploy does
// not start when shared values cannot be loaded.
func TestDeployInstance_SharedValuesLoadErrorFailsClosed(t *testing.T) {
	t.Parallel()
	f := newSVFixture(t, "test-cluster")
	rec := newRecordingHelmExecutor()
	mgr := deployer.NewManager(deployer.ManagerConfig{
		Registry:      cluster.NewRegistryForTest("test-cluster", nil, rec),
		InstanceRepo:  f.instRepo,
		DeployLogRepo: NewMockDeploymentLogRepository(),
		Hub:           &MockBroadcastSender{},
		MaxConcurrent: 2,
	})
	h := f.handler(t, &failingSharedValuesRepo{f.svRepo}, nil, mgr)

	r := svRouter(http.MethodPost, "/api/v1/stack-instances/:id/deploy", h.DeployInstance)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/stack-instances/inst-1/deploy", nil))
	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), "connection refused", "internal error must not leak")

	inst, err := f.instRepo.FindByID("inst-1")
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusDraft, inst.Status, "deploy must not start")
}

// failingListValueOverrideRepo fails ListByInstance only.
type failingListValueOverrideRepo struct {
	*MockValueOverrideRepository
}

func (f *failingListValueOverrideRepo) ListByInstance(string) ([]models.ValueOverride, error) {
	return nil, errors.New("connection refused")
}

// TestQuickDeploy_UsesValuesPipeline checks that quick deploy renders values
// with the shared pipeline (shared values of the resolved default cluster),
// records LastDeployedValues, and fails (instance status error, no Helm call)
// when a lookup of the pipeline fails.
func TestQuickDeploy_UsesValuesPipeline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		failOverride bool
		failBranch   bool
		failShared   bool
		wantOK       bool
	}{
		{name: "shared values reach Helm", wantOK: true},
		{name: "value override list error fails the deploy", failOverride: true},
		{name: "branch override list error fails the deploy", failBranch: true},
		{name: "shared values load error fails the deploy", failShared: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmplRepo := NewMockStackTemplateRepository()
			tmplChartRepo := NewMockTemplateChartConfigRepository()
			defRepo := NewMockStackDefinitionRepository()
			ccRepo := NewMockChartConfigRepository()
			instRepo := NewMockStackInstanceRepository()
			boRepo := NewMockChartBranchOverrideRepository()
			ovMock := NewMockValueOverrideRepository()
			svMock := newMockSharedValuesRepo()

			require.NoError(t, tmplRepo.Create(&models.StackTemplate{
				ID: "t1", Name: "Template", IsPublished: true, DefaultBranch: "main", OwnerID: "owner-1",
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}))
			require.NoError(t, tmplChartRepo.Create(&models.TemplateChartConfig{
				ID: "tc1", StackTemplateID: "t1", ChartName: "svc",
				RepositoryURL: "oci://example.com/charts/svc", DeployOrder: 1,
				DefaultValues: "fromDefault: svc\nlockedKey: default\n",
				LockedValues:  "lockedKey: locked\n",
			}))
			require.NoError(t, svMock.Create(&models.SharedValues{ID: "sv-1", ClusterID: "test-cluster", Values: "sharedValuesOnly: shared\nlockedKey: shared\n"}))

			var ovRepo models.ValueOverrideRepository = ovMock
			if tt.failOverride {
				ovRepo = &failingListValueOverrideRepo{ovMock}
			}
			if tt.failBranch {
				boRepo.SetError(errors.New("connection refused"))
			}
			var svRepo models.SharedValuesRepository = svMock
			if tt.failShared {
				svRepo = &failingSharedValuesRepo{svMock}
			}

			rec := newRecordingHelmExecutor()
			registry := cluster.NewRegistryForTest("test-cluster", nil, rec)
			mgr := deployer.NewManager(deployer.ManagerConfig{
				Registry:      registry,
				InstanceRepo:  instRepo,
				DeployLogRepo: NewMockDeploymentLogRepository(),
				Hub:           &MockBroadcastSender{},
				MaxConcurrent: 2,
			})

			h, err := NewQuickDeployHandler(
				tmplRepo, tmplChartRepo, defRepo, ccRepo,
				instRepo, boRepo, ovRepo, helm.NewValuesGenerator(),
				mgr, NewMockUserRepository(), nil, NewMockAuditLogRepository(),
				&MockBroadcastSender{}, registry, nil,
				0,
				&mockHandlerTxRunner{repos: database.TxRepos{
					StackDefinition: defRepo, ChartConfig: ccRepo, StackInstance: instRepo,
					ValueOverride: ovMock, BranchOverride: boRepo,
				}},
			)
			require.NoError(t, err)
			h.WithSharedValues(svRepo).WithTemplateVersions(newWorkingCopyReleaseRepo(tmplRepo, tmplChartRepo))

			r := svRouter(http.MethodPost, "/api/v1/templates/:id/quick-deploy", h.QuickDeploy)
			body, _ := json.Marshal(quickDeployRequest{InstanceName: "shared-values-test"})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/templates/t1/quick-deploy", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

			var resp quickDeployResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.NotNil(t, resp.Instance)

			if !tt.wantOK {
				assert.Empty(t, resp.LogID)
				inst, err := instRepo.FindByID(resp.Instance.ID)
				require.NoError(t, err)
				assert.Equal(t, models.StackStatusError, inst.Status)
				assert.Equal(t, "Deployment failed", inst.ErrorMessage)
				time.Sleep(50 * time.Millisecond)
				_, installed := rec.get("svc")
				assert.False(t, installed, "no chart may be installed")
				return
			}

			require.Eventually(t, func() bool {
				_, ok := rec.get("svc")
				return ok
			}, 3*time.Second, 20*time.Millisecond)
			svc, _ := rec.get("svc")
			vals := parseValues(t, svc)
			assert.Equal(t, "shared", vals["sharedValuesOnly"])
			assert.Equal(t, "svc", vals["fromDefault"])
			assert.Equal(t, "locked", vals["lockedKey"])

			// LastDeployedValues is recorded once the deploy finishes.
			require.Eventually(t, func() bool {
				inst, err := instRepo.FindByID(resp.Instance.ID)
				return err == nil && inst.LastDeployedValues != ""
			}, 3*time.Second, 20*time.Millisecond)
			inst, err := instRepo.FindByID(resp.Instance.ID)
			require.NoError(t, err)
			var last map[string]string
			require.NoError(t, json.Unmarshal([]byte(inst.LastDeployedValues), &last))
			assert.Equal(t, svc, last["svc"])
		})
	}
}
