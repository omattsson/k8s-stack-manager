package handlers

import (
	"net/http"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// GetOverrides godoc
// @Summary     Get overrides for an instance
// @Description List all value overrides for a stack instance
// @Tags        value-overrides
// @Produce     json
// @Param       id  path     string true "Instance ID"
// @Success     200 {array}  models.ValueOverride
// @Failure     404 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/overrides [get]
func (h *InstanceHandler) GetOverrides(c *gin.Context) {
	instanceID := c.Param("id")

	// Verify instance exists.
	if _, err := h.instanceRepo.FindByID(instanceID); err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	overrides, err := h.overrideRepo.ListByInstance(instanceID)
	if err != nil {
		status, message := mapError(err, "Value overrides")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, overrides)
}

// SetOverride godoc
// @Summary     Set or update override for a chart
// @Description Upsert value overrides for a specific chart in a stack instance
// @Tags        value-overrides
// @Accept      json
// @Produce     json
// @Param       id      path     string              true "Instance ID"
// @Param       chartId path     string              true "Chart config ID"
// @Param       body    body     models.ValueOverride true "Override values"
// @Success     200     {object} models.ValueOverride
// @Failure     400     {object} map[string]string
// @Failure     404     {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/overrides/{chartId} [put]
func (h *InstanceHandler) SetOverride(c *gin.Context) {
	instanceID := c.Param("id")
	chartID := c.Param("chartId")

	// Verify instance exists.
	if _, err := h.instanceRepo.FindByID(instanceID); err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	var input struct {
		Values string `json:"values"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	now := time.Now().UTC()

	// Try to find existing override for upsert.
	existing, err := h.overrideRepo.FindByInstanceAndChart(instanceID, chartID)
	if err == nil && existing != nil {
		existing.Values = input.Values
		existing.UpdatedAt = now

		if err := h.overrideRepo.Update(existing); err != nil {
			status, message := mapError(err, "Value override")
			c.JSON(status, gin.H{"error": message})
			return
		}

		c.JSON(http.StatusOK, existing)
		return
	}

	// Create new override.
	override := &models.ValueOverride{
		ID:              uuid.New().String(),
		StackInstanceID: instanceID,
		ChartConfigID:   chartID,
		Values:          input.Values,
		UpdatedAt:       now,
	}

	if err := h.overrideRepo.Create(override); err != nil {
		status, message := mapError(err, "Value override")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, override)
}
