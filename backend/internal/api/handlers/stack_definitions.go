package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/database"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Stack definition handler message constants.
const (
	msgDefinitionIDRequired = "Definition ID is required"
)

// supportedSchemaVersions lists the schema versions that the import endpoint can accept.
var supportedSchemaVersions = map[string]bool{
	"1.0": true,
}

// DefinitionExportBundle is the portable JSON format for exporting/importing stack definitions.
type DefinitionExportBundle struct {
	SchemaVersion string                  `json:"schema_version"`
	ExportedAt    time.Time               `json:"exported_at"`
	Definition    DefinitionExportData    `json:"definition"`
	Charts        []ChartConfigExportData `json:"charts"`
}

// DefinitionExportData holds the exportable fields of a stack definition.
type DefinitionExportData struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
}

// ChartConfigExportData holds the exportable fields of a chart configuration.
type ChartConfigExportData struct {
	ChartName     string `json:"chart_name"`
	RepositoryURL string `json:"repository_url"`
	SourceRepoURL string `json:"source_repo_url"`
	ChartPath     string `json:"chart_path"`
	ChartVersion  string `json:"chart_version"`
	DefaultValues string `json:"default_values"`
	DeployOrder   int    `json:"deploy_order"`
}

// DefinitionHandler handles stack definition and chart config endpoints.
type DefinitionHandler struct {
	definitionRepo    models.StackDefinitionRepository
	chartRepo         models.ChartConfigRepository
	instanceRepo      models.StackInstanceRepository
	templateRepo      models.StackTemplateRepository
	templateChartRepo models.TemplateChartConfigRepository
	versionRepo       models.TemplateVersionRepository
	txRunner          database.TxRunner
}

// NewDefinitionHandler creates a new DefinitionHandler.
func NewDefinitionHandler(
	definitionRepo models.StackDefinitionRepository,
	chartRepo models.ChartConfigRepository,
	instanceRepo models.StackInstanceRepository,
	templateRepo models.StackTemplateRepository,
	templateChartRepo models.TemplateChartConfigRepository,
) *DefinitionHandler {
	return &DefinitionHandler{
		definitionRepo:    definitionRepo,
		chartRepo:         chartRepo,
		instanceRepo:      instanceRepo,
		templateRepo:      templateRepo,
		templateChartRepo: templateChartRepo,
	}
}

// NewDefinitionHandlerWithVersions creates a DefinitionHandler with template version support.
func NewDefinitionHandlerWithVersions(
	definitionRepo models.StackDefinitionRepository,
	chartRepo models.ChartConfigRepository,
	instanceRepo models.StackInstanceRepository,
	templateRepo models.StackTemplateRepository,
	templateChartRepo models.TemplateChartConfigRepository,
	versionRepo models.TemplateVersionRepository,
) *DefinitionHandler {
	return &DefinitionHandler{
		definitionRepo:    definitionRepo,
		chartRepo:         chartRepo,
		instanceRepo:      instanceRepo,
		templateRepo:      templateRepo,
		templateChartRepo: templateChartRepo,
		versionRepo:       versionRepo,
	}
}

// SetTxRunner sets an optional TxRunner for transactional multi-entity operations.
func (h *DefinitionHandler) SetTxRunner(tx database.TxRunner) {
	h.txRunner = tx
}

// ListDefinitions godoc
// @Summary     List stack definitions
// @Description List stack definitions with server-side pagination
// @Tags        stack-definitions
// @Produce     json
// @Param       page     query    int false "Page number (default 1)"     minimum(1)
// @Param       pageSize query    int false "Items per page (default 25, max 100)" minimum(1) maximum(100)
// @Param       limit    query    int false "Max items to return (default 25, max 100)" minimum(1) maximum(100)
// @Param       offset   query    int false "Number of items to skip (default 0)" minimum(0)
// @Success     200 {object} map[string]interface{} "Paginated list with data, total, page, pageSize"
// @Failure     500 {object} map[string]string
// @Router      /api/v1/stack-definitions [get]
func (h *DefinitionHandler) ListDefinitions(c *gin.Context) {
	pageSize := listPageSizeDefault
	offset := 0
	page := 1

	if ps := c.Query("pageSize"); ps != "" {
		if v, err := strconv.Atoi(ps); err == nil && v > 0 {
			pageSize = v
		}
		if pageSize > listPageSizeMax {
			pageSize = listPageSizeMax
		}
	}

	if p := c.Query("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
		offset = (page - 1) * pageSize
	} else if l := c.Query("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			pageSize = v
			if pageSize > listPageSizeMax {
				pageSize = listPageSizeMax
			}
		}
		if o := c.Query("offset"); o != "" {
			if v, err := strconv.Atoi(o); err == nil && v >= 0 {
				offset = v
			}
		}
	}

	defs, total, err := h.definitionRepo.ListPaged(pageSize, offset)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":     defs,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
}

