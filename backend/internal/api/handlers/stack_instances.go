package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// InstanceHandler handles stack instance, value override, and values export endpoints.
type InstanceHandler struct {
	instanceRepo      models.StackInstanceRepository
	overrideRepo      models.ValueOverrideRepository
	definitionRepo    models.StackDefinitionRepository
	chartConfigRepo   models.ChartConfigRepository
	templateRepo      models.StackTemplateRepository
	templateChartRepo models.TemplateChartConfigRepository
	valuesGen         *helm.ValuesGenerator
	userRepo          models.UserRepository
}

// NewInstanceHandler creates a new InstanceHandler.
func NewInstanceHandler(
	instanceRepo models.StackInstanceRepository,
	overrideRepo models.ValueOverrideRepository,
	definitionRepo models.StackDefinitionRepository,
	chartConfigRepo models.ChartConfigRepository,
	templateRepo models.StackTemplateRepository,
	templateChartRepo models.TemplateChartConfigRepository,
	valuesGen *helm.ValuesGenerator,
	userRepo models.UserRepository,
) *InstanceHandler {
	return &InstanceHandler{
		instanceRepo:      instanceRepo,
		overrideRepo:      overrideRepo,
		definitionRepo:    definitionRepo,
		chartConfigRepo:   chartConfigRepo,
		templateRepo:      templateRepo,
		templateChartRepo: templateChartRepo,
		valuesGen:         valuesGen,
		userRepo:          userRepo,
	}
}

// ListInstances godoc
// @Summary     List stack instances
// @Description List all stack instances, optionally filtered by owner
// @Tags        stack-instances
// @Produce     json
// @Param       owner query    string false "Filter by owner (use 'me' for current user)"
// @Success     200   {array}  models.StackInstance
// @Failure     500   {object} map[string]string
// @Router      /api/v1/stack-instances [get]
func (h *InstanceHandler) ListInstances(c *gin.Context) {
	owner := c.Query("owner")

	var instances []models.StackInstance
	var err error
	if owner == "me" {
		userID := middleware.GetUserIDFromContext(c)
		instances, err = h.instanceRepo.ListByOwner(userID)
	} else {
		instances, err = h.instanceRepo.List()
	}
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, instances)
}

// CreateInstance godoc
// @Summary     Create a stack instance
// @Description Create a new stack instance from a definition
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       instance body     models.StackInstance true "Instance object"
// @Success     201      {object} models.StackInstance
// @Failure     400      {object} map[string]string
// @Router      /api/v1/stack-instances [post]
func (h *InstanceHandler) CreateInstance(c *gin.Context) {
	var inst models.StackInstance
	if err := c.ShouldBindJSON(&inst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	inst.ID = uuid.New().String()
	inst.OwnerID = middleware.GetUserIDFromContext(c)
	inst.Status = models.StackStatusDraft
	now := time.Now().UTC()
	inst.CreatedAt = now
	inst.UpdatedAt = now

	if inst.Branch == "" {
		inst.Branch = "master"
	}

	// Auto-generate namespace.
	if inst.Namespace == "" {
		owner := middleware.GetUsernameFromContext(c)
		safeName := strings.ToLower(strings.ReplaceAll(inst.Name, " ", "-"))
		inst.Namespace = fmt.Sprintf("stack-%s-%s", safeName, owner)
	}

	if err := inst.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Verify definition exists.
	if _, err := h.definitionRepo.FindByID(inst.StackDefinitionID); err != nil {
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if err := h.instanceRepo.Create(&inst); err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusCreated, inst)
}

// GetInstance godoc
// @Summary     Get a stack instance
// @Description Get a stack instance by ID with its overrides
// @Tags        stack-instances
// @Produce     json
// @Param       id  path     string true "Instance ID"
// @Success     200 {object} models.StackInstance
// @Failure     404 {object} map[string]string
// @Router      /api/v1/stack-instances/{id} [get]
func (h *InstanceHandler) GetInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance ID is required"})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	overrides, err := h.overrideRepo.ListByInstance(id)
	if err != nil {
		status, message := mapError(err, "Value overrides")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"instance":  inst,
		"overrides": overrides,
	})
}

// UpdateInstance godoc
// @Summary     Update a stack instance
// @Description Update a stack instance (branch, name, etc.)
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id       path     string               true "Instance ID"
// @Param       instance body     models.StackInstance   true "Instance object"
// @Success     200      {object} models.StackInstance
// @Failure     400      {object} map[string]string
// @Failure     404      {object} map[string]string
// @Router      /api/v1/stack-instances/{id} [put]
func (h *InstanceHandler) UpdateInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance ID is required"})
		return
	}

	existing, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	var update models.StackInstance
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	existing.Name = update.Name
	existing.Branch = update.Branch
	existing.Namespace = update.Namespace
	existing.UpdatedAt = time.Now().UTC()

	if err := existing.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.instanceRepo.Update(existing); err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, existing)
}

// DeleteInstance godoc
// @Summary     Delete a stack instance
// @Description Delete a stack instance
// @Tags        stack-instances
// @Produce     json
// @Param       id  path     string true "Instance ID"
// @Success     204 "No Content"
// @Failure     404 {object} map[string]string
// @Router      /api/v1/stack-instances/{id} [delete]
func (h *InstanceHandler) DeleteInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance ID is required"})
		return
	}

	if err := h.instanceRepo.Delete(id); err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}

