package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/database"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Stack template handler message constants.
const (
	msgTemplateIDRequired = "Template ID is required"
	entityTemplateCharts  = "Template charts"
)


// TemplateHandler handles stack template and template chart endpoints.
type TemplateHandler struct {
	templateRepo    models.StackTemplateRepository
	chartRepo       models.TemplateChartConfigRepository
	definitionRepo  models.StackDefinitionRepository
	chartConfigRepo models.ChartConfigRepository
	versionRepo     models.TemplateVersionRepository
	userRepo        models.UserRepository
	txRunner        database.TxRunner
}

// SetUserRepo sets the optional UserRepository for enriched list responses.
func (h *TemplateHandler) SetUserRepo(repo models.UserRepository) {
	h.userRepo = repo
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

// NewTemplateHandlerWithVersions creates a TemplateHandler with template version support.
func NewTemplateHandlerWithVersions(
	templateRepo models.StackTemplateRepository,
	chartRepo models.TemplateChartConfigRepository,
	definitionRepo models.StackDefinitionRepository,
	chartConfigRepo models.ChartConfigRepository,
	versionRepo models.TemplateVersionRepository,
) *TemplateHandler {
	return &TemplateHandler{
		templateRepo:    templateRepo,
		chartRepo:       chartRepo,
		definitionRepo:  definitionRepo,
		chartConfigRepo: chartConfigRepo,
		versionRepo:     versionRepo,
	}
}

// SetTxRunner sets an optional TxRunner for transactional multi-entity operations.
func (h *TemplateHandler) SetTxRunner(tx database.TxRunner) {
	h.txRunner = tx
}

// TemplateListItem extends StackTemplate with computed fields for the gallery.
type TemplateListItem struct {
	models.StackTemplate
	DefinitionCount int    `json:"definition_count"`
	OwnerUsername   string `json:"owner_username,omitempty"`
}

// ListTemplates godoc
// @Summary     List stack templates
// @Description List published templates for regular users, all templates for devops/admin. Includes definition_count and owner_username.
// @Tags        templates
// @Accept      json
// @Produce     json
// @Success     200 {array}  TemplateListItem
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
		status, message := mapError(err, entityTemplate)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// NOTE: This does N+1 queries (per-template definition count + per-owner username lookup).
	// Acceptable for typical gallery sizes. Ideal optimization: batch CountByTemplateIDs(ids)
	// and FindUsersByIDs(ids) methods to reduce to 2 queries.

	// Build definition count map by querying only for returned template IDs.
	defCountMap := make(map[string]int)
	if h.definitionRepo != nil {
		for _, t := range templates {
			defs, err := h.definitionRepo.ListByTemplate(t.ID)
			if err != nil {
				slog.Warn("failed to fetch definitions for template", "template_id", t.ID, "error", err)
				continue
			}
			defCountMap[t.ID] = len(defs)
		}
	}

	// Build username map from distinct owner IDs (instead of loading all users).
	usernameMap := make(map[string]string)
	if h.userRepo != nil {
		ownerIDs := make(map[string]struct{})
		for _, t := range templates {
			if t.OwnerID != "" {
				ownerIDs[t.OwnerID] = struct{}{}
			}
		}
		for ownerID := range ownerIDs {
			user, err := h.userRepo.FindByID(ownerID)
			if err != nil {
				slog.Warn("failed to fetch user for template listing", "owner_id", ownerID, "error", err)
				continue
			}
			usernameMap[ownerID] = user.Username
		}
	}

	// Assemble enriched response.
	items := make([]TemplateListItem, len(templates))
	for i, t := range templates {
		items[i] = TemplateListItem{
			StackTemplate:   t,
			DefinitionCount: defCountMap[t.ID],
			OwnerUsername:   usernameMap[t.OwnerID],
		}
	}

	c.JSON(http.StatusOK, items)
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
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
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
		status, message := mapError(err, entityTemplate)
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
		c.JSON(http.StatusBadRequest, gin.H{"error": msgTemplateIDRequired})
		return
	}

	tmpl, err := h.templateRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityTemplate)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartRepo.ListByTemplate(id)
	if err != nil {
		status, message := mapError(err, entityTemplateCharts)
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
		c.JSON(http.StatusBadRequest, gin.H{"error": msgTemplateIDRequired})
		return
	}

	existing, err := h.templateRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityTemplate)
		c.JSON(status, gin.H{"error": message})
		return
	}

	var update models.StackTemplate
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
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
		status, message := mapError(err, entityTemplate)
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
		c.JSON(http.StatusBadRequest, gin.H{"error": msgTemplateIDRequired})
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
		status, message := mapError(err, entityTemplate)
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
		status, message := mapError(err, entityTemplate)
		c.JSON(status, gin.H{"error": message})
		return
	}

	tmpl.IsPublished = true
	tmpl.UpdatedAt = time.Now().UTC()

	if err := h.templateRepo.Update(tmpl); err != nil {
		status, message := mapError(err, entityTemplate)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Auto-create a version snapshot on publish.
	if h.versionRepo != nil {
		h.createVersionSnapshot(c, tmpl)
	}

	c.JSON(http.StatusOK, tmpl)
}