// CreateDefinition godoc
// @Summary     Create a stack definition
// @Description Create a new stack definition
// @Tags        stack-definitions
// @Accept      json
// @Produce     json
// @Param       definition body     models.StackDefinition true "Definition object"
// @Success     201        {object} models.StackDefinition
// @Failure     400        {object} map[string]string
// @Router      /api/v1/stack-definitions [post]
func (h *DefinitionHandler) CreateDefinition(c *gin.Context) {
	var def models.StackDefinition
	if err := c.ShouldBindJSON(&def); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	def.ID = uuid.New().String()
	def.OwnerID = middleware.GetUserIDFromContext(c)
	now := time.Now().UTC()
	def.CreatedAt = now
	def.UpdatedAt = now

	if def.DefaultBranch == "" {
		def.DefaultBranch = "master"
	}

	if err := def.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.definitionRepo.Create(&def); err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusCreated, def)
}

// GetDefinition godoc
// @Summary     Get a stack definition
// @Description Get a stack definition by ID, including its chart configurations
// @Tags        stack-definitions
// @Produce     json
// @Param       id  path     string true "Definition ID"
// @Success     200 {object} models.StackDefinition
// @Failure     404 {object} map[string]string
// @Router      /api/v1/stack-definitions/{id} [get]
func (h *DefinitionHandler) GetDefinition(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgDefinitionIDRequired})
		return
	}

	def, err := h.definitionRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartRepo.ListByDefinition(id)
	if err != nil {
		status, message := mapError(err, entityChartConfigs)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"definition": def,
		"charts":     charts,
	})
}

// UpdateDefinition godoc
// @Summary     Update a stack definition
// @Description Update an existing stack definition
// @Tags        stack-definitions
// @Accept      json
// @Produce     json
// @Param       id         path     string                 true "Definition ID"
// @Param       definition body     models.StackDefinition  true "Definition object"
// @Success     200        {object} models.StackDefinition
// @Failure     400        {object} map[string]string
// @Failure     404        {object} map[string]string
// @Router      /api/v1/stack-definitions/{id} [put]
func (h *DefinitionHandler) UpdateDefinition(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgDefinitionIDRequired})
		return
	}

	existing, err := h.definitionRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	var update models.StackDefinition
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	existing.Name = update.Name
	existing.Description = update.Description
	existing.DefaultBranch = update.DefaultBranch
	existing.UpdatedAt = time.Now().UTC()

	if err := existing.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.definitionRepo.Update(existing); err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, existing)
}

// DeleteDefinition godoc
// @Summary     Delete a stack definition
// @Description Delete a stack definition if no running instances link to it
// @Tags        stack-definitions
// @Produce     json
// @Param       id  path     string true "Definition ID"
// @Success     204 "No Content"
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Router      /api/v1/stack-definitions/{id} [delete]
func (h *DefinitionHandler) DeleteDefinition(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgDefinitionIDRequired})
		return
	}

	// Check for running or deploying instances using a targeted query
	// instead of loading all instances (avoids full-table scan).
	if h.instanceRepo != nil {
		for _, status := range []string{models.StackStatusRunning, models.StackStatusDeploying} {
			exists, err := h.instanceRepo.ExistsByDefinitionAndStatus(id, status)
			if err == nil && exists {
				c.JSON(http.StatusConflict, gin.H{"error": "Cannot delete definition: running instances exist"})
				return
			}
		}
	}

	if err := h.definitionRepo.Delete(id); err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}

