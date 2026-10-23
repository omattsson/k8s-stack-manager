package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// instanceLimitEnv is a router with create, clone and quick deploy on one
// set of repositories, and the cluster cl-1 with max_instances_per_user 2.
type instanceLimitEnv struct {
	router   *gin.Engine
	clusters *MockClusterRepository
	insts    *MockStackInstanceRepository
}

// newInstanceLimitEnv stores one instance of uid-1 for each entry of
// existing, on that cluster ID ("" means no cluster). cl-1 is the default
// cluster when defaultCluster is set.
func newInstanceLimitEnv(t *testing.T, existing []string, defaultCluster bool) *instanceLimitEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	clusters := NewMockClusterRepository()
	require.NoError(t, clusters.Create(context.Background(), &models.Cluster{
		ID: "cl-1", Name: "one", APIServerURL: "https://one.example.com", KubeconfigPath: "/kc",
		MaxInstancesPerUser: 2, IsDefault: defaultCluster,
	}))
	insts := NewMockStackInstanceRepository()
	defs := NewMockStackDefinitionRepository()
	seedDefinition(t, defs, "d1", "def-1", "uid-1")
	for i, clusterID := range existing {
		require.NoError(t, insts.Create(context.Background(), &models.StackInstance{
			ID: fmt.Sprintf("i%d", i), Name: fmt.Sprintf("existing-%d", i), StackDefinitionID: "d1",
			Namespace: fmt.Sprintf("stack-existing-%d-alice", i), OwnerID: "uid-1", ClusterID: clusterID,
			Branch: "master", Status: models.StackStatusDraft, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}))
	}

	overrides := NewMockValueOverrideRepository()
	branches := NewMockChartBranchOverrideRepository()
	charts := NewMockChartConfigRepository()
	txRunner := &mockHandlerTxRunner{repos: database.TxRepos{
		StackInstance: insts, StackDefinition: defs, ChartConfig: charts, ValueOverride: overrides, BranchOverride: branches,
	}}

	ih := NewInstanceHandler(insts, overrides, branches, defs, charts, NewMockStackTemplateRepository(),
		NewMockTemplateChartConfigRepository(), helm.NewValuesGenerator(), NewMockUserRepository(), 0)
	ih.clusterRepo = clusters
	ih.txRunner = txRunner

	templates := NewMockStackTemplateRepository()
	templateCharts := NewMockTemplateChartConfigRepository()
	seedTemplate(t, templates, "t1", "My Template", "owner-1", true)
	require.NoError(t, templateCharts.Create(context.Background(), &models.TemplateChartConfig{
		ID: "tc1", StackTemplateID: "t1", ChartName: "app", RepositoryURL: "oci://example.com/charts/app", DeployOrder: 1,
	}))
	qh, err := NewQuickDeployHandler(templates, templateCharts, defs, charts, insts, branches, overrides,
		helm.NewValuesGenerator(), nil, NewMockUserRepository(), nil, NewMockAuditLogRepository(),
		&MockBroadcastSender{}, nil, nil, 0, txRunner)
	require.NoError(t, err)
	qh.WithTemplateVersions(newWorkingCopyReleaseRepo(templates, templateCharts)).WithClusterRepo(clusters)

	r := gin.New()
	r.Use(injectAuthContext("uid-1", "user"), func(c *gin.Context) {
		c.Set("username", "alice")
		c.Next()
	})
	r.POST("/api/v1/stack-instances", ih.CreateInstance)
	r.POST("/api/v1/stack-instances/:id/clone", ih.CloneInstance)
	r.POST("/api/v1/templates/:id/quick-deploy", qh.QuickDeploy)
	return &instanceLimitEnv{router: r, clusters: clusters, insts: insts}
}

// count returns the number of instances of uid-1, with or without a cluster.
func (e *instanceLimitEnv) count(t *testing.T) int {
	t.Helper()
	all, err := e.insts.ListByOwner(context.Background(), "uid-1")
	require.NoError(t, err)
	return len(all)
}

func (e *instanceLimitEnv) post(path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	e.router.ServeHTTP(w, req)
	return w
}

func assertLimitConflict(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "Maximum instances per user reached for this cluster (limit: 2)", body["error"])
}

var instanceLimitPaths = []struct {
	name   string
	path   string
	body   string
	wantOK int
}{
	{name: "create", path: "/api/v1/stack-instances",
		body: `{"stack_definition_id":"d1","name":"new-stack","cluster_id":"cl-1"}`, wantOK: http.StatusCreated},
	{name: "clone", path: "/api/v1/stack-instances/i0/clone",
		body: `{"name":"new-clone"}`, wantOK: http.StatusCreated},
	{name: "quick deploy", path: "/api/v1/templates/t1/quick-deploy",
		body: `{"instance_name":"new-quick","cluster_id":"cl-1"}`, wantOK: http.StatusAccepted},
}

