package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"backend/internal/cache"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// ---- Response types ----

// OverviewStats provides high-level aggregate counts across the platform.
type OverviewStats struct {
	TotalTemplates   int `json:"total_templates"`
	TotalDefinitions int `json:"total_definitions"`
	TotalInstances   int `json:"total_instances"`
	RunningInstances int `json:"running_instances"`
	TotalDeploys     int `json:"total_deploys"`
	TotalUsers       int `json:"total_users"`
}

// TemplateStats provides usage analytics for a single stack template.
type TemplateStats struct {
	TemplateID      string  `json:"template_id"`
	TemplateName    string  `json:"template_name"`
	Category        string  `json:"category"`
	IsPublished     bool    `json:"is_published"`
	DefinitionCount int     `json:"definition_count"`
	InstanceCount   int     `json:"instance_count"`
	DeployCount     int     `json:"deploy_count"`
	SuccessCount    int     `json:"success_count"`
	ErrorCount      int     `json:"error_count"`
	SuccessRate     float64 `json:"success_rate"`
}

// UserStats provides per-user usage analytics.
type UserStats struct {
	UserID        string     `json:"user_id"`
	Username      string     `json:"username"`
	InstanceCount int        `json:"instance_count"`
	DeployCount   int        `json:"deploy_count"`
	LastActive    *time.Time `json:"last_active,omitempty"`
}

// ---- Handler ----

// analyticsCacheTTL is the default TTL for analytics response caching.
const analyticsCacheTTL = 30 * time.Second

// userLogInfo aggregates deploy log stats per user for analytics computation.
type userLogInfo struct {
	deployCount int
	lastActive  *time.Time
}

// AnalyticsHandler provides read-only aggregation endpoints over existing data.
type AnalyticsHandler struct {
	templateRepo   models.StackTemplateRepository
	definitionRepo models.StackDefinitionRepository
	instanceRepo   models.StackInstanceRepository
	deployLogRepo  models.DeploymentLogRepository
	userRepo       models.UserRepository
	overviewCache  *cache.TTLCache[*OverviewStats]
	templateCache  *cache.TTLCache[[]TemplateStats]
}

// NewAnalyticsHandler creates a new AnalyticsHandler.
func NewAnalyticsHandler(
	templateRepo models.StackTemplateRepository,
	definitionRepo models.StackDefinitionRepository,
	instanceRepo models.StackInstanceRepository,
	deployLogRepo models.DeploymentLogRepository,
	userRepo models.UserRepository,
) *AnalyticsHandler {
	return &AnalyticsHandler{
		templateRepo:   templateRepo,
		definitionRepo: definitionRepo,
		instanceRepo:   instanceRepo,
		deployLogRepo:  deployLogRepo,
		userRepo:       userRepo,
		overviewCache:  cache.New[*OverviewStats](analyticsCacheTTL, analyticsCacheTTL),
		templateCache:  cache.New[[]TemplateStats](analyticsCacheTTL, analyticsCacheTTL),
	}
}

