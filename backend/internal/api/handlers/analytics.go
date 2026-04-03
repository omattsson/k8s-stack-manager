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

	ctx := c.Request.Context()
	totalDeploys, err := h.deployLogRepo.CountByAction(ctx, models.DeployActionDeploy)
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

	// Count definitions per template using aggregation query.
	defCountsByTemplate, err := h.definitionRepo.CountByTemplateIDs(templateIDs)
	if err != nil {
		slog.Error("analytics: failed to count definitions by template", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Get definition IDs grouped by template for instance lookups.
	defIDsByTemplate, err := h.definitionRepo.ListIDsByTemplateIDs(templateIDs)
	if err != nil {
		slog.Error("analytics: failed to list definition IDs by template", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Collect all definition IDs across all templates.
	var allDefIDs []string
	for _, ids := range defIDsByTemplate {
		allDefIDs = append(allDefIDs, ids...)
	}

	// Count instances per definition using aggregation query.
	instanceCountsByDef, err := h.instanceRepo.CountByDefinitionIDs(allDefIDs)
	if err != nil {
		slog.Error("analytics: failed to count instances by definition", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Get instance IDs per definition for deploy log summaries.
	instanceIDsByDef, err := h.instanceRepo.ListIDsByDefinitionIDs(allDefIDs)
	if err != nil {
		slog.Error("analytics: failed to list instance IDs by definition", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Collect all instance IDs for batch deploy log summary.
	var allInstanceIDs []string
	for _, ids := range instanceIDsByDef {
		allInstanceIDs = append(allInstanceIDs, ids...)
	}

	ctx := c.Request.Context()
	summaries := h.collectDeploySummariesByIDs(ctx, allInstanceIDs)

	result := make([]TemplateStats, 0, len(templates))
	for _, tmpl := range templates {
		defIDs := defIDsByTemplate[tmpl.ID]

		// Aggregate instance counts and deploy stats across all definitions for this template.
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

	// Count instances per owner using aggregation query.
	instanceCountsByOwner, err := h.instanceRepo.CountByOwnerIDs(ownerIDs)
	if err != nil {
		slog.Error("analytics: failed to count instances by owner", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Get instance IDs per owner for deploy log summaries.
	instanceIDsByOwner, err := h.instanceRepo.ListIDsByOwnerIDs(ownerIDs)
	if err != nil {
		slog.Error("analytics: failed to list instance IDs by owner", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Collect all instance IDs for batch deploy log summary.
	var allInstanceIDs []string
	for _, ids := range instanceIDsByOwner {
		allInstanceIDs = append(allInstanceIDs, ids...)
	}

	ctx := c.Request.Context()
	summaries := h.collectDeploySummariesByIDs(ctx, allInstanceIDs)

	// Build per-user summary index.
	type userLogInfo struct {
		deployCount int
		lastActive  *time.Time
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
			InstanceCount: instanceCountsByOwner[u.ID],
			DeployCount:   deployCount,
			LastActive:    lastActive,
		})
	}

	c.JSON(http.StatusOK, result)
}

// collectDeploySummariesByIDs fetches lightweight deploy log summaries for the
// given instance IDs in a single batched query and returns them indexed by instance ID.
func (h *AnalyticsHandler) collectDeploySummariesByIDs(ctx context.Context, instanceIDs []string) map[string]*models.DeployLogSummary {
	if len(instanceIDs) == 0 {
		return make(map[string]*models.DeployLogSummary)
	}

	summaries, err := h.deployLogRepo.SummarizeBatch(ctx, instanceIDs)
	if err != nil {
		slog.Error("analytics: failed to batch-summarize deploy logs", "count", len(instanceIDs), "error", err)
		return make(map[string]*models.DeployLogSummary)
	}
	return summaries
}
