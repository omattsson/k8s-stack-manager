package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/imagedigest"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hangingDigestRegistry answers /v2/ at once and holds every manifest request
// until the client leaves. arrived gets one value per manifest request.
func hangingDigestRegistry(t *testing.T) (host string, arrived chan struct{}, rt http.RoundTripper) {
	t.Helper()
	arrived = make(chan struct{}, 16)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/manifests/") {
			arrived <- struct{}{}
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "https://"), arrived, srv.Client().Transport
}

// TestImageDigest_CancelDuringLookupFailsClosed (#811 F1): a client that
// leaves during a digest lookup gets 499 and nothing is deployed, instead
// of a deploy with an empty digest that restarts every pod.
func TestImageDigest_CancelDuringLookupFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		route  string
		url    string
		call   func(h *InstanceHandler) gin.HandlerFunc
	}{
		{name: "deploy", method: http.MethodPost, route: "/api/v1/stack-instances/:id/deploy", url: "/api/v1/stack-instances/inst-1/deploy", call: func(h *InstanceHandler) gin.HandlerFunc { return h.DeployInstance }},
		{name: "deploy preview", method: http.MethodGet, route: "/api/v1/stack-instances/:id/deploy-preview", url: "/api/v1/stack-instances/inst-1/deploy-preview", call: func(h *InstanceHandler) gin.HandlerFunc { return h.DeployPreview }},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			host, arrived, rt := hangingDigestRegistry(t)
			f := newSVFixture(t, "test-cluster")
			require.NoError(t, f.clusterRepo.Create(context.Background(), &models.Cluster{ID: "test-cluster", Name: "test-cluster", RegistryURL: host}))
			ch, err := f.ccRepo.FindByID(context.Background(), "c1")
			require.NoError(t, err)
			ch.DefaultValues = digestValues(host)
			require.NoError(t, f.ccRepo.Update(context.Background(), ch))

			rec := newRecordingHelmExecutor()
			mgr := deployer.NewManager(deployer.ManagerConfig{
				Registry:      cluster.NewRegistryForTest("test-cluster", nil, rec),
				InstanceRepo:  f.instRepo,
				DeployLogRepo: NewMockDeploymentLogRepository(),
				Hub:           &MockBroadcastSender{},
				MaxConcurrent: 2,
			})
			h := f.handler(t, f.svRepo, nil, mgr).WithImageDigests(imagedigest.NewService(nil, imagedigest.WithTransport(rt)))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				<-arrived
				cancel()
			}()
			w := httptest.NewRecorder()
			svRouter(tt.method, tt.route, tt.call(h)).ServeHTTP(w, httptest.NewRequest(tt.method, tt.url, nil).WithContext(ctx))
			assert.Equal(t, 499, w.Code, w.Body.String())

			time.Sleep(100 * time.Millisecond)
			_, installed := rec.get("app")
			assert.False(t, installed, "nothing may be deployed")
			inst, err := f.instRepo.FindByID(context.Background(), "inst-1")
			require.NoError(t, err)
			assert.Equal(t, models.StackStatusDraft, inst.Status)
		})
	}
}

// TestQuickDeploy_DigestLookupsKeepDeployBudget (#811 F4): slow lookups run
// on their own budget, so the deploy still starts (with an empty digest)
// when the write context of the caller is short.
func TestQuickDeploy_DigestLookupsKeepDeployBudget(t *testing.T) {
	t.Parallel()

	host, _, rt := hangingDigestRegistry(t)
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	instRepo := NewMockStackInstanceRepository()
	clusterRepo := NewMockClusterRepository()
	require.NoError(t, clusterRepo.Create(context.Background(), &models.Cluster{ID: "test-cluster", Name: "test-cluster", RegistryURL: host}))
	seedDefinition(t, defRepo, "d1", "Def", "uid-1")
	chart := models.ChartConfig{ID: "c1", StackDefinitionID: "d1", ChartName: "svc", RepositoryURL: "oci://example.com/charts/svc", DefaultValues: digestValues(host)}
	require.NoError(t, ccRepo.Create(context.Background(), &chart))
	inst := seedInstance(t, instRepo, "inst-1", "qd", "d1", "uid-1", models.StackStatusDraft)
	inst.ClusterID = "test-cluster"
	require.NoError(t, instRepo.Update(context.Background(), inst))
	def, err := defRepo.FindByID(context.Background(), "d1")
	require.NoError(t, err)

	rec := newRecordingHelmExecutor()
	clReg := cluster.NewRegistryForTest("test-cluster", nil, rec)
	mgr := deployer.NewManager(deployer.ManagerConfig{
		Registry:      clReg,
		InstanceRepo:  instRepo,
		DeployLogRepo: NewMockDeploymentLogRepository(),
		Hub:           &MockBroadcastSender{},
		MaxConcurrent: 2,
	})
	h, err := NewQuickDeployHandler(
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), defRepo, ccRepo,
		instRepo, NewMockChartBranchOverrideRepository(), NewMockValueOverrideRepository(), helm.NewValuesGenerator(),
		mgr, NewMockUserRepository(), nil, NewMockAuditLogRepository(),
		&MockBroadcastSender{}, clReg, nil, 0,
		&mockHandlerTxRunner{repos: database.TxRepos{StackDefinition: defRepo, ChartConfig: ccRepo, StackInstance: instRepo}},
	)
	require.NoError(t, err)
	h.WithClusterRepo(clusterRepo).WithImageDigests(imagedigest.NewService(nil,
		imagedigest.WithTransport(rt), imagedigest.WithTimeout(600*time.Millisecond)))

	// The write context of the caller ends before the lookup does.
	wctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/templates/t1/quick-deploy", nil)

	logID, err := h.triggerDeploy(wctx, c, inst, def, []models.ChartConfig{chart}, "alice")
	require.NoError(t, err)
	assert.NotEmpty(t, logID)
	require.Eventually(t, func() bool {
		_, ok := rec.get("svc")
		return ok
	}, 3*time.Second, 20*time.Millisecond)
	svc, _ := rec.get("svc")
	assert.Equal(t, "", annotation(t, svc), "a failed lookup renders an empty digest and the deploy continues")
}
