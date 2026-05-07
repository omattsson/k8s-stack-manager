package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- dashboard-specific deploy log mock ----

type dashboardMockDeployLogRepo struct {
	mu    sync.RWMutex
	items []models.DeploymentLogWithContext
	err   error
}

func (m *dashboardMockDeployLogRepo) Create(_ context.Context, _ *models.DeploymentLog) error {
	return nil
}
func (m *dashboardMockDeployLogRepo) FindByID(_ context.Context, _ string) (*models.DeploymentLog, error) {
	return nil, nil
}
func (m *dashboardMockDeployLogRepo) Update(_ context.Context, _ *models.DeploymentLog) error {
	return nil
}
func (m *dashboardMockDeployLogRepo) ListByInstance(_ context.Context, _ string) ([]models.DeploymentLog, error) {
	return nil, nil
}
func (m *dashboardMockDeployLogRepo) ListByInstancePaginated(_ context.Context, _ models.DeploymentLogFilters) (*models.DeploymentLogResult, error) {
	return nil, nil
}
func (m *dashboardMockDeployLogRepo) GetLatestByInstance(_ context.Context, _ string) (*models.DeploymentLog, error) {
	return nil, nil
}
func (m *dashboardMockDeployLogRepo) SummarizeByInstance(_ context.Context, _ string) (*models.DeployLogSummary, error) {
	return nil, nil
}
func (m *dashboardMockDeployLogRepo) SummarizeBatch(_ context.Context, _ []string) (map[string]*models.DeployLogSummary, error) {
	return nil, nil
}
func (m *dashboardMockDeployLogRepo) CountByAction(_ context.Context, _ string) (int, error) {
	return 0, nil
}

func (m *dashboardMockDeployLogRepo) ListRecentGlobal(_ context.Context, limit int) ([]models.DeploymentLogWithContext, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	if limit <= 0 || limit > len(m.items) {
		return m.items, nil
	}
	return m.items[:limit], nil
}

func (m *dashboardMockDeployLogRepo) setItems(items []models.DeploymentLogWithContext) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = items
}

func (m *dashboardMockDeployLogRepo) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// ---- test helpers ----

func setupDashboardRouter(
	clusterRepo *MockClusterRepository,
	instanceRepo *MockStackInstanceRepository,
	deployLogRepo *dashboardMockDeployLogRepo,
	role string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext("user-1", role))

	h := NewDashboardHandler(clusterRepo, instanceRepo, deployLogRepo, nil)
	defer h.Stop()

	r.GET("/api/v1/dashboard", h.GetDashboard)
	return r
}

func callDashboard(router *gin.Engine) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	router.ServeHTTP(w, req)
	return w
}

// ---- tests ----

func TestDashboard_EmptyState(t *testing.T) {
	t.Parallel()

	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	deployLogRepo := &dashboardMockDeployLogRepo{}

	router := setupDashboardRouter(clusterRepo, instanceRepo, deployLogRepo, "developer")
	w := callDashboard(router)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp DashboardResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Empty(t, resp.Clusters)
	assert.Empty(t, resp.RecentDeployments)
	assert.Empty(t, resp.ExpiringSoon)
	assert.Empty(t, resp.FailingInstances)
}

func TestDashboard_BasicUserGetsNoK8sMetrics(t *testing.T) {
	t.Parallel()

	clusterRepo := NewMockClusterRepository()
	_ = clusterRepo.Create(&models.Cluster{ID: "c1", Name: "dev", HealthStatus: "healthy"})

	instanceRepo := NewMockStackInstanceRepository()
	deployLogRepo := &dashboardMockDeployLogRepo{}

	router := setupDashboardRouter(clusterRepo, instanceRepo, deployLogRepo, "developer")
	w := callDashboard(router)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp DashboardResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	require.Len(t, resp.Clusters, 1)
	assert.Equal(t, "dev", resp.Clusters[0].Name)
	assert.Equal(t, "healthy", resp.Clusters[0].HealthStatus)
	assert.Nil(t, resp.Clusters[0].NodeCount, "basic user should not see node metrics")
}

func TestDashboard_AdminGetsClusterData(t *testing.T) {
	t.Parallel()

	clusterRepo := NewMockClusterRepository()
	_ = clusterRepo.Create(&models.Cluster{ID: "c1", Name: "prod", HealthStatus: "healthy"})

	instanceRepo := NewMockStackInstanceRepository()
	deployLogRepo := &dashboardMockDeployLogRepo{}

	// Admin role, but no registry — so no enrichment, but still gets the cluster data
	router := setupDashboardRouter(clusterRepo, instanceRepo, deployLogRepo, "admin")
	w := callDashboard(router)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp DashboardResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	require.Len(t, resp.Clusters, 1)
	assert.Equal(t, "prod", resp.Clusters[0].Name)
}

