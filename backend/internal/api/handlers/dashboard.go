package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"backend/internal/cache"
	"backend/internal/cluster"
	"backend/internal/k8s"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
)

const dashboardCacheTTL = 30 * time.Second

// ---- Response types ----

type DashboardResponse struct {
	Clusters          []DashboardCluster    `json:"clusters"`
	RecentDeployments []DashboardDeployment `json:"recent_deployments"`
	ExpiringSoon      []DashboardExpiring   `json:"expiring_soon"`
	FailingInstances  []DashboardFailing    `json:"failing_instances"`
}

type DashboardCluster struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	HealthStatus      string `json:"health_status"`
	NodeCount         *int   `json:"node_count,omitempty"`
	ReadyNodeCount    *int   `json:"ready_node_count,omitempty"`
	TotalCPU          string `json:"total_cpu,omitempty"`
	TotalMemory       string `json:"total_memory,omitempty"`
	AllocatableCPU    string `json:"allocatable_cpu,omitempty"`
	AllocatableMemory string `json:"allocatable_memory,omitempty"`
	NamespaceCount    *int   `json:"namespace_count,omitempty"`
}

type DashboardDeployment struct {
	ID              string     `json:"id"`
	StackInstanceID string     `json:"stack_instance_id"`
	InstanceName    string     `json:"instance_name"`
	Action          string     `json:"action"`
	Status          string     `json:"status"`
	StartedAt       time.Time  `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	Username        string     `json:"username,omitempty"`
}

type DashboardExpiring struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Namespace  string     `json:"namespace"`
	Status     string     `json:"status"`
	ExpiresAt  *time.Time `json:"expires_at"`
	TTLMinutes int        `json:"ttl_minutes"`
	ClusterID  string     `json:"cluster_id,omitempty"`
}

type DashboardFailing struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Namespace    string    `json:"namespace"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message"`
	ClusterID    string    `json:"cluster_id,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ---- Handler ----

type DashboardHandler struct {
	clusterRepo   models.ClusterRepository
	instanceRepo  models.StackInstanceRepository
	deployLogRepo models.DeploymentLogRepository
	registry      *cluster.Registry
	privCache     *cache.TTLCache[*DashboardResponse]
	basicCache    *cache.TTLCache[*DashboardResponse]
	sfGroup       singleflight.Group
}

func NewDashboardHandler(
	clusterRepo models.ClusterRepository,
	instanceRepo models.StackInstanceRepository,
	deployLogRepo models.DeploymentLogRepository,
	registry *cluster.Registry,
) *DashboardHandler {
	return &DashboardHandler{
		clusterRepo:   clusterRepo,
		instanceRepo:  instanceRepo,
		deployLogRepo: deployLogRepo,
		registry:      registry,
		privCache:     cache.New[*DashboardResponse](dashboardCacheTTL, dashboardCacheTTL),
		basicCache:    cache.New[*DashboardResponse](dashboardCacheTTL, dashboardCacheTTL),
	}
}

// Stop halts background cache cleanup goroutines.
func (h *DashboardHandler) Stop() {
	h.privCache.Stop()
	h.basicCache.Stop()
}

// GetDashboard godoc
// @Summary     Get dashboard overview
// @Description Returns aggregated dashboard data: cluster health, recent deployments, expiring instances, and failing instances.
// @Tags        dashboard
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} DashboardResponse
// @Failure     500 {object} map[string]string
// @Router      /api/v1/dashboard [get]
func (h *DashboardHandler) GetDashboard(c *gin.Context) {
	privileged := isPrivilegedRole(c)

	cacheStore := h.basicCache
	cacheKey := "dashboard:basic"
	if privileged {
		cacheStore = h.privCache
		cacheKey = "dashboard:privileged"
	}

	if cached, ok := cacheStore.Get(cacheKey); ok {
		c.JSON(http.StatusOK, cached)
		return
	}

	ctx := c.Request.Context()

	val, err, _ := h.sfGroup.Do(cacheKey, func() (interface{}, error) {
		// Re-check cache inside singleflight to avoid redundant work.
		if cached, ok := cacheStore.Get(cacheKey); ok {
			return cached, nil
		}
		return h.buildDashboard(ctx, privileged, cacheStore, cacheKey)
	})
	if err != nil {
		slog.Error("dashboard: failed to build dashboard", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	c.JSON(http.StatusOK, val)
}

func (h *DashboardHandler) buildDashboard(ctx context.Context, privileged bool, cacheStore *cache.TTLCache[*DashboardResponse], cacheKey string) (*DashboardResponse, error) {
	clusters, err := h.buildClusterData(ctx, privileged)
	if err != nil {
		return nil, err
	}

	recentDeploys, err := h.buildRecentDeployments(ctx)
	if err != nil {
		return nil, err
	}

	expiring, err := h.buildExpiringSoon()
	if err != nil {
		return nil, err
	}

	failing, err := h.buildFailingInstances()
	if err != nil {
		return nil, err
	}

	resp := &DashboardResponse{
		Clusters:          clusters,
		RecentDeployments: recentDeploys,
		ExpiringSoon:      expiring,
		FailingInstances:  failing,
	}

	cacheStore.Set(cacheKey, resp)
	return resp, nil
}

func (h *DashboardHandler) buildClusterData(ctx context.Context, privileged bool) ([]DashboardCluster, error) {
	clusters, err := h.clusterRepo.List()
	if err != nil {
		return nil, err
	}

	result := make([]DashboardCluster, len(clusters))
	for i, cl := range clusters {
		result[i] = DashboardCluster{
			ID:           cl.ID,
			Name:         cl.Name,
			HealthStatus: cl.HealthStatus,
		}
	}

	if !privileged || h.registry == nil {
		return result, nil
	}

	// Fan out health summary fetches with a 3s timeout per cluster.
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, cl := range clusters {
		wg.Add(1)
		go func(idx int, clusterID string) {
			defer wg.Done()
			fetchCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()

			k8sClient, err := h.registry.GetK8sClient(clusterID)
			if err != nil {
				slog.Warn("dashboard: failed to get k8s client", "cluster_id", clusterID, "error", err)
				return
			}

			summary, err := k8sClient.GetClusterSummary(fetchCtx)
			if err != nil {
				slog.Warn("dashboard: failed to get cluster summary", "cluster_id", clusterID, "error", err)
				return
			}

			h.enrichClusterFromSummary(&mu, &result[idx], summary)
		}(i, cl.ID)
	}
	wg.Wait()

	return result, nil
}

func (h *DashboardHandler) enrichClusterFromSummary(mu *sync.Mutex, dc *DashboardCluster, summary *k8s.ClusterSummary) {
	mu.Lock()
	defer mu.Unlock()
	dc.NodeCount = &summary.NodeCount
	dc.ReadyNodeCount = &summary.ReadyNodeCount
	dc.TotalCPU = summary.TotalCPU
	dc.TotalMemory = summary.TotalMemory
	dc.AllocatableCPU = summary.AllocatableCPU
	dc.AllocatableMemory = summary.AllocatableMemory
	dc.NamespaceCount = &summary.NamespaceCount
}

func (h *DashboardHandler) buildRecentDeployments(ctx context.Context) ([]DashboardDeployment, error) {
	logs, err := h.deployLogRepo.ListRecentGlobal(ctx, 10)
	if err != nil {
		return nil, err
	}

	result := make([]DashboardDeployment, len(logs))
	for i, l := range logs {
		result[i] = DashboardDeployment{
			ID:              l.ID,
			StackInstanceID: l.StackInstanceID,
			InstanceName:    l.InstanceName,
			Action:          l.Action,
			Status:          l.Status,
			StartedAt:       l.StartedAt,
			CompletedAt:     l.CompletedAt,
			Username:        l.Username,
		}
	}
	return result, nil
}

func (h *DashboardHandler) buildExpiringSoon() ([]DashboardExpiring, error) {
	instances, err := h.instanceRepo.ListExpiringSoon(1 * time.Hour)
	if err != nil {
		return nil, err
	}

	result := make([]DashboardExpiring, len(instances))
	for i, inst := range instances {
		result[i] = DashboardExpiring{
			ID:         inst.ID,
			Name:       inst.Name,
			Namespace:  inst.Namespace,
			Status:     inst.Status,
			ExpiresAt:  inst.ExpiresAt,
			TTLMinutes: inst.TTLMinutes,
			ClusterID:  inst.ClusterID,
		}
	}
	return result, nil
}

func (h *DashboardHandler) buildFailingInstances() ([]DashboardFailing, error) {
	failing, err := h.instanceRepo.ListByStatus(models.StackStatusError, 50)
	if err != nil {
		return nil, err
	}

	result := make([]DashboardFailing, len(failing))
	for i, inst := range failing {
		result[i] = DashboardFailing{
			ID:           inst.ID,
			Name:         inst.Name,
			Namespace:    inst.Namespace,
			Status:       inst.Status,
			ErrorMessage: inst.ErrorMessage,
			ClusterID:    inst.ClusterID,
			UpdatedAt:    inst.UpdatedAt,
		}
	}
	return result, nil
}
