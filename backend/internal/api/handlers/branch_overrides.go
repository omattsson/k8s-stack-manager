package handlers

import (
	"net/http"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// BranchOverrideHandler handles per-chart branch override endpoints.
type BranchOverrideHandler struct {
	overrideRepo models.ChartBranchOverrideRepository
	instanceRepo models.StackInstanceRepository
}

// NewBranchOverrideHandler creates a new BranchOverrideHandler.
func NewBranchOverrideHandler(
	overrideRepo models.ChartBranchOverrideRepository,
	instanceRepo models.StackInstanceRepository,
) *BranchOverrideHandler {
	return &BranchOverrideHandler{
		overrideRepo: overrideRepo,
		instanceRepo: instanceRepo,
	}
}

// ListBranchOverrides godoc
// @Summary     List branch overrides for an instance
// @Description List all per-chart branch overrides for a stack instance
// @Tags        branch-overrides
// @Produce     json
// @Param       id  path     string true "Instance ID"
// @Success     200 {array}  models.ChartBranchOverride
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/branches [get]
func (h *BranchOverrideHandler) ListBranchOverrides(c *gin.Context) {
	instanceID := c.Param("id")

	if _, err := h.instanceRepo.FindByID(instanceID); err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	overrides, err := h.overrideRepo.List(instanceID)
	if err != nil {
		status, message := mapError(err, "Branch overrides")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, overrides)
}

// SetBranchOverride godoc
// @Summary     Set or update branch override for a chart
// @Description Upsert a per-chart branch override for a specific chart in a stack instance
// @Tags        branch-overrides
// @Accept      json
// @Produce     json
// @Param       id      path     string true "Instance ID"
// @Param       chartId path     string true "Chart config ID"
// @Param       body    body     object true "Branch override (branch field required)"
// @Success     200     {object} models.ChartBranchOverride
// @Failure     400     {object} map[string]string
// @Failure     404     {object} map[string]string
// @Failure     500     {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/branches/{chartId} [put]
func (h *BranchOverrideHandler) SetBranchOverride(c *gin.Context) {
	instanceID := c.Param("id")
	chartID := c.Param("chartId")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	userID := middleware.GetUserIDFromContext(c)
	role := middleware.GetRoleFromContext(c)
	if inst.OwnerID != userID && role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "You do not have permission to modify this instance"})
		return
	}

	var input struct {
		Branch string `json:"branch"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}
	if input.Branch == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Branch is required"})
		return
	}

	override := &models.ChartBranchOverride{
		StackInstanceID: instanceID,
		ChartConfigID:   chartID,
		Branch:          input.Branch,
		UpdatedAt:       time.Now().UTC(),
	}

	// Check if one already exists to preserve the ID.
	existing, err := h.overrideRepo.Get(instanceID, chartID)
	if err == nil && existing != nil {
		override.ID = existing.ID
	} else {
		override.ID = uuid.New().String()
	}

	if err := h.overrideRepo.Set(override); err != nil {
		status, message := mapError(err, "Branch override")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, override)
}

// DeleteBranchOverride godoc
// @Summary     Delete branch override for a chart
// @Description Remove the per-chart branch override for a specific chart in a stack instance
// @Tags        branch-overrides
// @Produce     json
// @Param       id      path     string true "Instance ID"
// @Param       chartId path     string true "Chart config ID"
// @Success     204     "No Content"
// @Failure     404     {object} map[string]string
// @Failure     500     {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/branches/{chartId} [delete]
func (h *BranchOverrideHandler) DeleteBranchOverride(c *gin.Context) {
	instanceID := c.Param("id")
	chartID := c.Param("chartId")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	userID := middleware.GetUserIDFromContext(c)
	role := middleware.GetRoleFromContext(c)
	if inst.OwnerID != userID && role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "You do not have permission to modify this instance"})
		return
	}

	if err := h.overrideRepo.Delete(instanceID, chartID); err != nil {
		status, message := mapError(err, "Branch override")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}