// GetOverview godoc
// @Summary     Get platform overview statistics
// @Description Returns high-level aggregate counts (templates, definitions, instances, deploys, users)
// @Tags        analytics
// @Produce     json
// @Success     200 {object} OverviewStats
// @Failure     500 {object} map[string]string
// @Router      /api/v1/analytics/overview [get]
func (h *AnalyticsHandler) GetOverview(c *gin.Context) {
	if cached, ok := h.overviewCache.Get("overview"); ok {
		c.JSON(http.StatusOK, cached)
		return
	}

	templateCount, err := h.templateRepo.Count()
	if err != nil {
		slog.Error("analytics: failed to count templates", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	definitionCount, err := h.definitionRepo.Count()
	if err != nil {
		slog.Error("analytics: failed to count definitions", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	totalInstances, err := h.instanceRepo.CountAll()
	if err != nil {
		slog.Error("analytics: failed to count instances", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	runningInstances, err := h.instanceRepo.CountByStatus(models.StackStatusRunning)
	if err != nil {
		slog.Error("analytics: failed to count running instances", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	totalDeploys, err := h.computeOverviewDeploys(c.Request.Context())
	if err != nil {
		slog.Error("analytics: failed to count deploys", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	userCount, err := h.userRepo.Count()
	if err != nil {
		slog.Error("analytics: failed to count users", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	stats := &OverviewStats{
		TotalTemplates:   int(templateCount),
		TotalDefinitions: int(definitionCount),
		TotalInstances:   totalInstances,
		RunningInstances: runningInstances,
		TotalDeploys:     totalDeploys,
		TotalUsers:       int(userCount),
	}
	h.overviewCache.Set("overview", stats)
	c.JSON(http.StatusOK, stats)
}

// GetTemplateStats godoc
// @Summary     Get per-template usage statistics
// @Description Returns usage analytics for each template including definition count, instance count, deploy counts, and success rate
// @Tags        analytics
// @Produce     json
// @Success     200 {array}  TemplateStats
// @Failure     500 {object} map[string]string
// @Router      /api/v1/analytics/templates [get]
func (h *AnalyticsHandler) GetTemplateStats(c *gin.Context) {
	if cached, ok := h.templateCache.Get("templates"); ok {
		c.JSON(http.StatusOK, cached)
		return
	}

	templates, err := h.templateRepo.List()
	if err != nil {
		slog.Error("analytics: failed to list templates", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	templateIDs := make([]string, len(templates))
	for i, t := range templates {
		templateIDs[i] = t.ID
	}

	result, err := h.computeTemplateStats(c.Request.Context(), templates, templateIDs)
	if err != nil {
		slog.Error("analytics: failed to compute template stats", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	h.templateCache.Set("templates", result)
	c.JSON(http.StatusOK, result)
}

// GetUserStats godoc
// @Summary     Get per-user usage statistics
// @Description Returns usage analytics per user including instance count, deploy count, and last active time (admin only)
// @Tags        analytics
// @Produce     json
// @Success     200 {array}  UserStats
// @Failure     500 {object} map[string]string
// @Router      /api/v1/analytics/users [get]
func (h *AnalyticsHandler) GetUserStats(c *gin.Context) {
	users, err := h.userRepo.List()
	if err != nil {
		slog.Error("analytics: failed to list users", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	ownerIDs := make([]string, len(users))
	for i, u := range users {
		ownerIDs[i] = u.ID
	}

	result, err := h.computeUserStats(c.Request.Context(), users, ownerIDs)
	if err != nil {
		slog.Error("analytics: failed to compute user stats", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	c.JSON(http.StatusOK, result)
}

// computeOverviewDeploys returns the total deploy count.
func (h *AnalyticsHandler) computeOverviewDeploys(ctx context.Context) (int, error) {
	return h.deployLogRepo.CountByAction(ctx, models.DeployActionDeploy)
}

// computeTemplateStats uses batch aggregation queries for per-template stats.
func (h *AnalyticsHandler) computeTemplateStats(ctx context.Context, templates []models.StackTemplate, templateIDs []string) ([]TemplateStats, error) {
	return h.computeTemplateStatsOptimized(ctx, templates, templateIDs)
}

// computeTemplateStatsOptimized uses batch aggregation queries. Returns an
// error (potentially wrapping ErrNotImplemented) if any repository call is
// unsupported, so the caller can fall back.
func (h *AnalyticsHandler) computeTemplateStatsOptimized(ctx context.Context, templates []models.StackTemplate, templateIDs []string) ([]TemplateStats, error) {
	defCountsByTemplate, err := h.definitionRepo.CountByTemplateIDs(templateIDs)
	if err != nil {
		return nil, err
	}

	defIDsByTemplate, err := h.definitionRepo.ListIDsByTemplateIDs(templateIDs)
	if err != nil {
		return nil, err
	}

	var allDefIDs []string
	for _, ids := range defIDsByTemplate {
		allDefIDs = append(allDefIDs, ids...)
	}

	instanceCountsByDef, err := h.instanceRepo.CountByDefinitionIDs(allDefIDs)
	if err != nil {
		return nil, err
	}

	instanceIDsByDef, err := h.instanceRepo.ListIDsByDefinitionIDs(allDefIDs)
	if err != nil {
		return nil, err
	}

	var allInstanceIDs []string
	for _, ids := range instanceIDsByDef {
		allInstanceIDs = append(allInstanceIDs, ids...)
	}

	summaries, err := h.collectDeploySummariesOrError(ctx, allInstanceIDs)
	if err != nil {
		return nil, err
	}

	result := make([]TemplateStats, 0, len(templates))
	for _, tmpl := range templates {
		defIDs := defIDsByTemplate[tmpl.ID]

		instanceCount := 0
		deployCount := 0
		successCount := 0
		errorCount := 0

		for _, defID := range defIDs {
			instanceCount += instanceCountsByDef[defID]
			for _, instID := range instanceIDsByDef[defID] {
				if s, ok := summaries[instID]; ok {
					deployCount += s.DeployCount
					successCount += s.SuccessCount
					errorCount += s.ErrorCount
				}
			}
		}

		successRate := 0.0
		if deployCount > 0 {
			successRate = float64(successCount) / float64(deployCount) * 100
		}

		result = append(result, TemplateStats{
			TemplateID:      tmpl.ID,
			TemplateName:    tmpl.Name,
			Category:        tmpl.Category,
			IsPublished:     tmpl.IsPublished,
			DefinitionCount: defCountsByTemplate[tmpl.ID],
			InstanceCount:   instanceCount,
			DeployCount:     deployCount,
			SuccessCount:    successCount,
			ErrorCount:      errorCount,
			SuccessRate:     successRate,
		})
	}
	return result, nil
}

// computeUserStats uses batch aggregation queries for per-user stats.
func (h *AnalyticsHandler) computeUserStats(ctx context.Context, users []models.User, ownerIDs []string) ([]UserStats, error) {
	return h.computeUserStatsOptimized(ctx, users, ownerIDs)
}

// computeUserStatsOptimized uses batch aggregation queries.
func (h *AnalyticsHandler) computeUserStatsOptimized(ctx context.Context, users []models.User, ownerIDs []string) ([]UserStats, error) {
	instanceCountsByOwner, err := h.instanceRepo.CountByOwnerIDs(ownerIDs)
	if err != nil {
		return nil, err
	}

	instanceIDsByOwner, err := h.instanceRepo.ListIDsByOwnerIDs(ownerIDs)
	if err != nil {
		return nil, err
	}

	var allInstanceIDs []string
	for _, ids := range instanceIDsByOwner {
		allInstanceIDs = append(allInstanceIDs, ids...)
	}

	summaries, err := h.collectDeploySummariesOrError(ctx, allInstanceIDs)
	if err != nil {
		return nil, err
	}

	userLogs := make(map[string]*userLogInfo)

	for ownerID, instIDs := range instanceIDsByOwner {
		for _, instID := range instIDs {
			s, ok := summaries[instID]
			if !ok {
				continue
			}
			info := userLogs[ownerID]
			if info == nil {
				info = &userLogInfo{}
				userLogs[ownerID] = info
			}
			info.deployCount += s.DeployCount
			if s.LastDeployAt != nil && (info.lastActive == nil || s.LastDeployAt.After(*info.lastActive)) {
				cp := *s.LastDeployAt
				info.lastActive = &cp
			}
		}
	}

	return h.buildUserStatsResult(users, instanceCountsByOwner, userLogs), nil
}

// buildUserStatsResult assembles the final []UserStats from the pre-computed maps.
func (h *AnalyticsHandler) buildUserStatsResult(users []models.User, instanceCounts map[string]int, userLogs map[string]*userLogInfo) []UserStats {
	result := make([]UserStats, 0, len(users))
	for _, u := range users {
		info := userLogs[u.ID]
		var deployCount int
		var lastActive *time.Time
		if info != nil {
			deployCount = info.deployCount
			lastActive = info.lastActive
		}
		result = append(result, UserStats{
			UserID:        u.ID,
			Username:      u.Username,
			InstanceCount: instanceCounts[u.ID],
			DeployCount:   deployCount,
			LastActive:    lastActive,
		})
	}
	return result
}

// collectDeploySummariesOrError fetches lightweight deploy log summaries for
// the given instance IDs in a single batched query and returns them indexed by
// instance ID. Propagates all errors to the caller.
func (h *AnalyticsHandler) collectDeploySummariesOrError(ctx context.Context, instanceIDs []string) (map[string]*models.DeployLogSummary, error) {
	if len(instanceIDs) == 0 {
		return make(map[string]*models.DeployLogSummary), nil
	}
	return h.deployLogRepo.SummarizeBatch(ctx, instanceIDs)
}
