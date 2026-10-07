package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/hooks"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBulkDeploy_PreDeployHookCarriesChartBranchOverride checks that bulk
// deploy reports the per-chart branch override, and its image tag, to the
// pre-deploy hook. The rendered {{.ImageTag}} uses the override, so a CI gate
// must check and build the same tag.
func TestBulkDeploy_PreDeployHookCarriesChartBranchOverride(t *testing.T) {
	t.Parallel()

	rec := newHandlerHookRecorder(t)
	dispatcher, err := hooks.NewDispatcher(hooks.Config{Subscriptions: []hooks.Subscription{{
		Name:          "gate",
		Events:        []string{hooks.EventPreDeploy},
		URL:           rec.server.URL,
		FailurePolicy: hooks.FailurePolicyIgnore,
	}}}, rec.server.Client())
	require.NoError(t, err)

	instRepo := NewMockStackInstanceRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	logRepo := newMockDeployLogRepo()

	seedInstance(t, instRepo, "i1", "stack-a", "d1", "uid-1", models.StackStatusDraft)
	seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
	require.NoError(t, ccRepo.Create(&models.ChartConfig{
		ID:                "c1",
		StackDefinitionID: "d1",
		ChartName:         "app",
		RepositoryURL:     "oci://example.com/charts/app",
		BuildPipelineID:   "42",
		DefaultValues:     "image:\n  tag: \"{{.ImageTag}}\"\n",
		DeployOrder:       1,
	}))
	require.NoError(t, boRepo.Set(&models.ChartBranchOverride{
		ID:              "bo1",
		StackInstanceID: "i1",
		ChartConfigID:   "c1",
		Branch:          "Feature/App_Fix",
	}))

	mgr := deployer.NewManager(deployer.ManagerConfig{
		Registry:      cluster.NewRegistryForTest("test-cluster", nil, &noopHelmExecutor{}),
		InstanceRepo:  instRepo,
		DeployLogRepo: logRepo,
		Hub:           &MockBroadcastSender{},
		MaxConcurrent: 2,
		Hooks:         dispatcher,
	})

	h, err := NewInstanceHandlerWithDeployer(
		instRepo, NewMockValueOverrideRepository(), boRepo, defRepo, ccRepo,
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(),
		helm.NewValuesGenerator(), NewMockUserRepository(),
		mgr, nil, nil, logRepo, nil,
		0,
		&mockHandlerTxRunner{repos: database.TxRepos{StackInstance: instRepo, BranchOverride: boRepo}},
	)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", "uid-1")
		c.Set("username", "alice")
		c.Set("role", "devops")
		c.Next()
	})
	r.POST("/api/v1/stack-instances/bulk/deploy", h.BulkDeploy)

	body, _ := json.Marshal(BulkOperationRequest{InstanceIDs: []string{"i1"}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stack-instances/bulk/deploy", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var charts []hooks.ChartRef
	require.Eventually(t, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		for _, e := range rec.events {
			if e.event == hooks.EventPreDeploy {
				charts = e.envelope.Charts
				return true
			}
		}
		return false
	}, 2*time.Second, 20*time.Millisecond)

	require.Len(t, charts, 1)
	assert.Equal(t, "Feature/App_Fix", charts[0].Branch)
	assert.Equal(t, "feature-app-fix", charts[0].ImageTag)
	assert.Equal(t, "42", charts[0].BuildPipelineID)
}
