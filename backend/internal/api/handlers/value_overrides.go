package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

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
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/overrides [get]
func (h *InstanceHandler) GetOverrides(c *gin.Context) {
	instanceID := c.Param("id")

	// Verify instance exists.
	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: overrides are part of the instance configuration, so the
	// read follows the modify rule (owner, admin or devops), like DeployPreview.
	if !requireInstanceModify(c, inst) {
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
// @Description Upsert value overrides for a specific chart in a stack instance. chartId must be a chart config of the instance's stack definition. Empty or whitespace-only values remove the override and return 204 No Content (also when no override existed).
// @Tags        value-overrides
// @Accept      json
// @Produce     json
// @Param       id      path     string              true "Instance ID"
// @Param       chartId path     string              true "Chart config ID"
// @Param       body    body     models.ValueOverride true "Override values"
// @Success     200     {object} models.ValueOverride
// @Success     204     "Override removed (empty values)"
// @Failure     400     {object} map[string]string
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Failure     404     {object} map[string]string "Instance not found, or chart not found in this stack definition"
// @Failure     500     {object} map[string]string
// @Security    BearerAuth
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

	// Authorization: only the owner, an admin or a devops user may set overrides.
	if !requireInstanceModify(c, inst) {
		return
	}

	if _, ok := requireInstanceChart(c, h.chartConfigRepo, inst, chartID); !ok {
		return
	}

	var input struct {
		Values string `json:"values"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	// Empty values mean "no override": remove the row instead of storing an
	// empty one, so GET /overrides lists only real overrides.
	if strings.TrimSpace(input.Values) == "" {
		if err := h.deleteValueOverride(instanceID, chartID); err != nil && !isNotFoundError(err) {
			status, message := mapError(err, entityValueOverride)
			if status == http.StatusInternalServerError {
				slog.Error("failed to remove empty value override", logKeyInstanceID, instanceID, "chart_id", chartID, "error", err)
			}
			c.JSON(status, gin.H{"error": message})
			return
		}
		c.Status(http.StatusNoContent)
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

// entityValueOverride is the entity name for value override errors.
const entityValueOverride = "Value override"

// msgChartNotInDefinition is the 404 message when :chartId is not a chart
// config of the instance's stack definition.
const msgChartNotInDefinition = "Chart not found in this stack definition"

// requireInstanceChart loads chartID and checks that it is a chart config of
// the instance's stack definition. It writes 404 ("Chart not found in this
// stack definition") when the chart does not exist or belongs to another
// definition, 500 on a lookup error, and returns false in both cases.
func requireInstanceChart(c *gin.Context, repo models.ChartConfigRepository, inst *models.StackInstance, chartID string) (*models.ChartConfig, bool) {
	if repo == nil || inst == nil {
		slog.Error("chart validation unavailable", "path", c.FullPath())
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return nil, false
	}
	chart, err := repo.FindByID(chartID)
	if err != nil {
		status, message := mapError(err, entityChartConfig)
		if status == http.StatusNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": msgChartNotInDefinition})
			return nil, false
		}
		if status == http.StatusInternalServerError {
			slog.Error("failed to find chart config", logKeyInstanceID, inst.ID, "chart_id", chartID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return nil, false
	}
	if chart == nil || chart.StackDefinitionID != inst.StackDefinitionID {
		c.JSON(http.StatusNotFound, gin.H{"error": msgChartNotInDefinition})
		return nil, false
	}
	return chart, true
}

// deleteValueOverride removes the value override of a chart. It returns a
// not-found error (see isNotFoundError) when no override exists.
func (h *InstanceHandler) deleteValueOverride(instanceID, chartID string) error {
	existing, err := h.overrideRepo.FindByInstanceAndChart(instanceID, chartID)
	if err != nil {
		return err
	}
	if existing == nil {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return h.overrideRepo.Delete(existing.ID)
}

// GetOverride godoc
// @Summary     Get the override for a chart
// @Description Get the value override of one chart in a stack instance. chartId must be a chart config of the instance's stack definition.
// @Tags        value-overrides
// @Produce     json
// @Param       id      path     string true "Instance ID"
// @Param       chartId path     string true "Chart config ID"
// @Success     200     {object} models.ValueOverride
// @Failure     400     {object} map[string]string
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Failure     404     {object} map[string]string "Instance not found, chart not found in this stack definition, or no override"
// @Failure     500     {object} map[string]string
// @Security    BearerAuth
// @Router      /api/v1/stack-instances/{id}/overrides/{chartId} [get]
func (h *InstanceHandler) GetOverride(c *gin.Context) {
	instanceID := c.Param("id")
	chartID := c.Param("chartId")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: same rule as GetOverrides (owner, admin or devops).
	if !requireInstanceModify(c, inst) {
		return
	}

	if _, ok := requireInstanceChart(c, h.chartConfigRepo, inst, chartID); !ok {
		return
	}

	override, err := h.overrideRepo.FindByInstanceAndChart(instanceID, chartID)
	if err != nil {
		status, message := mapError(err, entityValueOverride)
		if status == http.StatusInternalServerError {
			slog.Error("failed to get value override", logKeyInstanceID, instanceID, "chart_id", chartID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}
	if override == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": entityValueOverride + " not found"})
		return
	}

	c.JSON(http.StatusOK, override)
}

// DeleteOverride godoc
// @Summary     Delete the override for a chart
// @Description Remove the value override of one chart in a stack instance. An existing override row is removed even when its chart is no longer part of the definition, so stale rows can be cleaned up. Without an override the response is 404: "Chart not found in this stack definition" for an unknown chart, otherwise "Value override not found". The audit middleware records the delete.
// @Tags        value-overrides
// @Produce     json
// @Param       id      path     string true "Instance ID"
// @Param       chartId path     string true "Chart config ID"
// @Success     204     "No Content"
// @Failure     400     {object} map[string]string
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Failure     404     {object} map[string]string
// @Failure     500     {object} map[string]string
// @Security    BearerAuth
// @Router      /api/v1/stack-instances/{id}/overrides/{chartId} [delete]
func (h *InstanceHandler) DeleteOverride(c *gin.Context) {
	instanceID := c.Param("id")
	chartID := c.Param("chartId")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: same rule as SetOverride (owner, admin or devops).
	if !requireInstanceModify(c, inst) {
		return
	}

	err = h.deleteValueOverride(instanceID, chartID)
	if err == nil {
		c.Status(http.StatusNoContent)
		return
	}
	if isNotFoundError(err) {
		// No override: tell an unknown chart apart from a missing override.
		if _, ok := requireInstanceChart(c, h.chartConfigRepo, inst, chartID); !ok {
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": entityValueOverride + " not found"})
		return
	}
	status, message := mapError(err, entityValueOverride)
	if status == http.StatusInternalServerError {
		slog.Error("failed to delete value override", logKeyInstanceID, instanceID, "chart_id", chartID, "error", err)
	}
	c.JSON(status, gin.H{"error": message})
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