func TestDashboard_RecentDeployments(t *testing.T) {
	t.Parallel()

	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	deployLogRepo := &dashboardMockDeployLogRepo{}

	now := time.Now()
	deployLogRepo.setItems([]models.DeploymentLogWithContext{
		{
			DeploymentLog: models.DeploymentLog{
				ID:              "d1",
				StackInstanceID: "i1",
				Action:          "deploy",
				Status:          "completed",
				StartedAt:       now.Add(-5 * time.Minute),
			},
			InstanceName: "my-stack",
			Username:     "alice",
		},
	})

	router := setupDashboardRouter(clusterRepo, instanceRepo, deployLogRepo, "developer")
	w := callDashboard(router)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp DashboardResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	require.Len(t, resp.RecentDeployments, 1)
	assert.Equal(t, "my-stack", resp.RecentDeployments[0].InstanceName)
	assert.Equal(t, "alice", resp.RecentDeployments[0].Username)
}

func TestDashboard_ExpiringSoon(t *testing.T) {
	t.Parallel()

	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	deployLogRepo := &dashboardMockDeployLogRepo{}

	expiresAt := time.Now().Add(30 * time.Minute)
	_ = instanceRepo.Create(&models.StackInstance{
		ID:         "i1",
		Name:       "expiring-stack",
		Namespace:  "ns-1",
		Status:     models.StackStatusRunning,
		ExpiresAt:  &expiresAt,
		TTLMinutes: 60,
	})

	router := setupDashboardRouter(clusterRepo, instanceRepo, deployLogRepo, "developer")
	w := callDashboard(router)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp DashboardResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	require.Len(t, resp.ExpiringSoon, 1)
	assert.Equal(t, "expiring-stack", resp.ExpiringSoon[0].Name)
}

func TestDashboard_FailingInstances(t *testing.T) {
	t.Parallel()

	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	deployLogRepo := &dashboardMockDeployLogRepo{}

	_ = instanceRepo.Create(&models.StackInstance{
		ID:           "i1",
		Name:         "broken-stack",
		Namespace:    "ns-1",
		Status:       models.StackStatusError,
		ErrorMessage: "helm install failed",
	})

	router := setupDashboardRouter(clusterRepo, instanceRepo, deployLogRepo, "developer")
	w := callDashboard(router)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp DashboardResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	require.Len(t, resp.FailingInstances, 1)
	assert.Equal(t, "broken-stack", resp.FailingInstances[0].Name)
	assert.Equal(t, "helm install failed", resp.FailingInstances[0].ErrorMessage)
}

func TestDashboard_ClusterRepoError(t *testing.T) {
	t.Parallel()

	clusterRepo := NewMockClusterRepository()
	clusterRepo.SetError(errors.New("db connection lost"))

	instanceRepo := NewMockStackInstanceRepository()
	deployLogRepo := &dashboardMockDeployLogRepo{}

	router := setupDashboardRouter(clusterRepo, instanceRepo, deployLogRepo, "developer")
	w := callDashboard(router)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestDashboard_PartialFailure_DeployLogs(t *testing.T) {
	t.Parallel()

	clusterRepo := NewMockClusterRepository()
	_ = clusterRepo.Create(&models.Cluster{ID: "c1", Name: "dev", HealthStatus: "healthy"})

	instanceRepo := NewMockStackInstanceRepository()
	deployLogRepo := &dashboardMockDeployLogRepo{}
	deployLogRepo.setError(errors.New("deploy log query failed"))

	router := setupDashboardRouter(clusterRepo, instanceRepo, deployLogRepo, "developer")
	w := callDashboard(router)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestDashboard_CacheHit(t *testing.T) {
	t.Parallel()

	clusterRepo := NewMockClusterRepository()
	instanceRepo := NewMockStackInstanceRepository()
	deployLogRepo := &dashboardMockDeployLogRepo{}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext("user-1", "developer"))

	h := NewDashboardHandler(clusterRepo, instanceRepo, deployLogRepo, nil)
	defer h.Stop()
	r.GET("/api/v1/dashboard", h.GetDashboard)

	// First call populates cache
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	r.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	// Inject error — second call should still succeed from cache
	clusterRepo.SetError(errors.New("should not be called"))

	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	r.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, w1.Body.String(), w2.Body.String())
}