// TestInstanceLimit_AllCreatePaths checks that create, clone and quick deploy
// apply the per-user instance limit of the cluster with the same 409, and a
// failed cluster read stops the create (#682).
func TestInstanceLimit_AllCreatePaths(t *testing.T) {
	t.Parallel()

	cases := []struct {
		fetchErr error
		name     string
		existing []string
		wantCode int // 0: the success code of the path
		wantNew  int
	}{
		{name: "below the limit", existing: []string{"cl-1"}, wantNew: 1},
		{name: "at the limit", existing: []string{"cl-1", "cl-1"}, wantCode: http.StatusConflict},
		{name: "failed cluster read", existing: []string{"cl-1"}, fetchErr: errors.New("db down"), wantCode: http.StatusInternalServerError},
	}
	for _, p := range instanceLimitPaths {
		for _, tc := range cases {
			p, tc := p, tc
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				env := newInstanceLimitEnv(t, tc.existing, false)
				if tc.fetchErr != nil {
					env.clusters.SetFetchError(tc.fetchErr)
				}

				w := env.post(p.path, p.body)

				want := tc.wantCode
				if want == 0 {
					want = p.wantOK
				}
				require.Equal(t, want, w.Code, w.Body.String())
				assert.Equal(t, len(tc.existing)+tc.wantNew, env.count(t))
				switch want {
				case http.StatusConflict:
					assertLimitConflict(t, w)
				case http.StatusInternalServerError:
					assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
				}
			})
		}
	}
}

// TestInstanceLimit_InstancesWithoutCluster checks that the instances without
// a cluster count toward the limit of the default cluster only, and that a
// clone of such an instance gets the limit of the default cluster (#682).
func TestInstanceLimit_InstancesWithoutCluster(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		path           string
		body           string
		existing       []string
		defaultCluster bool
		wantCode       int
	}{
		{name: "create on the default cluster counts them", path: instanceLimitPaths[0].path, body: instanceLimitPaths[0].body,
			existing: []string{"cl-1", ""}, defaultCluster: true, wantCode: http.StatusConflict},
		{name: "quick deploy on the default cluster counts them", path: instanceLimitPaths[2].path, body: instanceLimitPaths[2].body,
			existing: []string{"cl-1", ""}, defaultCluster: true, wantCode: http.StatusConflict},
		{name: "clone of an instance without a cluster gets the default limit", path: "/api/v1/stack-instances/i0/clone",
			body: `{"name":"new-clone"}`, existing: []string{"", "cl-1"}, defaultCluster: true, wantCode: http.StatusConflict},
		{name: "clone of an instance without a cluster below the default limit", path: "/api/v1/stack-instances/i0/clone",
			body: `{"name":"new-clone"}`, existing: []string{""}, defaultCluster: true, wantCode: http.StatusCreated},
		{name: "create on another cluster does not count them", path: instanceLimitPaths[0].path, body: instanceLimitPaths[0].body,
			existing: []string{"cl-1", ""}, wantCode: http.StatusCreated},
		{name: "clone without a cluster and without a default cluster has no limit", path: "/api/v1/stack-instances/i0/clone",
			body: `{"name":"new-clone"}`, existing: []string{"", ""}, wantCode: http.StatusCreated},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newInstanceLimitEnv(t, tt.existing, tt.defaultCluster)

			w := env.post(tt.path, tt.body)

			if tt.wantCode == http.StatusConflict {
				assertLimitConflict(t, w)
				assert.Equal(t, len(tt.existing), env.count(t))
				return
			}
			require.Equal(t, tt.wantCode, w.Code, w.Body.String())
			assert.Equal(t, len(tt.existing)+1, env.count(t))
		})
	}
}

// TestInstanceLimit_CloneSourceClusterGone checks the answer for a clone
// whose source refers to a deleted cluster: the request names no cluster, so
// the message names the source.
func TestInstanceLimit_CloneSourceClusterGone(t *testing.T) {
	t.Parallel()
	env := newInstanceLimitEnv(t, []string{"cl-gone"}, false)

	w := env.post("/api/v1/stack-instances/i0/clone", `{"name":"new-clone"}`)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.JSONEq(t, `{"error":"The cluster of the source instance no longer exists"}`, w.Body.String())
	assert.Equal(t, 1, env.count(t))
}
