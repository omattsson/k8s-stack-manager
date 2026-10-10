package handlers

import (
	"log/slog"
	"net/http"

	"backend/internal/models"
	"backend/internal/scheduler"

	"github.com/gin-gonic/gin"
)

// Cleanup policy handler message constants.
const (
	entityCleanupPolicy = "Cleanup policy"
	msgPolicyIDRequired = "Policy ID is required"
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	c.JSON(http.StatusOK, policies)
}

// CreateCleanupPolicy godoc
// @Summary     Create a cleanup policy
// @Description Creates a new cleanup policy and reloads the scheduler. condition is a comma-separated list of key:value pairs (all must match): status:<status>; idle_days:<N> (no deploy for N days, creation time when never deployed); age_days:<N> (created more than N days ago); stopped_days:<N> (stopped, and the last stop finished N or more days ago; instances without a recorded stop time never match); ttl_expired.
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
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	if err := policy.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}


	if _, err := scheduler.ParseCondition(policy.Condition); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.repo.Create(&policy); err != nil {
		status, msg := mapError(err, entityCleanupPolicy)
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

// updateCleanupPolicyRequest is the body of PUT /admin/cleanup-policies/:id.
// Only the fields that are present change (partial update). id, created_at,
// updated_at and last_run_at are not writable.
type updateCleanupPolicyRequest struct {
	Name      *string `json:"name"`
	ClusterID *string `json:"cluster_id"`
	Action    *string `json:"action"`
	Condition *string `json:"condition"`
	Schedule  *string `json:"schedule"`
	Enabled   *bool   `json:"enabled"`
	DryRun    *bool   `json:"dry_run"`
}

// apply copies the fields that are present in the request to policy.
func (r *updateCleanupPolicyRequest) apply(policy *models.CleanupPolicy) {
	if r.Name != nil {
		policy.Name = *r.Name
	}
	if r.ClusterID != nil {
		policy.ClusterID = *r.ClusterID
	}
	if r.Action != nil {
		policy.Action = *r.Action
	}
	if r.Condition != nil {
		policy.Condition = *r.Condition
	}
	if r.Schedule != nil {
		policy.Schedule = *r.Schedule
	}
	if r.Enabled != nil {
		policy.Enabled = *r.Enabled
	}
	if r.DryRun != nil {
		policy.DryRun = *r.DryRun
	}
}

// UpdateCleanupPolicy godoc
// @Summary     Update a cleanup policy
// @Description Updates an existing cleanup policy and reloads the scheduler. Partial update: only the fields in the body change (for example {"enabled": false}); the merged policy is validated. See the create endpoint for the condition syntax (status, idle_days, age_days, stopped_days, ttl_expired).
// @Tags        cleanup-policies
// @Accept      json
// @Produce     json
// @Param       id     path     string                     true "Policy ID"
// @Param       policy body     updateCleanupPolicyRequest true "Fields to change"
// @Success     200    {object} models.CleanupPolicy
// @Failure     400    {object} map[string]string
// @Failure     401    {object} map[string]string
// @Failure     403    {object} map[string]string
// @Failure     404    {object} map[string]string
// @Failure     500    {object} map[string]string
// @Router      /api/v1/admin/cleanup-policies/{id} [put]
// @Security    BearerAuth
func (h *CleanupPolicyHandler) UpdateCleanupPolicy(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgPolicyIDRequired})
		return
	}

	existing, err := h.repo.FindByID(id)
	if err != nil {
		status, msg := mapError(err, entityCleanupPolicy)
		c.JSON(status, gin.H{"error": msg})
		return
	}

	var update updateCleanupPolicyRequest
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}
	// Merge into a copy, so a rejected update leaves the loaded policy as it
	// is.
	merged := *existing
	policy := &merged
	update.apply(policy)

	if err := policy.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, err := scheduler.ParseCondition(policy.Condition); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.repo.Update(policy); err != nil {
		status, msg := mapError(err, entityCleanupPolicy)
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
		c.JSON(http.StatusBadRequest, gin.H{"error": msgPolicyIDRequired})
		return
	}

	if err := h.repo.Delete(id); err != nil {
		status, msg := mapError(err, entityCleanupPolicy)
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
// @Description Executes a cleanup policy immediately and answers when the run ends. Use ?dry_run=true to preview matches without acting. Stop and clean only start the operations (fast). A delete runs the pre-instance-delete hooks per instance first, so a delete run can take up to the number of matches times (5 minutes, or the sum of the pre-instance-delete hook timeouts plus one minute when longer); use a client timeout that allows this. The run fires cleanup-policy-executed when at least one instance matched.
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
		c.JSON(http.StatusBadRequest, gin.H{"error": msgPolicyIDRequired})
		return
	}

	dryRun := c.Query("dry_run") == "true"

	if h.scheduler == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Scheduler not available"})
		return
	}

	results, err := h.scheduler.RunPolicy(id, dryRun)
	if err != nil {
		status, msg := mapError(err, entityCleanupPolicy)
		c.JSON(status, gin.H{"error": msg})
		return
	}

	if results == nil {
		results = []scheduler.CleanupResult{}
	}

	c.JSON(http.StatusOK, results)
}