// createVersionSnapshot builds a TemplateSnapshot from the current template and
// its charts, then persists it as a TemplateVersion record. Errors are logged
// but do not fail the publish operation.
func (h *TemplateHandler) createVersionSnapshot(c *gin.Context, tmpl *models.StackTemplate) {
	charts, err := h.chartRepo.ListByTemplate(tmpl.ID)
	if err != nil {
		slog.Error("failed to fetch charts for version snapshot", "template_id", tmpl.ID, "error", err)
		return
	}

	chartSnapshots := make([]models.TemplateChartSnapshotData, 0, len(charts))
	for _, ch := range charts {
		chartSnapshots = append(chartSnapshots, models.TemplateChartSnapshotData{
			ChartName:     ch.ChartName,
			RepoURL:       ch.RepositoryURL,
			DefaultValues: ch.DefaultValues,
			LockedValues:  ch.LockedValues,
			IsRequired:    ch.Required,
			SortOrder:     ch.DeployOrder,
		})
	}

	snapshot := models.TemplateSnapshot{
		Template: models.TemplateSnapshotData{
			Name:          tmpl.Name,
			Description:   tmpl.Description,
			Category:      tmpl.Category,
			DefaultBranch: tmpl.DefaultBranch,
			IsPublished:   tmpl.IsPublished,
			Version:       tmpl.Version,
		},
		Charts: chartSnapshots,
	}

	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		slog.Error("failed to marshal version snapshot", "template_id", tmpl.ID, "error", err)
		return
	}

	version := &models.TemplateVersion{
		ID:         uuid.New().String(),
		TemplateID: tmpl.ID,
		Version:    tmpl.Version,
		Snapshot:   string(snapshotBytes),
		CreatedBy:  middleware.GetUserIDFromContext(c),
		CreatedAt:  time.Now().UTC(),
	}

	if err := h.versionRepo.Create(c.Request.Context(), version); err != nil {
		slog.Error("failed to create version snapshot", "template_id", tmpl.ID, "error", err)
	}
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
		status, message := mapError(err, entityTemplate)
		c.JSON(status, gin.H{"error": message})
		return
	}

	tmpl.IsPublished = false
	tmpl.UpdatedAt = time.Now().UTC()

	if err := h.templateRepo.Update(tmpl); err != nil {
		status, message := mapError(err, entityTemplate)
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
		status, message := mapError(err, entityTemplate)
		c.JSON(status, gin.H{"error": message})
		return
	}

	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
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

	if h.txRunner != nil && h.txRunner.IsTransactional() {
		// Transactional path — definition + chart configs are created atomically.
		var chartConfigs []models.ChartConfig
		txErr := h.txRunner.RunInTx(func(repos database.TxRepos) error {
			if err := repos.StackDefinition.Create(def); err != nil {
				return err
			}
			ccs, copyErr := h.copyTemplateChartsToDefinitionTx(tmpl.ID, def.ID, now, repos)
			if copyErr != nil {
				return copyErr
			}
			chartConfigs = ccs
			return nil
		})
		if txErr != nil {
			status, message := mapError(txErr, entityStackDefinition)
			c.JSON(status, gin.H{"error": message})
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"definition": def,
			"charts":     chartConfigs,
		})
		return
	}

	// Non-transactional fallback (Azure Table Storage).
	if err := h.definitionRepo.Create(def); err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	chartConfigs, err := h.copyTemplateChartsToDefinition(tmpl.ID, def.ID, now)
	if err != nil {
		status, message := mapError(err, entityChartConfig)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"definition": def,
		"charts":     chartConfigs,
	})
}

