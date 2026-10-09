package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Branch override handler message constants.
const (
	entityBranchOverride       = "Branch override"
	msgFailedFindStackInstance = "failed to find stack instance"
)

// Slog structured logging key constants.
const (
	logKeyBOInstanceID = "instance_id"
	logKeyBOChartID    = "chart_id"
)



// BranchOverrideHandler handles per-chart branch override endpoints.
type BranchOverrideHandler struct {
	overrideRepo    models.ChartBranchOverrideRepository
	instanceRepo    models.StackInstanceRepository
	chartConfigRepo models.ChartConfigRepository
}

// NewBranchOverrideHandler creates a new BranchOverrideHandler. chartConfigRepo
// is used to check that :chartId is a chart of the instance's definition.
func NewBranchOverrideHandler(
	overrideRepo models.ChartBranchOverrideRepository,
	instanceRepo models.StackInstanceRepository,
	chartConfigRepo models.ChartConfigRepository,
) *BranchOverrideHandler {
	return &BranchOverrideHandler{
		overrideRepo:    overrideRepo,
		instanceRepo:    instanceRepo,
		chartConfigRepo: chartConfigRepo,
	}
}

// ListBranchOverrides godoc
// @Summary     List branch overrides for an instance
// @Description List all per-chart branch overrides for a stack instance
// @Tags        branch-overrides
// @Produce     json
// @Param       id  path     string true "Instance ID"
// @Success     200 {array}  models.ChartBranchOverride
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Security    BearerAuth
// @Router      /api/v1/stack-instances/{id}/branches [get]
func (h *BranchOverrideHandler) ListBranchOverrides(c *gin.Context) {
	instanceID := c.Param("id")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		if status == http.StatusInternalServerError {
			slog.Error(msgFailedFindStackInstance, logKeyBOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: owner, admin or devops (see canModifyInstance).
	if !requireInstanceModify(c, inst) {
		return
	}

	overrides, err := h.overrideRepo.List(instanceID)
	if err != nil {
		status, message := mapError(err, "Branch overrides")
		if status == http.StatusInternalServerError {
			slog.Error("failed to list branch overrides", logKeyBOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, overrides)
}

// GetBranchOverride godoc
// @Summary     Get the branch override for a chart
// @Description Get the per-chart branch override of one chart in a stack instance. The authorization rule is the same as for the list (owner, admin or devops). An existing override is returned also when its chart is no longer part of the definition. Without an override the response is 404: "Chart not found in this stack definition" for an unknown chart, otherwise "Branch override not found".
// @Tags        branch-overrides
// @Produce     json
// @Param       id      path     string true "Instance ID"
// @Param       chartId path     string true "Chart config ID"
// @Success     200     {object} models.ChartBranchOverride
// @Failure     400     {object} map[string]string
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Failure     404     {object} map[string]string "Instance not found, chart not found in this stack definition, or no override"
// @Failure     500     {object} map[string]string
// @Security    BearerAuth
// @Router      /api/v1/stack-instances/{id}/branches/{chartId} [get]
func (h *BranchOverrideHandler) GetBranchOverride(c *gin.Context) {
	instanceID := c.Param("id")
	chartID := c.Param("chartId")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		if status == http.StatusInternalServerError {
			slog.Error(msgFailedFindStackInstance, logKeyBOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: same rule as ListBranchOverrides (owner, admin or devops).
	if !requireInstanceModify(c, inst) {
		return
	}

	override, err := h.overrideRepo.Get(instanceID, chartID)
	if err == nil && override == nil {
		err = dberrors.NewDatabaseError("get", dberrors.ErrNotFound)
	}
	if err != nil {
		if isNotFoundError(err) {
			// No override: tell an unknown chart apart from a missing override.
			if _, ok := requireInstanceChart(c, h.chartConfigRepo, inst, chartID); !ok {
				return
			}
		}
		status, message := mapError(err, entityBranchOverride)
		if status == http.StatusInternalServerError {
			slog.Error("failed to get branch override", logKeyBOInstanceID, instanceID, logKeyBOChartID, chartID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, override)
}

// setBranchOverrideRequest is the request body for setting a branch override.
type setBranchOverrideRequest struct {
	Branch string `json:"branch" example:"feature/my-branch"`
}

// SetBranchOverride godoc
// @Summary     Set or update branch override for a chart
// @Description Upsert a per-chart branch override for a specific chart in a stack instance. chartId must be a chart config of the instance's stack definition (404 "Chart not found in this stack definition" otherwise).
// @Tags        branch-overrides
// @Accept      json
// @Produce     json
// @Param       id      path     string true "Instance ID"
// @Param       chartId path     string true "Chart config ID"
// @Param       body    body     setBranchOverrideRequest true "Branch override"
// @Success     200     {object} models.ChartBranchOverride
// @Failure     400     {object} map[string]string
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Failure     404     {object} map[string]string
// @Failure     500     {object} map[string]string
// @Security    BearerAuth
// @Router      /api/v1/stack-instances/{id}/branches/{chartId} [put]
func (h *BranchOverrideHandler) SetBranchOverride(c *gin.Context) {
	instanceID := c.Param("id")
	chartID := c.Param("chartId")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		if status == http.StatusInternalServerError {
			slog.Error(msgFailedFindStackInstance, logKeyBOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: owner, admin or devops (see canModifyInstance).
	if !requireInstanceModify(c, inst) {
		return
	}

	if _, ok := requireInstanceChart(c, h.chartConfigRepo, inst, chartID); !ok {
		return
	}

	var input setBranchOverrideRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
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
	if err != nil {
		if !errors.Is(err, dberrors.ErrNotFound) {
			slog.Error("failed to check existing branch override", logKeyBOInstanceID, instanceID, logKeyBOChartID, chartID, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}
		override.ID = uuid.New().String()
	} else {
		override.ID = existing.ID
	}

	if err := h.overrideRepo.Set(override); err != nil {
		status, message := mapError(err, entityBranchOverride)
		if status == http.StatusInternalServerError {
			slog.Error("failed to set branch override", logKeyBOInstanceID, instanceID, logKeyBOChartID, chartID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, override)
}

// DeleteBranchOverride godoc
// @Summary     Delete branch override for a chart
// @Description Remove the per-chart branch override for a specific chart in a stack instance. An existing override row is removed even when its chart is not part of the definition, so stale rows can be cleaned up. Without an override the response is 404: "Chart not found in this stack definition" for an unknown chart, otherwise "Branch override not found".
// @Tags        branch-overrides
// @Produce     json
// @Param       id      path     string true "Instance ID"
// @Param       chartId path     string true "Chart config ID"
// @Success     204     "No Content"
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Failure     404     {object} map[string]string
// @Failure     500     {object} map[string]string
// @Security    BearerAuth
// @Router      /api/v1/stack-instances/{id}/branches/{chartId} [delete]
func (h *BranchOverrideHandler) DeleteBranchOverride(c *gin.Context) {
	instanceID := c.Param("id")
	chartID := c.Param("chartId")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		if status == http.StatusInternalServerError {
			slog.Error(msgFailedFindStackInstance, logKeyBOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: owner, admin or devops (see canModifyInstance).
	if !requireInstanceModify(c, inst) {
		return
	}

	if err := h.overrideRepo.Delete(instanceID, chartID); err != nil {
		if isNotFoundError(err) {
			// No override: tell an unknown chart apart from a missing override.
			if _, ok := requireInstanceChart(c, h.chartConfigRepo, inst, chartID); !ok {
				return
			}
		}
		status, message := mapError(err, entityBranchOverride)
		if status == http.StatusInternalServerError {
			slog.Error("failed to delete branch override", logKeyBOInstanceID, instanceID, logKeyBOChartID, chartID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}
