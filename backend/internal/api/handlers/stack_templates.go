package handlers

import (
	"net/http"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// TemplateHandler handles stack template and template chart endpoints.
type TemplateHandler struct {
	templateRepo    models.StackTemplateRepository
	chartRepo       models.TemplateChartConfigRepository
	definitionRepo  models.StackDefinitionRepository
	chartConfigRepo models.ChartConfigRepository
}

// NewTemplateHandler creates a new TemplateHandler.
func NewTemplateHandler(
	templateRepo models.StackTemplateRepository,
	chartRepo models.TemplateChartConfigRepository,
	definitionRepo models.StackDefinitionRepository,
	chartConfigRepo models.ChartConfigRepository,
) *TemplateHandler {
	return &TemplateHandler{
		templateRepo:    templateRepo,
		chartRepo:       chartRepo,
		definitionRepo:  definitionRepo,
		chartConfigRepo: chartConfigRepo,
	}
}

// ListTemplates godoc
// @Summary     List stack templates
// @Description List published templates for regular users, all templates for devops/admin
// @Tags        templates
// @Produce     json
// @Success     200 {array}  models.StackTemplate
// @Failure     500 {object} map[string]string
// @Router      /api/v1/templates [get]
func (h *TemplateHandler) ListTemplates(c *gin.Context) {
	role := middleware.GetRoleFromContext(c)

	var templates []models.StackTemplate
	var err error
	if role == "admin" || role == "devops" {
		templates, err = h.templateRepo.List()
	} else {
		templates, err = h.templateRepo.ListPublished()
	}
	if err != nil {
		status, message := mapError(err, "Template")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, templates)
}

// CreateTemplate godoc
// @Summary     Create a stack template
// @Description Create a new stack template (devops/admin only)
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       template body     models.StackTemplate true "Template object"
// @Success     201      {object} models.StackTemplate
// @Failure     400      {object} map[string]string
// @Router      /api/v1/templates [post]
func (h *TemplateHandler) CreateTemplate(c *gin.Context) {
	var tmpl models.StackTemplate
	if err := c.ShouldBindJSON(&tmpl); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	tmpl.ID = uuid.New().String()
	tmpl.OwnerID = middleware.GetUserIDFromContext(c)
	now := time.Now().UTC()
	tmpl.CreatedAt = now
	tmpl.UpdatedAt = now

	if err := tmpl.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.templateRepo.Create(&tmpl); err != nil {
		status, message := mapError(err, "Template")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusCreated, tmpl)
}

// GetTemplate godoc
// @Summary     Get a stack template
// @Description Get a stack template by ID, including its chart configurations
// @Tags        templates
// @Produce     json
// @Param       id  path     string true "Template ID"
// @Success     200 {object} models.StackTemplate
// @Failure     404 {object} map[string]string
// @Router      /api/v1/templates/{id} [get]
func (h *TemplateHandler) GetTemplate(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Template ID is required"})
		return
	}

	tmpl, err := h.templateRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Template")
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartRepo.ListByTemplate(id)
	if err != nil {
		status, message := mapError(err, "Template charts")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"template": tmpl,
		"charts":   charts,
	})
}

// UpdateTemplate godoc
// @Summary     Update a stack template
// @Description Update an existing stack template (devops/admin only)
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       id       path     string               true "Template ID"
// @Param       template body     models.StackTemplate  true "Template object"
// @Success     200      {object} models.StackTemplate
// @Failure     400      {object} map[string]string
// @Failure     404      {object} map[string]string
// @Router      /api/v1/templates/{id} [put]
func (h *TemplateHandler) UpdateTemplate(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Template ID is required"})
		return
	}

	existing, err := h.templateRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Template")
		c.JSON(status, gin.H{"error": message})
		return
	}

	var update models.StackTemplate
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	existing.Name = update.Name
	existing.Description = update.Description
	existing.Category = update.Category
	existing.Version = update.Version
	existing.DefaultBranch = update.DefaultBranch
	existing.UpdatedAt = time.Now().UTC()

	if err := existing.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.templateRepo.Update(existing); err != nil {
		status, message := mapError(err, "Template")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, existing)
}

// DeleteTemplate godoc
// @Summary     Delete a stack template
// @Description Delete a stack template if no definitions link to it (devops/admin only)
// @Tags        templates
// @Produce     json
// @Param       id  path     string true "Template ID"
// @Success     204 "No Content"
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Router      /api/v1/templates/{id} [delete]
func (h *TemplateHandler) DeleteTemplate(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Template ID is required"})
		return
	}

	// Check that no definitions reference this template.
	if h.definitionRepo != nil {
		defs, err := h.definitionRepo.ListByTemplate(id)
		if err == nil && len(defs) > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "Cannot delete template: stack definitions reference it"})
			return
		}
	}

	if err := h.templateRepo.Delete(id); err != nil {
		status, message := mapError(err, "Template")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}

// PublishTemplate godoc
// @Summary     Publish a stack template
// @Description Make a template visible to all users (devops/admin only)
// @Tags        templates
// @Produce     json
// @Param       id  path     string true "Template ID"
// @Success     200 {object} models.StackTemplate
// @Failure     404 {object} map[string]string
// @Router      /api/v1/templates/{id}/publish [post]
func (h *TemplateHandler) PublishTemplate(c *gin.Context) {
	id := c.Param("id")
	tmpl, err := h.templateRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Template")
		c.JSON(status, gin.H{"error": message})
		return
	}

	tmpl.IsPublished = true
	tmpl.UpdatedAt = time.Now().UTC()

	if err := h.templateRepo.Update(tmpl); err != nil {
		status, message := mapError(err, "Template")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, tmpl)
}