// copyTemplateChartsToDefinition copies all template chart configs into chart configs
// for the given definition and returns the created configs.
func (h *TemplateHandler) copyTemplateChartsToDefinition(templateID, defID string, now time.Time) ([]models.ChartConfig, error) {
	templateCharts, err := h.chartRepo.ListByTemplate(templateID)
	if err != nil {
		return nil, err
	}

	chartConfigs := make([]models.ChartConfig, 0, len(templateCharts))
	for _, tc := range templateCharts {
		cc := models.ChartConfig{
			ID:                uuid.New().String(),
			StackDefinitionID: defID,
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
			return nil, err
		}
		chartConfigs = append(chartConfigs, cc)
	}

	return chartConfigs, nil
}

// copyTemplateChartsToDefinitionTx is the transactional variant of
// copyTemplateChartsToDefinition. It reads template charts via the handler's
// chartRepo (read-only) and creates chart configs via the transactional repos.
func (h *TemplateHandler) copyTemplateChartsToDefinitionTx(templateID, defID string, now time.Time, repos database.TxRepos) ([]models.ChartConfig, error) {
	templateCharts, err := h.chartRepo.ListByTemplate(templateID)
	if err != nil {
		return nil, err
	}

	chartConfigs := make([]models.ChartConfig, 0, len(templateCharts))
	for _, tc := range templateCharts {
		cc := models.ChartConfig{
			ID:                uuid.New().String(),
			StackDefinitionID: defID,
			ChartName:         tc.ChartName,
			RepositoryURL:     tc.RepositoryURL,
			SourceRepoURL:     tc.SourceRepoURL,
			ChartPath:         tc.ChartPath,
			ChartVersion:      tc.ChartVersion,
			DefaultValues:     tc.DefaultValues,
			DeployOrder:       tc.DeployOrder,
			CreatedAt:         now,
		}
		if err := repos.ChartConfig.Create(&cc); err != nil {
			return nil, err
		}
		chartConfigs = append(chartConfigs, cc)
	}

	return chartConfigs, nil
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
		status, message := mapError(err, entityTemplate)
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

	// Fetch source charts before any writes so we fail early on read errors.
	charts, err := h.chartRepo.ListByTemplate(source.ID)
	if err != nil {
		status, message := mapError(err, entityTemplateCharts)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Build chart clone models.
	chartClones := make([]*models.TemplateChartConfig, 0, len(charts))
	for _, ch := range charts {
		chartClones = append(chartClones, &models.TemplateChartConfig{
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
		})
	}

	if h.txRunner != nil && h.txRunner.IsTransactional() {
		// Transactional path — template + chart copies are atomic.
		txErr := h.txRunner.RunInTx(func(repos database.TxRepos) error {
			if err := repos.StackTemplate.Create(clone); err != nil {
				return err
			}
			for _, cc := range chartClones {
				if err := repos.TemplateChart.Create(cc); err != nil {
					return err
				}
			}
			return nil
		})
		if txErr != nil {
			status, message := mapError(txErr, entityTemplate)
			c.JSON(status, gin.H{"error": message})
			return
		}
	} else {
		// Non-transactional fallback (Azure Table Storage).
		if err := h.templateRepo.Create(clone); err != nil {
			status, message := mapError(err, entityTemplate)
			c.JSON(status, gin.H{"error": message})
			return
		}

		for _, cc := range chartClones {
			if err := h.chartRepo.Create(cc); err != nil {
				status, message := mapError(err, "Template chart")
				c.JSON(status, gin.H{"error": message})
				return
			}
		}
	}

	c.JSON(http.StatusCreated, clone)
}
