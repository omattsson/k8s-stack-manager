package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// Analytics handler message constants.
const (
	msgAnalyticsListInstances = "analytics: failed to list instances"
)


// maxDeployLogWorkers limits the number of concurrent goroutines used to
// fetch deploy log summaries in collectDeploySummaries, preventing excessive
// fan-out against the backing store.
const maxDeployLogWorkers = 20

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

// AnalyticsHandler provides read-only aggregation endpoints over existing data.
type AnalyticsHandler struct {
	templateRepo   models.StackTemplateRepository
	definitionRepo models.StackDefinitionRepository
	instanceRepo   models.StackInstanceRepository
	deployLogRepo  models.DeploymentLogRepository
	userRepo       models.UserRepository
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
	templates, err := h.templateRepo.List()
	if err != nil {
		slog.Error("analytics: failed to list templates", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	definitions, err := h.definitionRepo.List()
	if err != nil {
		slog.Error("analytics: failed to list definitions", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	instances, err := h.instanceRepo.List()
	if err != nil {
		slog.Error(msgAnalyticsListInstances, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	users, err := h.userRepo.List()
	if err != nil {
		slog.Error("analytics: failed to list users", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	ctx := c.Request.Context()
	summaries := h.collectDeploySummaries(ctx, instances)

	totalDeploys := 0
	for _, s := range summaries {
		totalDeploys += s.DeployCount
	}

	running := 0
	for _, inst := range instances {
		if inst.Status == models.StackStatusRunning {
			running++
		}
	}

	c.JSON(http.StatusOK, OverviewStats{
		TotalTemplates:   len(templates),
		TotalDefinitions: len(definitions),
		TotalInstances:   len(instances),
		RunningInstances: running,
		TotalDeploys:     totalDeploys,
		TotalUsers:       len(users),
	})
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
	templates, err := h.templateRepo.List()
	if err != nil {
		slog.Error("analytics: failed to list templates", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Fetch all definitions and group by SourceTemplateID.
	allDefinitions, err := h.definitionRepo.List()
	if err != nil {
		slog.Error("analytics: failed to list definitions", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	defsByTemplate := make(map[string][]models.StackDefinition)
	for _, d := range allDefinitions {
		if d.SourceTemplateID != "" {
			defsByTemplate[d.SourceTemplateID] = append(defsByTemplate[d.SourceTemplateID], d)
		}
	}

	// Fetch all instances and group by StackDefinitionID.
	allInstances, err := h.instanceRepo.List()
	if err != nil {
		slog.Error(msgAnalyticsListInstances, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	instancesByDef := make(map[string][]models.StackInstance)
	for _, inst := range allInstances {
		instancesByDef[inst.StackDefinitionID] = append(instancesByDef[inst.StackDefinitionID], inst)
	}

	// Collect lightweight deploy summaries for all instances (avoid N+1 per template).
	ctx := c.Request.Context()
	summaries := h.collectDeploySummaries(ctx, allInstances)

	result := make([]TemplateStats, 0, len(templates))
	for _, tmpl := range templates {
		defs := defsByTemplate[tmpl.ID]

		// Count instances and collect deploy log stats across all definitions for this template.
		instanceCount := 0
		deployCount := 0
		successCount := 0
		errorCount := 0

		for _, def := range defs {
			insts := instancesByDef[def.ID]
			instanceCount += len(insts)
			for _, inst := range insts {
				if s, ok := summaries[inst.ID]; ok {
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
			DefinitionCount: len(defs),
			InstanceCount:   instanceCount,
			DeployCount:     deployCount,
			SuccessCount:    successCount,
			ErrorCount:      errorCount,
			SuccessRate:     successRate,
		})
	}

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

	allInstances, err := h.instanceRepo.List()
	if err != nil {
		slog.Error(msgAnalyticsListInstances, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Group instances by owner.
	instancesByOwner := make(map[string][]models.StackInstance)
	for _, inst := range allInstances {
		instancesByOwner[inst.OwnerID] = append(instancesByOwner[inst.OwnerID], inst)
	}

	// Collect lightweight deploy summaries for all instances.
	ctx := c.Request.Context()
	summaries := h.collectDeploySummaries(ctx, allInstances)

	// Build per-user summary index.
	type userLogInfo struct {
		deployCount int
		lastActive  *time.Time
	}
	userLogs := make(map[string]*userLogInfo)
	for _, inst := range allInstances {
		s, ok := summaries[inst.ID]
		if !ok {
			continue
		}
		info := userLogs[inst.OwnerID]
		if info == nil {
			info = &userLogInfo{}
			userLogs[inst.OwnerID] = info
		}
		info.deployCount += s.DeployCount
		if s.LastDeployAt != nil && (info.lastActive == nil || s.LastDeployAt.After(*info.lastActive)) {
			cp := *s.LastDeployAt
			info.lastActive = &cp
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
			InstanceCount: len(instancesByOwner[u.ID]),
			DeployCount:   deployCount,
			LastActive:    lastActive,
		})
	}

	c.JSON(http.StatusOK, result)
}

// collectDeploySummaries fetches lightweight deploy log summaries for each
// instance concurrently and returns them indexed by instance ID. A bounded
// worker pool of maxDeployLogWorkers goroutines prevents excessive fan-out
// against the backing store.
func (h *AnalyticsHandler) collectDeploySummaries(ctx context.Context, instances []models.StackInstance) map[string]*models.DeployLogSummary {
	result := make(map[string]*models.DeployLogSummary, len(instances))
	if len(instances) == 0 {
		return result
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxDeployLogWorkers)

	for _, inst := range instances {
		wg.Add(1)
		sem <- struct{}{} // acquire slot
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }() // release slot

			summary, err := h.deployLogRepo.SummarizeByInstance(ctx, id)
			if err != nil {
				slog.Error("analytics: failed to summarize deploy logs", "instance_id", id, "error", err)
				return
			}
			mu.Lock()
			result[id] = summary
			mu.Unlock()
		}(inst.ID)
	}
	wg.Wait()
	return result
}
