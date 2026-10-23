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

func newInstanceLimitEnv(t *testing.T, existing int) *instanceLimitEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	clusters := NewMockClusterRepository()
	require.NoError(t, clusters.Create(context.Background(), &models.Cluster{
		ID: "cl-1", Name: "one", APIServerURL: "https://one.example.com", KubeconfigPath: "/kc", MaxInstancesPerUser: 2,
	}))
	insts := NewMockStackInstanceRepository()
	defs := NewMockStackDefinitionRepository()
	seedDefinition(t, defs, "d1", "def-1", "uid-1")
	for i := 0; i < existing; i++ {
		require.NoError(t, insts.Create(context.Background(), &models.StackInstance{
			ID: fmt.Sprintf("i%d", i), Name: fmt.Sprintf("existing-%d", i), StackDefinitionID: "d1",
			Namespace: fmt.Sprintf("stack-existing-%d-alice", i), OwnerID: "uid-1", ClusterID: "cl-1",
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

func (e *instanceLimitEnv) count(t *testing.T) int {
	t.Helper()
	n, err := e.insts.CountByClusterAndOwner(context.Background(), "cl-1", "uid-1")
	require.NoError(t, err)
	return n
}

// TestInstanceLimit_AllCreatePaths checks that create, clone and quick deploy
// apply the per-user instance limit of the cluster with the same 409, and a
// failed cluster read stops the create (#682).
func TestInstanceLimit_AllCreatePaths(t *testing.T) {
	t.Parallel()

	paths := []struct {
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
	cases := []struct {
		fetchErr error
		name     string
		existing int
		wantCode int // 0: the success code of the path
		wantNew  int
	}{
		{name: "below the limit", existing: 1, wantNew: 1},
		{name: "at the limit", existing: 2, wantCode: http.StatusConflict},
		{name: "failed cluster read", existing: 1, fetchErr: errors.New("db down"), wantCode: http.StatusInternalServerError},
	}
	for _, p := range paths {
		for _, tc := range cases {
			p, tc := p, tc
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				env := newInstanceLimitEnv(t, tc.existing)
				if tc.fetchErr != nil {
					env.clusters.SetFetchError(tc.fetchErr)
				}

				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, p.path, bytes.NewBufferString(p.body))
				req.Header.Set("Content-Type", "application/json")
				env.router.ServeHTTP(w, req)

				want := tc.wantCode
				if want == 0 {
					want = p.wantOK
				}
				require.Equal(t, want, w.Code, w.Body.String())
				assert.Equal(t, tc.existing+tc.wantNew, env.count(t))
				switch want {
				case http.StatusConflict:
					var body map[string]string
					require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
					assert.Equal(t, "Maximum instances per user reached for this cluster (limit: 2)", body["error"])
				case http.StatusInternalServerError:
					assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
				}
			})
		}
	}
}