// UnpublishTemplate godoc
// @Summary     Unpublish a stack template
// @Description Hide a template from regular users (devops/admin only)
// @Tags        templates
// @Produce     json
// @Param       id  path     string true "Template ID"
// @Success     200 {object} models.StackTemplate
// @Failure     404 {object} map[string]string
// @Router      /api/v1/templates/{id}/unpublish [post]
func (h *TemplateHandler) UnpublishTemplate(c *gin.Context) {
	id := c.Param("id")
	tmpl, err := h.templateRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Template")
		c.JSON(status, gin.H{"error": message})
		return
	}

	tmpl.IsPublished = false
	tmpl.UpdatedAt = time.Now().UTC()

	if err := h.templateRepo.Update(tmpl); err != nil {
		status, message := mapError(err, "Template")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, tmpl)
}

// InstantiateTemplate godoc
// @Summary     Instantiate a template
// @Description Create a StackDefinition and ChartConfigs from a template
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       id   path     string                true "Template ID"
// @Param       body body     models.StackDefinition true "Definition overrides (name, description)"
// @Success     201  {object} models.StackDefinition
// @Failure     400  {object} map[string]string
// @Failure     404  {object} map[string]string
// @Router      /api/v1/templates/{id}/instantiate [post]
func (h *TemplateHandler) InstantiateTemplate(c *gin.Context) {
	id := c.Param("id")
	tmpl, err := h.templateRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Template")
		c.JSON(status, gin.H{"error": message})
		return
	}

	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}
	if input.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Name is required"})
		return
	}

	now := time.Now().UTC()
	def := &models.StackDefinition{
		ID:                    uuid.New().String(),
		Name:                  input.Name,
		Description:           input.Description,
		OwnerID:               middleware.GetUserIDFromContext(c),
		SourceTemplateID:      tmpl.ID,
		SourceTemplateVersion: tmpl.Version,
		DefaultBranch:         tmpl.DefaultBranch,
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	if err := h.definitionRepo.Create(def); err != nil {
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Copy template charts into chart configs.
	templateCharts, err := h.chartRepo.ListByTemplate(tmpl.ID)
	if err != nil {
		status, message := mapError(err, "Template charts")
		c.JSON(status, gin.H{"error": message})
		return
	}

	var chartConfigs []models.ChartConfig
	for _, tc := range templateCharts {
		cc := models.ChartConfig{
			ID:                uuid.New().String(),
			StackDefinitionID: def.ID,
			ChartName:         tc.ChartName,
			RepositoryURL:     tc.RepositoryURL,
			SourceRepoURL:     tc.SourceRepoURL,
			ChartPath:         tc.ChartPath,
			ChartVersion:      tc.ChartVersion,
			DefaultValues:     tc.DefaultValues,
			DeployOrder:       tc.DeployOrder,
			CreatedAt:         now,
		}
		if err := h.chartConfigRepo.Create(&cc); err != nil {
			status, message := mapError(err, "Chart config")
			c.JSON(status, gin.H{"error": message})
			return
		}
		chartConfigs = append(chartConfigs, cc)
	}

	c.JSON(http.StatusCreated, gin.H{
		"definition": def,
		"charts":     chartConfigs,
	})
}

// CloneTemplate godoc
// @Summary     Clone a stack template
// @Description Create a new draft template that is a copy of the source (devops/admin only)
// @Tags        templates
// @Produce     json
// @Param       id  path     string true "Template ID"
// @Success     201 {object} models.StackTemplate
// @Failure     404 {object} map[string]string
// @Router      /api/v1/templates/{id}/clone [post]
func (h *TemplateHandler) CloneTemplate(c *gin.Context) {
	id := c.Param("id")
	source, err := h.templateRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Template")
		c.JSON(status, gin.H{"error": message})
		return
	}

	now := time.Now().UTC()
	clone := &models.StackTemplate{
		ID:            uuid.New().String(),
		Name:          source.Name + " (Copy)",
		Description:   source.Description,
		Category:      source.Category,
		Version:       source.Version,
		OwnerID:       middleware.GetUserIDFromContext(c),
		DefaultBranch: source.DefaultBranch,
		IsPublished:   false,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := h.templateRepo.Create(clone); err != nil {
		status, message := mapError(err, "Template")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Copy charts.
	charts, err := h.chartRepo.ListByTemplate(source.ID)
	if err != nil {
		status, message := mapError(err, "Template charts")
		c.JSON(status, gin.H{"error": message})
		return
	}

	for _, ch := range charts {
		chartClone := &models.TemplateChartConfig{
			ID:              uuid.New().String(),
			StackTemplateID: clone.ID,
			ChartName:       ch.ChartName,
			RepositoryURL:   ch.RepositoryURL,
			SourceRepoURL:   ch.SourceRepoURL,
			ChartPath:       ch.ChartPath,
			ChartVersion:    ch.ChartVersion,
			DefaultValues:   ch.DefaultValues,
			LockedValues:    ch.LockedValues,
			DeployOrder:     ch.DeployOrder,
			Required:        ch.Required,
			CreatedAt:       now,
		}
		if err := h.chartRepo.Create(chartClone); err != nil {
			status, message := mapError(err, "Template chart")
			c.JSON(status, gin.H{"error": message})
			return
		}
	}

	c.JSON(http.StatusCreated, clone)
}
