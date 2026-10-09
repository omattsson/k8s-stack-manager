package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/database"
	"backend/internal/models"
	"backend/pkg/dberrors"

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
	ChartName       string `json:"chart_name"`
	RepositoryURL   string `json:"repository_url"`
	SourceRepoURL   string `json:"source_repo_url"`
	BuildPipelineID string `json:"build_pipeline_id,omitempty"`
	ChartPath       string `json:"chart_path"`
	ChartVersion    string `json:"chart_version"`
	DefaultValues   string `json:"default_values"`
	DeployOrder     int    `json:"deploy_order"`
}

// DefinitionHandler handles stack definition and chart config endpoints.
type DefinitionHandler struct {
	definitionRepo    models.StackDefinitionRepository
	chartRepo         models.ChartConfigRepository
	instanceRepo      models.StackInstanceRepository
	templateRepo      models.StackTemplateRepository
	templateChartRepo models.TemplateChartConfigRepository
	versionRepo       models.TemplateVersionRepository
	userRepo          models.UserRepository
	txRunner          database.TxRunner
}

// WithUserRepo attaches the user repository. Definition responses then
// include owner_username, and the owner list filter accepts usernames.
// Returns h for chaining.
func (h *DefinitionHandler) WithUserRepo(repo models.UserRepository) *DefinitionHandler {
	h.userRepo = repo
	return h
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
	txRunner database.TxRunner,
) (*DefinitionHandler, error) {
	if txRunner == nil {
		return nil, fmt.Errorf("txRunner must not be nil")
	}
	return &DefinitionHandler{
		definitionRepo:    definitionRepo,
		chartRepo:         chartRepo,
		instanceRepo:      instanceRepo,
		templateRepo:      templateRepo,
		templateChartRepo: templateChartRepo,
		versionRepo:       versionRepo,
		txRunner:          txRunner,
	}, nil
}

// ListDefinitions godoc
// @Summary     List stack definitions
// @Description List stack definitions with server-side pagination, newest first. The filters name and owner combine (AND); total is the number of definitions that match.
// @Description owner is "me" (the authenticated user), a username, or a user ID. A value in UUID form is matched as a user ID first, then as a username. An unknown username or ID gives an empty list. Each definition includes owner_username (omitted when the owner no longer exists) and chart_count (the number of charts; omitted when the count fails).
// @Description The list is paged (default pageSize 25); with owner=me or name the total is the real number of matches, not the page size.
// @Tags        stack-definitions
// @Produce     json
// @Param       name     query    string false "Filter by exact name"
// @Param       owner    query    string false "Filter by owner: 'me', a username, or a user ID"
// @Param       page     query    int false "Page number (default 1)"     minimum(1)
// @Param       pageSize query    int false "Items per page (default 25, max 100)" minimum(1) maximum(100)
// @Param       limit    query    int false "Max items to return (default 25, max 100)" minimum(1) maximum(100)
// @Param       offset   query    int false "Number of items to skip (default 0)" minimum(0)
// @Success     200 {object} map[string]interface{} "Paginated list with data, total, page, pageSize"
// @Failure     400 {object} map[string]string "Owner filter is too long"
// @Failure     401 {object} map[string]string "owner=me without an authenticated user"
// @Failure     500 {object} map[string]string
// @Router      /api/v1/stack-definitions [get]
func (h *DefinitionHandler) ListDefinitions(c *gin.Context) {
	ownerID, ok := resolveOwnerFilter(c, h.userRepo, c.Query("owner"))
	if !ok {
		return
	}
	filter := models.StackDefinitionFilter{Name: c.Query("name"), OwnerID: ownerID}

	page, pageSize, offset := listPagination(c)

	defs, total, err := h.definitionRepo.ListPaged(filter, pageSize, offset)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}
	if defs == nil {
		defs = []models.StackDefinition{}
	}
	ptrs := make([]*models.StackDefinition, len(defs))
	for i := range defs {
		ptrs[i] = &defs[i]
	}
	setDefinitionOwnerNames(h.userRepo, ptrs...)
	h.setDefinitionChartCounts(defs)

	c.JSON(http.StatusOK, gin.H{
		"data":     defs,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
}

// setDefinitionChartCounts sets ChartCount on each definition with one batch
// query. On an error it logs and leaves the field out; the list still returns.
func (h *DefinitionHandler) setDefinitionChartCounts(defs []models.StackDefinition) {
	if len(defs) == 0 || h.chartRepo == nil {
		return
	}
	ids := make([]string, len(defs))
	for i := range defs {
		ids[i] = defs[i].ID
	}
	counts, err := h.chartRepo.CountByDefinitionIDs(ids)
	if err != nil {
		slog.Warn("Failed to count charts for definition list", "error", err)
		return
	}
	for i := range defs {
		n := counts[defs[i].ID]
		defs[i].ChartCount = &n
	}
}

// CreateDefinition godoc
// @Summary     Create a stack definition
// @Description Create a new stack definition. Definition names are unique per owner.
// @Tags        stack-definitions
// @Accept      json
// @Produce     json
// @Param       definition body     models.StackDefinition true "Definition object"
// @Success     201        {object} models.StackDefinition
// @Failure     400        {object} map[string]string
// @Failure     401        {object} map[string]string
// @Failure     409        {object} map[string]string "The owner already has a definition with this name"
// @Failure     500        {object} map[string]string
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

	// Quick deploy sets the owner instance; a user-created definition has none.
	def.OwnerInstanceID = ""

	if respondDefinitionNameTaken(c, h.definitionRepo, def.OwnerID, def.Name, "") {
		return
	}

	if err := h.definitionRepo.Create(&def); err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	setDefinitionOwnerNames(h.userRepo, &def)
	c.JSON(http.StatusCreated, def)
}

