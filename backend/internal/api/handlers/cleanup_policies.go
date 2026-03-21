package handlers

import (
	"log/slog"
	"net/http"

	"backend/internal/models"
	"backend/internal/scheduler"

	"github.com/gin-gonic/gin"
)

// CleanupPolicyHandler handles CRUD and manual execution of cleanup policies.
type CleanupPolicyHandler struct {
	repo      models.CleanupPolicyRepository
	scheduler *scheduler.Scheduler
}

// NewCleanupPolicyHandler creates a new CleanupPolicyHandler.
func NewCleanupPolicyHandler(repo models.CleanupPolicyRepository, sched *scheduler.Scheduler) *CleanupPolicyHandler {
	return &CleanupPolicyHandler{repo: repo, scheduler: sched}
}

// ListCleanupPolicies godoc
// @Summary     List all cleanup policies
// @Description Returns all cleanup policies
// @Tags        cleanup-policies
// @Produce     json
// @Success     200 {array}  models.CleanupPolicy
// @Failure     500 {object} map[string]string
// @Router      /api/v1/admin/cleanup-policies [get]
// @Security    BearerAuth
func (h *CleanupPolicyHandler) ListCleanupPolicies(c *gin.Context) {
	policies, err := h.repo.List()
	if err != nil {
		slog.Error("Failed to list cleanup policies", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}
	c.JSON(http.StatusOK, policies)
}

// CreateCleanupPolicy godoc
// @Summary     Create a cleanup policy
// @Description Creates a new cleanup policy and reloads the scheduler
// @Tags        cleanup-policies
// @Accept      json
// @Produce     json
// @Param       policy body     models.CleanupPolicy true "Cleanup policy"
// @Success     201    {object} models.CleanupPolicy
// @Failure     400    {object} map[string]string
// @Failure     500    {object} map[string]string
// @Router      /api/v1/admin/cleanup-policies [post]
// @Security    BearerAuth
func (h *CleanupPolicyHandler) CreateCleanupPolicy(c *gin.Context) {
	var policy models.CleanupPolicy
	if err := c.ShouldBindJSON(&policy); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	if err := policy.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.repo.Create(&policy); err != nil {
		status, msg := mapError(err, "Cleanup policy")
		c.JSON(status, gin.H{"error": msg})
		return
	}

	if h.scheduler != nil {
		if err := h.scheduler.Reload(); err != nil {
			slog.Error("Failed to reload scheduler after create", "error", err)
		}
	}

	c.JSON(http.StatusCreated, policy)
}

// UpdateCleanupPolicy godoc
// @Summary     Update a cleanup policy
// @Description Updates an existing cleanup policy and reloads the scheduler
// @Tags        cleanup-policies
// @Accept      json
// @Produce     json
// @Param       id     path     string               true "Policy ID"
// @Param       policy body     models.CleanupPolicy  true "Cleanup policy"
// @Success     200    {object} models.CleanupPolicy
// @Failure     400    {object} map[string]string
// @Failure     404    {object} map[string]string
// @Failure     500    {object} map[string]string
// @Router      /api/v1/admin/cleanup-policies/{id} [put]
// @Security    BearerAuth
func (h *CleanupPolicyHandler) UpdateCleanupPolicy(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Policy ID is required"})
		return
	}

	existing, err := h.repo.FindByID(id)
	if err != nil {
		status, msg := mapError(err, "Cleanup policy")
		c.JSON(status, gin.H{"error": msg})
		return
	}

	var policy models.CleanupPolicy
	if err := c.ShouldBindJSON(&policy); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	policy.ID = id
	policy.CreatedAt = existing.CreatedAt
	policy.LastRunAt = existing.LastRunAt

	if err := policy.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.repo.Update(&policy); err != nil {
		status, msg := mapError(err, "Cleanup policy")
		c.JSON(status, gin.H{"error": msg})
		return
	}

	if h.scheduler != nil {
		if err := h.scheduler.Reload(); err != nil {
			slog.Error("Failed to reload scheduler after update", "error", err)
		}
	}

	c.JSON(http.StatusOK, policy)
}

// DeleteCleanupPolicy godoc
// @Summary     Delete a cleanup policy
// @Description Deletes a cleanup policy and reloads the scheduler
// @Tags        cleanup-policies
// @Param       id path string true "Policy ID"
// @Success     204
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/admin/cleanup-policies/{id} [delete]
// @Security    BearerAuth
func (h *CleanupPolicyHandler) DeleteCleanupPolicy(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Policy ID is required"})
		return
	}

	if err := h.repo.Delete(id); err != nil {
		status, msg := mapError(err, "Cleanup policy")
		c.JSON(status, gin.H{"error": msg})
		return
	}

	if h.scheduler != nil {
		if err := h.scheduler.Reload(); err != nil {
			slog.Error("Failed to reload scheduler after delete", "error", err)
		}
	}

	c.Status(http.StatusNoContent)
}

// RunCleanupPolicy godoc
// @Summary     Run a cleanup policy manually
// @Description Executes a cleanup policy immediately. Use ?dry_run=true to preview matches without acting.
// @Tags        cleanup-policies
// @Produce     json
// @Param       id      path  string true  "Policy ID"
// @Param       dry_run query bool   false "Dry run mode"
// @Success     200 {array}  scheduler.CleanupResult
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/admin/cleanup-policies/{id}/run [post]
// @Security    BearerAuth
func (h *CleanupPolicyHandler) RunCleanupPolicy(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Policy ID is required"})
		return
	}

	dryRun := c.Query("dry_run") == "true"

	if h.scheduler == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Scheduler not available"})
		return
	}

	results, err := h.scheduler.RunPolicy(id, dryRun)
	if err != nil {
		status, msg := mapError(err, "Cleanup policy")
		c.JSON(status, gin.H{"error": msg})
		return
	}

	if results == nil {
		results = []scheduler.CleanupResult{}
	}

	c.JSON(http.StatusOK, results)
}
