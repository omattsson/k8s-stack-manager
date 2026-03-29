package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
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
		status, message := mapError(err, entityStackInstance)
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
	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	var input struct {
		Values string `json:"values"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	// Check for locked values from the source template.
	if err := h.checkLockedValues(inst, chartID, input.Values); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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

// checkLockedValues verifies that the submitted override values do not conflict
// with locked values from the source template. Returns an error if conflicts exist.
func (h *InstanceHandler) checkLockedValues(inst *models.StackInstance, chartID, overrideYAML string) error {
	if overrideYAML == "" {
		return nil
	}

	// Look up the definition to check for a source template.
	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil || def.SourceTemplateID == "" {
		return nil
	}

	// Look up the chart config to get its ChartName.
	chart, err := h.chartConfigRepo.FindByID(chartID)
	if err != nil {
		return nil
	}

	// Find the matching template chart config by ChartName.
	templateCharts, err := h.templateChartRepo.ListByTemplate(def.SourceTemplateID)
	if err != nil {
		return nil
	}

	var lockedYAML string
	for _, tc := range templateCharts {
		if tc.ChartName == chart.ChartName {
			lockedYAML = tc.LockedValues
			break
		}
	}

	if lockedYAML == "" {
		return nil
	}

	// Parse both YAML strings into maps and check for top-level key conflicts.
	var lockedMap map[string]interface{}
	if err := yaml.Unmarshal([]byte(lockedYAML), &lockedMap); err != nil {
		return nil // If locked values can't be parsed, skip the check.
	}

	var overrideMap map[string]interface{}
	if err := yaml.Unmarshal([]byte(overrideYAML), &overrideMap); err != nil {
		return nil // If override values can't be parsed, skip the check.
	}

	var conflicts []string
	for key := range overrideMap {
		if _, exists := lockedMap[key]; exists {
			conflicts = append(conflicts, key)
		}
	}

	if len(conflicts) > 0 {
		return fmt.Errorf("Cannot override locked values: %s", strings.Join(conflicts, ", "))
	}

	return nil
}