// ExportDefinition godoc
// @Summary     Export a stack definition
// @Description Export a stack definition and its chart configs as a portable JSON bundle
// @Tags        stack-definitions
// @Produce     json
// @Param       id  path     string true "Definition ID"
// @Success     200 {object} DefinitionExportBundle
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/stack-definitions/{id}/export [get]
func (h *DefinitionHandler) ExportDefinition(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgDefinitionIDRequired})
		return
	}

	def, err := h.definitionRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartRepo.ListByDefinition(id)
	if err != nil {
		slog.Error("failed to list charts for export", "definition_id", id, "error", err)
		status, message := mapError(err, entityChartConfigs)
		c.JSON(status, gin.H{"error": message})
		return
	}

	exportCharts := make([]ChartConfigExportData, 0, len(charts))
	for _, ch := range charts {
		exportCharts = append(exportCharts, ChartConfigExportData{
			ChartName:     ch.ChartName,
			RepositoryURL: ch.RepositoryURL,
			SourceRepoURL: ch.SourceRepoURL,
			ChartPath:     ch.ChartPath,
			ChartVersion:  ch.ChartVersion,
			DefaultValues: ch.DefaultValues,
			DeployOrder:   ch.DeployOrder,
		})
	}

	bundle := DefinitionExportBundle{
		SchemaVersion: "1.0",
		ExportedAt:    time.Now().UTC(),
		Definition: DefinitionExportData{
			Name:          def.Name,
			Description:   def.Description,
			DefaultBranch: def.DefaultBranch,
		},
		Charts: exportCharts,
	}

	c.JSON(http.StatusOK, bundle)
}