// GetDefinition godoc
// @Summary     Get a stack definition
// @Description Get a stack definition by ID, including its chart configurations and owner_username (omitted when the owner no longer exists)
// @Tags        stack-definitions
// @Produce     json
// @Param       id  path     string true "Definition ID"
// @Success     200 {object} DefinitionWithChartsResponse
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

	setDefinitionOwnerNames(h.userRepo, def)
	c.JSON(http.StatusOK, DefinitionWithChartsResponse{
		StackDefinition: *def,
		Charts:          charts,
	})
}

// UpdateDefinition godoc
// @Summary     Update a stack definition
// @Description Update an existing stack definition. A changed name must be unique for the owner of the definition.
// @Tags        stack-definitions
// @Accept      json
// @Produce     json
// @Param       id         path     string                 true "Definition ID"
// @Param       definition body     models.StackDefinition  true "Definition object"
// @Success     200        {object} models.StackDefinition
// @Failure     400        {object} map[string]string
// @Failure     401        {object} map[string]string
// @Failure     404        {object} map[string]string
// @Failure     409        {object} map[string]string "The owner already has a definition with this name"
// @Failure     500        {object} map[string]string
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

	if update.Name != existing.Name && update.Name != "" {
		if respondDefinitionNameTaken(c, h.definitionRepo, existing.OwnerID, update.Name, existing.ID) {
			return
		}
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

	setDefinitionOwnerNames(h.userRepo, existing)
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
			ChartName:       ch.ChartName,
			RepositoryURL:   ch.RepositoryURL,
			SourceRepoURL:   ch.SourceRepoURL,
			BuildPipelineID: ch.BuildPipelineID,
			ChartPath:       ch.ChartPath,
			ChartVersion:    ch.ChartVersion,
			DefaultValues:   ch.DefaultValues,
			DeployOrder:     ch.DeployOrder,
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
// @Description Import a stack definition from a portable JSON bundle, creating a new definition with fresh IDs. Definition names are unique per owner: when the caller already has a definition with the bundle name, the import uses "<name> (imported)", then "<name> (imported 2)", ... The response holds the final name (409 when no free name is found).
// @Tags        stack-definitions
// @Accept      json
// @Produce     json
// @Param       bundle body     DefinitionExportBundle true "Export bundle"
// @Success     201    {object} DefinitionWithChartsResponse
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

	// Definition names are unique per owner: an existing name gets the
	// suffix " (imported)", " (imported 2)", ...
	finalName, nameErr := uniqueDefinitionName(h.definitionRepo, def.OwnerID, importedDefinitionName(def.Name))
	if errors.Is(nameErr, errNoFreeDefinitionName) {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("A stack definition named %q already exists for this owner", def.Name)})
		return
	}
	if nameErr != nil {
		status, message := mapError(nameErr, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}
	def.Name = finalName

	// Build chart models up front so both paths share the same data.
	chartModels := make([]models.ChartConfig, 0, len(bundle.Charts))
	for _, ch := range bundle.Charts {
		chartModels = append(chartModels, models.ChartConfig{
			ID:                uuid.New().String(),
			StackDefinitionID: def.ID,
			ChartName:         ch.ChartName,
			RepositoryURL:     ch.RepositoryURL,
			SourceRepoURL:     ch.SourceRepoURL,
			BuildPipelineID:   ch.BuildPipelineID,
			ChartPath:         ch.ChartPath,
			ChartVersion:      ch.ChartVersion,
			DefaultValues:     ch.DefaultValues,
			DeployOrder:       ch.DeployOrder,
			CreatedAt:         now,
		})
	}

	if h.txRunner == nil {
		slog.Error("failed to import definition", "error", "transaction runner is not configured")
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
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

	c.JSON(http.StatusCreated, DefinitionWithChartsResponse{
		StackDefinition: def,
		Charts:          createdCharts,
	})
}

// CheckUpgrade godoc
// @Summary     Check if a template upgrade is available
// @Description Check if the latest published snapshot of the source template has another version than the definition's current version. An unpublished template, or one without a snapshot, gives upgrade_available=false.
// @Description chart_diffs has the shape of the version diff chart_diffs: left = the definition charts now (locked values from the current source version snapshot), right = the latest snapshot. change_type "removed" means the chart is only in the definition; the upgrade keeps it.
// @Tags        stack-definitions
// @Accept      json
// @Produce     json
// @Param       id  path     string true "Definition ID"
// @Success     200 {object} upgradeCheckResponse
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
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

	// The upgrade target is the latest published snapshot of the template.
	release, err := h.upgradeRelease(c.Request.Context(), def.SourceTemplateID)
	if err != nil {
		if errors.Is(err, errNoPublishedVersion) {
			// Not published, or no snapshot yet — no upgrade.
			c.JSON(http.StatusOK, gin.H{"upgrade_available": false})
			return
		}
		slog.Error("failed to read latest template version", "template_id", def.SourceTemplateID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	latest := release.Version
	snapshot := release.Snapshot

	if latest.Version == def.SourceTemplateVersion {
		c.JSON(http.StatusOK, gin.H{"upgrade_available": false})
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
	chartDiffs := h.upgradeChartDiffs(c.Request.Context(), def, currentCharts, snapshot)

	c.JSON(http.StatusOK, upgradeCheckResponse{
		UpgradeAvailable: true,
		CurrentVersion:   def.SourceTemplateVersion,
		LatestVersion:    latest.Version,
		Changes:          &changes,
		ChartDiffs:       chartDiffs,
	})
}

// upgradeCheckResponse is the response of GET /stack-definitions/:id/check-upgrade.
// Without an upgrade only upgrade_available (false) is set.
type upgradeCheckResponse struct {
	UpgradeAvailable bool             `json:"upgrade_available"`
	CurrentVersion   string           `json:"current_version,omitempty"`
	LatestVersion    string           `json:"latest_version,omitempty"`
	Changes          *upgradeChanges  `json:"changes,omitempty"`
	ChartDiffs       []chartDiffEntry `json:"chart_diffs,omitempty"`
}

// upgradeChartDiffs compares the definition charts (left) with the charts of
// the upgrade target snapshot (right), in the shape of the version diff
// chart_diffs. Left locked values and required flags come from the snapshot
// of the definition's current source version; without that snapshot they are
// taken from the target (shown as unchanged). change_type
// "removed" means the chart is in the definition but not in the target; the
// upgrade keeps such charts.
func (h *DefinitionHandler) upgradeChartDiffs(ctx context.Context, def *models.StackDefinition, current []models.ChartConfig, target models.TemplateSnapshot) []chartDiffEntry {
	sourceCharts := make(map[string]models.TemplateChartSnapshotData)
	if versions, err := h.versionRepo.ListByTemplate(ctx, def.SourceTemplateID); err != nil {
		slog.Warn("failed to list template versions for upgrade diff", "template_id", def.SourceTemplateID, "error", err)
	} else {
		for _, v := range versions { // newest first: the newest match wins
			if v.Version != def.SourceTemplateVersion {
				continue
			}
			var snap models.TemplateSnapshot
			if err := json.Unmarshal([]byte(v.Snapshot), &snap); err == nil {
				for _, ch := range snap.Charts {
					sourceCharts[ch.ChartName] = ch
				}
			}
			break
		}
	}

	targetCharts := make(map[string]models.TemplateChartSnapshotData, len(target.Charts))
	for _, ch := range target.Charts {
		targetCharts[ch.ChartName] = ch
	}

	left := make([]models.TemplateChartSnapshotData, 0, len(current))
	for _, ch := range current {
		src, known := sourceCharts[ch.ChartName]
		if !known {
			// Unknown current locked values: take the target's, so they
			// do not show as a change.
			src = targetCharts[ch.ChartName]
		}
		left = append(left, models.TemplateChartSnapshotData{
			ChartName:     ch.ChartName,
			RepoURL:       ch.RepositoryURL,
			DefaultValues: ch.DefaultValues,
			LockedValues:  src.LockedValues,
			IsRequired:    src.IsRequired,
			SortOrder:     ch.DeployOrder,
			ChartPath:     ch.ChartPath,
			ChartVersion:  ch.ChartVersion,
		})
	}
	// ApplyUpgrade keeps the definition's chart_version and chart_path when
	// the target has none, so an empty target value is shown as unchanged.
	currentByName := make(map[string]models.ChartConfig, len(current))
	for _, ch := range current {
		currentByName[ch.ChartName] = ch
	}
	right := make([]models.TemplateChartSnapshotData, 0, len(target.Charts))
	for _, ch := range target.Charts {
		if cur, ok := currentByName[ch.ChartName]; ok {
			if ch.ChartVersion == "" {
				ch.ChartVersion = cur.ChartVersion
			}
			if ch.ChartPath == "" {
				ch.ChartPath = cur.ChartPath
			}
		}
		right = append(right, ch)
	}
	diffs := computeChartDiffs(left, right, target.SchemaVersion >= 1)
	if diffs == nil {
		diffs = []chartDiffEntry{}
	}
	return diffs
}

// upgradeRelease returns the latest published snapshot of a template, the
// target of definition upgrades. An unpublished template, or one without a
// snapshot, gives errNoPublishedVersion.
func (h *DefinitionHandler) upgradeRelease(ctx context.Context, templateID string) (*templateRelease, error) {
	if h.templateRepo != nil {
		tmpl, err := h.templateRepo.FindByID(templateID)
		if err != nil {
			if errors.Is(err, dberrors.ErrNotFound) {
				return nil, errNoPublishedVersion
			}
			return nil, fmt.Errorf("find template: %w", err)
		}
		if !tmpl.IsPublished {
			return nil, errNoPublishedVersion
		}
	}
	return latestTemplateRelease(ctx, h.versionRepo, h.templateChartRepo, templateID)
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
// @Description Upgrade a definition to the latest published snapshot of its source template, adding new charts and updating defaults, repository URL, deploy order and (when the snapshot has them) chart version, chart path, source repo and build pipeline. Charts that are only in the definition stay.
// @Description An unpublished template, or one without a snapshot, gives 409 "Template has no published version".
// @Tags        stack-definitions
// @Accept      json
// @Produce     json
// @Param       id  path     string true "Definition ID"
// @Success     200 {object} DefinitionWithChartsResponse
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string "Already at the latest version, or no published version"
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

	// The upgrade target is the latest published snapshot of the template.
	release, err := h.upgradeRelease(c.Request.Context(), def.SourceTemplateID)
	if err != nil {
		if errors.Is(err, errNoPublishedVersion) {
			c.JSON(http.StatusConflict, gin.H{"error": msgNoPublishedVersion})
			return
		}
		slog.Error("failed to read latest template version for upgrade", "template_id", def.SourceTemplateID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	latest := release.Version

	if latest.Version == def.SourceTemplateVersion {
		c.JSON(http.StatusConflict, gin.H{"error": "Definition is already at the latest version"})
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
	for _, tch := range release.Charts {
		existing, exists := defChartMap[tch.ChartName]
		if !exists {
			// Add new chart from template.
			newChart := models.ChartConfig{
				ID:                uuid.New().String(),
				StackDefinitionID: def.ID,
				ChartName:         tch.ChartName,
				RepositoryURL:     tch.RepositoryURL,
				SourceRepoURL:     tch.SourceRepoURL,
				BuildPipelineID:   tch.BuildPipelineID,
				ChartPath:         tch.ChartPath,
				ChartVersion:      tch.ChartVersion,
				DefaultValues:     tch.DefaultValues,
				DeployOrder:       tch.DeployOrder,
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
		existing.RepositoryURL = tch.RepositoryURL
		existing.DeployOrder = tch.DeployOrder
		// Chart source fields follow the template when the snapshot has them.
		if tch.ChartVersion != "" {
			existing.ChartVersion = tch.ChartVersion
		}
		if tch.ChartPath != "" {
			existing.ChartPath = tch.ChartPath
		}
		if tch.SourceRepoURL != "" {
			existing.SourceRepoURL = tch.SourceRepoURL
		}
		if tch.BuildPipelineID != "" {
			existing.BuildPipelineID = tch.BuildPipelineID
		}
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

	c.JSON(http.StatusOK, DefinitionWithChartsResponse{
		StackDefinition: *def,
		Charts:          updatedCharts,
	})
}