// CloneInstance godoc
// @Summary     Clone a stack instance
// @Description Create a new stack instance as a copy of an existing one
// @Tags        stack-instances
// @Produce     json
// @Param       id  path     string true "Instance ID"
// @Success     201 {object} models.StackInstance
// @Failure     404 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/clone [post]
func (h *InstanceHandler) CloneInstance(c *gin.Context) {
	id := c.Param("id")
	source, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	now := time.Now().UTC()
	ownerID := middleware.GetUserIDFromContext(c)
	ownerName := middleware.GetUsernameFromContext(c)
	safeName := strings.ToLower(strings.ReplaceAll(source.Name, " ", "-"))

	clone := &models.StackInstance{
		ID:                uuid.New().String(),
		StackDefinitionID: source.StackDefinitionID,
		Name:              source.Name + " (Copy)",
		Namespace:         fmt.Sprintf("stack-%s-copy-%s", safeName, ownerName),
		OwnerID:           ownerID,
		Branch:            source.Branch,
		Status:            models.StackStatusDraft,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := h.instanceRepo.Create(clone); err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Copy value overrides.
	overrides, err := h.overrideRepo.ListByInstance(source.ID)
	if err == nil {
		for _, ov := range overrides {
			clonedOV := &models.ValueOverride{
				ID:              uuid.New().String(),
				StackInstanceID: clone.ID,
				ChartConfigID:   ov.ChartConfigID,
				Values:          ov.Values,
				UpdatedAt:       now,
			}
			// Best-effort — don't fail the clone if override copy fails.
			_ = h.overrideRepo.Create(clonedOV)
		}
	}

	c.JSON(http.StatusCreated, clone)
}

// ExportChartValues godoc
// @Summary     Export chart values
// @Description Generate and export merged values.yaml for a specific chart
// @Tags        stack-instances
// @Produce     application/x-yaml
// @Param       id      path     string true "Instance ID"
// @Param       chartId path     string true "Chart config ID"
// @Success     200     {string} string "YAML content"
// @Failure     404     {object} map[string]string
// @Failure     500     {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/values/{chartId} [get]
func (h *InstanceHandler) ExportChartValues(c *gin.Context) {
	instanceID := c.Param("id")
	chartID := c.Param("chartId")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	chart, err := h.chartConfigRepo.FindByID(chartID)
	if err != nil {
		status, message := mapError(err, "Chart config")
		c.JSON(status, gin.H{"error": message})
		return
	}

	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Get locked values from the source template, if any.
	var lockedValues string
	if def.SourceTemplateID != "" && h.templateChartRepo != nil {
		templateCharts, err := h.templateChartRepo.ListByTemplate(def.SourceTemplateID)
		if err == nil {
			for _, tc := range templateCharts {
				if tc.ChartName == chart.ChartName {
					lockedValues = tc.LockedValues
					break
				}
			}
		}
	}

	// Get value overrides.
	var overrideValues string
	override, err := h.overrideRepo.FindByInstanceAndChart(instanceID, chartID)
	if err == nil && override != nil {
		overrideValues = override.Values
	}

	// Resolve owner username for template vars.
	ownerName := resolveOwnerName(h.userRepo, inst.OwnerID)

	params := helm.GenerateParams{
		ChartName:      chart.ChartName,
		DefaultValues:  chart.DefaultValues,
		LockedValues:   lockedValues,
		OverrideValues: overrideValues,
		TemplateVars: helm.TemplateVars{
			Branch:       inst.Branch,
			Namespace:    inst.Namespace,
			InstanceName: inst.Name,
			StackName:    def.Name,
			Owner:        ownerName,
		},
	}

	yamlData, err := h.valuesGen.GenerateValues(c.Request.Context(), params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.Data(http.StatusOK, "application/x-yaml", yamlData)
}

// ExportAllValues godoc
// @Summary     Export all chart values
// @Description Generate and export merged values for all charts as a zip archive
// @Tags        stack-instances
// @Produce     application/zip
// @Param       id  path     string true "Instance ID"
// @Success     200 {file}   file   "ZIP archive"
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/values [get]
func (h *InstanceHandler) ExportAllValues(c *gin.Context) {
	instanceID := c.Param("id")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		status, message := mapError(err, "Chart configs")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Build locked values map from template.
	lockedMap := make(map[string]string) // chartName → lockedValues
	if def.SourceTemplateID != "" && h.templateChartRepo != nil {
		templateCharts, err := h.templateChartRepo.ListByTemplate(def.SourceTemplateID)
		if err == nil {
			for _, tc := range templateCharts {
				lockedMap[tc.ChartName] = tc.LockedValues
			}
		}
	}

	// Build overrides map.
	overridesMap := make(map[string]string) // chartConfigID → values
	overrides, err := h.overrideRepo.ListByInstance(instanceID)
	if err == nil {
		for _, ov := range overrides {
			overridesMap[ov.ChartConfigID] = ov.Values
		}
	}

	ownerName := resolveOwnerName(h.userRepo, inst.OwnerID)

	var chartValues []helm.ChartValues
	for _, ch := range charts {
		chartValues = append(chartValues, helm.ChartValues{
			ChartName:      ch.ChartName,
			DefaultValues:  ch.DefaultValues,
			LockedValues:   lockedMap[ch.ChartName],
			OverrideValues: overridesMap[ch.ID],
		})
	}

	params := helm.GenerateAllParams{
		Charts: chartValues,
		TemplateVars: helm.TemplateVars{
			Branch:       inst.Branch,
			Namespace:    inst.Namespace,
			InstanceName: inst.Name,
			StackName:    def.Name,
			Owner:        ownerName,
		},
	}

	allValues, err := h.valuesGen.ExportAsZip(c.Request.Context(), params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s-values.zip", inst.Name))
	c.Data(http.StatusOK, "application/zip", allValues)
}

func resolveOwnerName(userRepo models.UserRepository, ownerID string) string {
	if userRepo == nil {
		return ownerID
	}
	user, err := userRepo.FindByID(ownerID)
	if err != nil {
		return ownerID
	}
	return user.Username
}