// ImportDefinition godoc
// @Summary     Import a stack definition
// @Description Import a stack definition from a portable JSON bundle, creating a new definition with fresh IDs
// @Tags        stack-definitions
// @Accept      json
// @Produce     json
// @Param       bundle body     DefinitionExportBundle true "Export bundle"
// @Success     201    {object} map[string]interface{}
// @Failure     400    {object} map[string]string
// @Failure     500    {object} map[string]string
// @Router      /api/v1/stack-definitions/import [post]
func (h *DefinitionHandler) ImportDefinition(c *gin.Context) {
	var bundle DefinitionExportBundle
	if err := c.ShouldBindJSON(&bundle); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	// Validate schema version.
	if bundle.SchemaVersion == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "schema_version is required"})
		return
	}
	if !supportedSchemaVersions[bundle.SchemaVersion] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported schema_version: " + bundle.SchemaVersion})
		return
	}

	// Validate required definition fields.
	if bundle.Definition.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "definition name is required"})
		return
	}

	// Validate chart entries.
	for i, ch := range bundle.Charts {
		if ch.ChartName == "" {
			slog.Error("import bundle contains chart with empty name", "index", i)
			c.JSON(http.StatusBadRequest, gin.H{"error": "chart_name is required for all charts"})
			return
		}
	}

	// Build new definition with fresh IDs.
	now := time.Now().UTC()
	def := models.StackDefinition{
		ID:            uuid.New().String(),
		Name:          bundle.Definition.Name,
		Description:   bundle.Definition.Description,
		DefaultBranch: bundle.Definition.DefaultBranch,
		OwnerID:       middleware.GetUserIDFromContext(c),
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if def.DefaultBranch == "" {
		def.DefaultBranch = "master"
	}

	if err := def.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Build chart models up front so both paths share the same data.
	chartModels := make([]models.ChartConfig, 0, len(bundle.Charts))
	for _, ch := range bundle.Charts {
		chartModels = append(chartModels, models.ChartConfig{
			ID:                uuid.New().String(),
			StackDefinitionID: def.ID,
			ChartName:         ch.ChartName,
			RepositoryURL:     ch.RepositoryURL,
			SourceRepoURL:     ch.SourceRepoURL,
			ChartPath:         ch.ChartPath,
			ChartVersion:      ch.ChartVersion,
			DefaultValues:     ch.DefaultValues,
			DeployOrder:       ch.DeployOrder,
			CreatedAt:         now,
		})
	}

	if h.txRunner == nil {
		slog.Error("txRunner not configured for ImportDefinition")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	var createdCharts []models.ChartConfig
	txErr := h.txRunner.RunInTx(func(repos database.TxRepos) error {
		if err := repos.StackDefinition.Create(&def); err != nil {
			return err
		}
		for i := range chartModels {
			if err := repos.ChartConfig.Create(&chartModels[i]); err != nil {
				return err
			}
		}
		createdCharts = chartModels
		return nil
	})
	if txErr != nil {
		slog.Error("failed to import definition", "error", txErr)
		status, message := mapError(txErr, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"definition": def,
		"charts":     createdCharts,
	})
}

// CheckUpgrade godoc
// @Summary     Check if a template upgrade is available
// @Description Check if the source template has a newer version than the definition's current version
// @Tags        stack-definitions
// @Produce     json
// @Param       id  path     string true "Definition ID"
// @Success     200 {object} map[string]interface{}
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/stack-definitions/{id}/check-upgrade [get]
func (h *DefinitionHandler) CheckUpgrade(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgDefinitionIDRequired})
		return
	}

	def, err := h.definitionRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// No source template — no upgrade possible.
	if def.SourceTemplateID == "" || h.versionRepo == nil {
		c.JSON(http.StatusOK, gin.H{"upgrade_available": false})
		return
	}

	latest, err := h.versionRepo.GetLatestByTemplate(context.Background(), def.SourceTemplateID)
	if err != nil {
		// No versions published yet — no upgrade.
		c.JSON(http.StatusOK, gin.H{"upgrade_available": false})
		return
	}

	if latest.Version == def.SourceTemplateVersion {
		c.JSON(http.StatusOK, gin.H{"upgrade_available": false})
		return
	}

	// Parse the latest snapshot to compute chart changes.
	var snapshot models.TemplateSnapshot
	if err := json.Unmarshal([]byte(latest.Snapshot), &snapshot); err != nil {
		slog.Error("failed to unmarshal latest version snapshot", "version_id", latest.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Get the definition's current charts.
	currentCharts, err := h.chartRepo.ListByDefinition(id)
	if err != nil {
		status, message := mapError(err, entityChartConfigs)
		c.JSON(status, gin.H{"error": message})
		return
	}

	changes := computeUpgradeChanges(currentCharts, snapshot.Charts)

	c.JSON(http.StatusOK, gin.H{
		"upgrade_available": true,
		"current_version":   def.SourceTemplateVersion,
		"latest_version":    latest.Version,
		"changes":           changes,
	})
}

// upgradeChanges describes chart-level differences for an upgrade.
type upgradeChanges struct {
	ChartsAdded     []string `json:"charts_added"`
	ChartsRemoved   []string `json:"charts_removed"`
	ChartsModified  []string `json:"charts_modified"`
	ChartsUnchanged []string `json:"charts_unchanged"`
}

// computeUpgradeChanges compares definition charts against template snapshot charts.
func computeUpgradeChanges(defCharts []models.ChartConfig, templateCharts []models.TemplateChartSnapshotData) upgradeChanges {
	defMap := make(map[string]models.ChartConfig, len(defCharts))
	for _, ch := range defCharts {
		defMap[ch.ChartName] = ch
	}
	tmplMap := make(map[string]models.TemplateChartSnapshotData, len(templateCharts))
	for _, ch := range templateCharts {
		tmplMap[ch.ChartName] = ch
	}

	var changes upgradeChanges
	changes.ChartsAdded = make([]string, 0)
	changes.ChartsRemoved = make([]string, 0)
	changes.ChartsModified = make([]string, 0)
	changes.ChartsUnchanged = make([]string, 0)

	// Charts in template but not in definition = added.
	for _, tch := range templateCharts {
		if _, exists := defMap[tch.ChartName]; !exists {
			changes.ChartsAdded = append(changes.ChartsAdded, tch.ChartName)
		}
	}

	// Charts in definition that match a template chart — modified or unchanged.
	for _, dch := range defCharts {
		tch, inTemplate := tmplMap[dch.ChartName]
		if !inTemplate {
			// Chart exists in definition but not in latest template — "removed" from template.
			changes.ChartsRemoved = append(changes.ChartsRemoved, dch.ChartName)
			continue
		}
		if dch.DefaultValues != tch.DefaultValues || dch.RepositoryURL != tch.RepoURL {
			changes.ChartsModified = append(changes.ChartsModified, dch.ChartName)
		} else {
			changes.ChartsUnchanged = append(changes.ChartsUnchanged, dch.ChartName)
		}
	}

	return changes
}

// ApplyUpgrade godoc
// @Summary     Apply a template upgrade to a definition
// @Description Upgrade a definition to the latest template version, adding new charts and updating defaults
// @Tags        stack-definitions
// @Accept      json
// @Produce     json
// @Param       id  path     string true "Definition ID"
// @Success     200 {object} map[string]interface{}
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/stack-definitions/{id}/upgrade [post]
func (h *DefinitionHandler) ApplyUpgrade(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgDefinitionIDRequired})
		return
	}

	def, err := h.definitionRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	if def.SourceTemplateID == "" || h.versionRepo == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Definition has no source template"})
		return
	}

	latest, err := h.versionRepo.GetLatestByTemplate(context.Background(), def.SourceTemplateID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No template versions available"})
		return
	}

	if latest.Version == def.SourceTemplateVersion {
		c.JSON(http.StatusConflict, gin.H{"error": "Definition is already at the latest version"})
		return
	}

	var snapshot models.TemplateSnapshot
	if err := json.Unmarshal([]byte(latest.Snapshot), &snapshot); err != nil {
		slog.Error("failed to unmarshal version snapshot for upgrade", "version_id", latest.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Get the definition's current charts.
	currentCharts, err := h.chartRepo.ListByDefinition(id)
	if err != nil {
		status, message := mapError(err, entityChartConfigs)
		c.JSON(status, gin.H{"error": message})
		return
	}

	defChartMap := make(map[string]*models.ChartConfig, len(currentCharts))
	for i := range currentCharts {
		defChartMap[currentCharts[i].ChartName] = &currentCharts[i]
	}

	now := time.Now().UTC()

	// Apply changes: add new required charts, update existing chart defaults.
	for _, tch := range snapshot.Charts {
		existing, exists := defChartMap[tch.ChartName]
		if !exists {
			// Add new chart from template.
			newChart := models.ChartConfig{
				ID:                uuid.New().String(),
				StackDefinitionID: def.ID,
				ChartName:         tch.ChartName,
				RepositoryURL:     tch.RepoURL,
				DefaultValues:     tch.DefaultValues,
				DeployOrder:       tch.SortOrder,
				CreatedAt:         now,
			}
			if err := h.chartRepo.Create(&newChart); err != nil {
				slog.Error("failed to create chart during upgrade",
					"chart_name", tch.ChartName,
					"definition_id", def.ID,
					"error", err,
				)
				status, message := mapError(err, entityChartConfig)
				c.JSON(status, gin.H{"error": message})
				return
			}
			continue
		}

		// Update default values for existing charts (preserve structure, update template defaults).
		existing.DefaultValues = tch.DefaultValues
		existing.RepositoryURL = tch.RepoURL
		existing.DeployOrder = tch.SortOrder
		if err := h.chartRepo.Update(existing); err != nil {
			slog.Error("failed to update chart during upgrade",
				"chart_name", tch.ChartName,
				"definition_id", def.ID,
				"error", err,
			)
			status, message := mapError(err, entityChartConfig)
			c.JSON(status, gin.H{"error": message})
			return
		}
	}
	// Note: we do NOT remove charts that the user has — conservative upgrade.

	// Update the definition's source template version.
	def.SourceTemplateVersion = latest.Version
	def.UpdatedAt = now

	if err := h.definitionRepo.Update(def); err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Return the updated definition with its charts.
	updatedCharts, err := h.chartRepo.ListByDefinition(id)
	if err != nil {
		status, message := mapError(err, entityChartConfigs)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"definition": def,
		"charts":     updatedCharts,
	})
}
