package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
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
	txRunner database.TxRunner,
) (*TemplateHandler, error) {
	if txRunner == nil {
		return nil, fmt.Errorf("txRunner must not be nil")
	}
	return &TemplateHandler{
		templateRepo:    templateRepo,
		chartRepo:       chartRepo,
		definitionRepo:  definitionRepo,
		chartConfigRepo: chartConfigRepo,
		versionRepo:     versionRepo,
		txRunner:        txRunner,
	}, nil
}

// TemplateListItem extends StackTemplate with computed fields for the gallery.
type TemplateListItem struct {
	models.StackTemplate
	DefinitionCount int    `json:"definition_count"`
	OwnerUsername   string `json:"owner_username,omitempty"`
}

// TemplateDetailResponse is a flat response for GET /templates/:id that embeds
// chart configs directly instead of wrapping in a "template" key. The fields
// and charts are the working copy (draft). PublishedVersion and
// PublishedVersionID describe the latest snapshot (null when the template has
// no snapshot). HasUnpublishedChanges is true when the working copy differs
// from the latest snapshot, or when there is no snapshot yet.
type TemplateDetailResponse struct {
	models.StackTemplate
	Charts                []models.TemplateChartConfig `json:"charts"`
	PublishedVersion      *string                      `json:"published_version"`
	PublishedVersionID    *string                      `json:"published_version_id"`
	HasUnpublishedChanges bool                         `json:"has_unpublished_changes"`
	// PublishedCharts are the charts of the latest snapshot: what Use
	// Template and Quick Deploy apply. The IDs are the template chart config
	// IDs at publish time; Use Template accepts them as chart_overrides keys.
	// Null when the template has no snapshot.
	PublishedCharts []models.TemplateChartConfig `json:"published_charts"`
}

// DefinitionWithChartsResponse is a flat response for endpoints that return a
// stack definition together with its chart configs.
type DefinitionWithChartsResponse struct {
	models.StackDefinition
	Charts []models.ChartConfig `json:"charts"`
}

// ListTemplates godoc
// @Summary     List stack templates
// @Description List published templates for regular users, all templates for devops/admin. Includes definition_count and owner_username. Supports server-side pagination.
// @Description name filters by exact template name (same as the stack-definitions name filter); the response keeps the paged envelope.
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       name     query    string false "Filter by exact name"
// @Param       page     query    int false "Page number (default 1)"     minimum(1)
// @Param       pageSize query    int false "Items per page (default 25, max 100)" minimum(1) maximum(100)
// @Success     200 {object} map[string]interface{} "Paginated list with data, total, page, pageSize"
// @Failure     500 {object} map[string]string
// @Router      /api/v1/templates [get]
func (h *TemplateHandler) ListTemplates(c *gin.Context) {
	pageSize := 25
	if ps := c.Query("pageSize"); ps != "" {
		if v, err := strconv.Atoi(ps); err == nil && v > 0 {
			pageSize = v
		}
		if pageSize > 100 {
			pageSize = 100
		}
	}
	page := 1
	if p := c.Query("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}
	offset := (page - 1) * pageSize

	role := middleware.GetRoleFromContext(c)
	name := c.Query("name")

	var templates []models.StackTemplate
	var total int64
	var err error
	if role == "admin" || role == "devops" {
		templates, total, err = h.templateRepo.ListPaged(pageSize, offset, name)
	} else {
		templates, total, err = h.templateRepo.ListPublishedPaged(pageSize, offset, name)
	}
	if err != nil {
		status, message := mapError(err, entityTemplate)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Batch-fetch definition counts and owner usernames (2 queries instead of N+1).
	templateIDs := make([]string, len(templates))
	ownerIDSet := make(map[string]struct{})
	for i, t := range templates {
		templateIDs[i] = t.ID
		if t.OwnerID != "" {
			ownerIDSet[t.OwnerID] = struct{}{}
		}
	}

	defCountMap := make(map[string]int)
	if h.definitionRepo != nil && len(templateIDs) > 0 {
		counts, countErr := h.definitionRepo.CountByTemplateIDs(templateIDs)
		if countErr != nil {
			slog.Warn("failed to batch-fetch definition counts", "error", countErr)
		} else {
			defCountMap = counts
		}
	}

	usernameMap := make(map[string]string)
	if h.userRepo != nil && len(ownerIDSet) > 0 {
		ownerIDs := make([]string, 0, len(ownerIDSet))
		for id := range ownerIDSet {
			ownerIDs = append(ownerIDs, id)
		}
		users, userErr := h.userRepo.FindByIDs(ownerIDs)
		if userErr != nil {
			slog.Warn("failed to batch-fetch users", "error", userErr)
		} else {
			for id, u := range users {
				usernameMap[id] = u.Username
			}
		}
	}

	items := make([]TemplateListItem, len(templates))
	for i, t := range templates {
		items[i] = TemplateListItem{
			StackTemplate:   t,
			DefinitionCount: defCountMap[t.ID],
			OwnerUsername:   usernameMap[t.OwnerID],
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"data":     items,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
}

// createTemplateRequest mirrors models.StackTemplate's writable fields and
// adds an optional Charts array so the entire template + its initial chart
// set can be created in a single request. Existing callers that only send
// template fields (no "charts" key) keep their current behaviour — the
// charts slice is just nil and the legacy single-step create path runs.
type createTemplateRequest struct {
	Name          string                       `json:"name" binding:"required"`
	Description   string                       `json:"description"`
	Category      string                       `json:"category"`
	Version       string                       `json:"version"`
	DefaultBranch string                       `json:"default_branch"`
	IsPublished   bool                         `json:"is_published"`
	Charts        []models.TemplateChartConfig `json:"charts"`
}

// CreateTemplate godoc
// @Summary     Create a stack template
// @Description Create a new stack template (devops/admin only). May include a `charts` array to register the initial chart set in the same transaction.
// @Description With is_published=true the template is published in the same transaction (first version snapshot; version is then required). A failed publish rolls the create back.
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       template body     createTemplateRequest true "Template object (may include charts[])"
// @Success     201      {object} TemplateDetailResponse
// @Failure     400      {object} map[string]string
// @Failure     401      {object} map[string]string
// @Failure     403      {object} map[string]string
// @Failure     500      {object} map[string]string
// @Router      /api/v1/templates [post]
func (h *TemplateHandler) CreateTemplate(c *gin.Context) {
	var req createTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	now := time.Now().UTC()
	tmpl := models.StackTemplate{
		ID:            uuid.New().String(),
		Name:          req.Name,
		Description:   req.Description,
		Category:      req.Category,
		Version:       strings.TrimSpace(req.Version),
		DefaultBranch: req.DefaultBranch,
		IsPublished:   req.IsPublished,
		OwnerID:       middleware.GetUserIDFromContext(c),
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := tmpl.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// A template created as published gets its first snapshot through the
	// normal publish path, in the same transaction as the create: a failed
	// publish rolls the create back.
	publishAfterCreate := tmpl.IsPublished && h.versionRepo != nil
	if publishAfterCreate {
		if _, err := validatePublishInput(tmpl.Version, ""); err != nil {
			h.respondPublishError(c, tmpl.ID, err)
			return
		}
		tmpl.IsPublished = false
	}

	// Stamp + validate every chart up-front so we surface a 400 before
	// touching the DB. Backend-controlled fields (ID, StackTemplateID,
	// CreatedAt) are overwritten from any value the caller sent.
	chartModels := make([]models.TemplateChartConfig, len(req.Charts))
	for i, ch := range req.Charts {
		ch.ID = uuid.New().String()
		ch.StackTemplateID = tmpl.ID
		ch.CreatedAt = now
		if err := ch.Validate(); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("charts[%d]: %s", i, err.Error())})
			return
		}
		chartModels[i] = ch
	}

	// Legacy path: no charts and no publish, no need for a transaction.
	if len(chartModels) == 0 && !publishAfterCreate {
		if err := h.templateRepo.Create(&tmpl); err != nil {
			status, message := mapError(err, entityTemplate)
			c.JSON(status, gin.H{"error": message})
			return
		}
		c.JSON(http.StatusCreated, TemplateDetailResponse{StackTemplate: tmpl, Charts: []models.TemplateChartConfig{}, HasUnpublishedChanges: true})
		return
	}

	// Transactional path: template + every chart (+ the first snapshot)
	// commit together, or nothing does. Prevents the partial-state surprise
	// seed scripts saw before this fix (template created, charts silently
	// dropped).
	if h.txRunner == nil {
		slog.Error("CreateTemplate with charts requires a transaction runner but none is configured")
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	var published *models.TemplateVersion
	txErr := h.txRunner.RunInTx(func(repos database.TxRepos) error {
		if err := repos.StackTemplate.Create(&tmpl); err != nil {
			return err
		}
		for i := range chartModels {
			if err := repos.TemplateChart.Create(&chartModels[i]); err != nil {
				return err
			}
		}
		if !publishAfterCreate {
			return nil
		}
		v, _, err := publishInTx(c.Request.Context(), repos, &tmpl, "", "", middleware.GetUserIDFromContext(c))
		published = v
		return err
	})
	if txErr != nil {
		if publishAfterCreate {
			h.respondPublishError(c, tmpl.ID, txErr)
			return
		}
		status, message := mapError(txErr, entityTemplate)
		c.JSON(status, gin.H{"error": message})
		return
	}

	resp := TemplateDetailResponse{StackTemplate: tmpl, Charts: chartModels, HasUnpublishedChanges: true}
	if published != nil {
		resp.PublishedVersion = &published.Version
		resp.PublishedVersionID = &published.ID
		resp.PublishedCharts = models.NewTemplateSnapshot(&tmpl, chartModels).ChartConfigs(tmpl.ID, nil)
		resp.HasUnpublishedChanges = false
	}
	c.JSON(http.StatusCreated, resp)
}

// GetTemplate godoc
// @Summary     Get a stack template
// @Description Get a stack template by ID, including its chart configurations. The template fields and charts are the working copy (draft).
// @Description published_version and published_version_id describe the latest published snapshot (null when there is none). has_unpublished_changes is true when the working copy differs from that snapshot (or there is no snapshot).
// @Description published_charts are the charts of that snapshot (what Use Template and Quick Deploy apply; null when there is none). Their IDs are valid chart_overrides keys for Use Template.
// @Description charts are the working copy charts only for the template owner and admins. For other users charts equals published_charts (an empty list without a snapshot), version is the published version (empty without a snapshot) and has_unpublished_changes is false, so draft information is not exposed.
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       id  path     string true "Template ID"
// @Success     200 {object} TemplateDetailResponse
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
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

	if charts == nil {
		charts = []models.TemplateChartConfig{}
	}
	resp := TemplateDetailResponse{
		StackTemplate:         *tmpl,
		Charts:                charts,
		HasUnpublishedChanges: true,
	}
	if h.versionRepo != nil {
		release, relErr := latestTemplateRelease(c.Request.Context(), h.versionRepo, h.chartRepo, id)
		switch {
		case relErr == nil:
			resp.PublishedVersion = &release.Version.Version
			resp.PublishedVersionID = &release.Version.ID
			resp.PublishedCharts = release.Charts
			// A legacy snapshot always counts as changed (publish stores the
			// full format).
			resp.HasUnpublishedChanges = release.Snapshot.SchemaVersion < models.TemplateSnapshotSchemaVersion ||
				!models.SameTemplateContent(release.Snapshot, models.NewTemplateSnapshot(tmpl, charts))
		case errors.Is(relErr, errNoPublishedVersion):
			// No snapshot yet: the whole working copy is unpublished.
		default:
			slog.Error("failed to read latest template version", "template_id", id, "error", relErr)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}
		// Only users who can manage the template see the working copy
		// charts; others see what they get (the published charts).
		if !canManageTemplate(c, tmpl) {
			resp.Charts = resp.PublishedCharts
			if resp.Charts == nil {
				resp.Charts = []models.TemplateChartConfig{}
			}
			resp.Version = ""
			if resp.PublishedVersion != nil {
				resp.Version = *resp.PublishedVersion
			}
			resp.HasUnpublishedChanges = false
		}
	}
	c.JSON(http.StatusOK, resp)
}

// updateTemplateRequest is the body of PUT /templates/:id. Only the fields
// that are present change (partial update); is_published and owner_id are
// not writable here.
type updateTemplateRequest struct {
	Name          *string `json:"name"`
	Description   *string `json:"description"`
	Category      *string `json:"category"`
	Version       *string `json:"version"`
	DefaultBranch *string `json:"default_branch"`
}

// msgTemplateForbidden is the 403 message for template changes by a user who
// is neither the owner nor an admin.
const msgTemplateForbidden = "Only the template owner or an admin can change this template"

// canManageTemplate reports whether the caller may change the template: an
// admin, or the template owner (same rule as the bulk template operations).
func canManageTemplate(c *gin.Context, tmpl *models.StackTemplate) bool {
	return middleware.GetRoleFromContext(c) == "admin" || tmpl.OwnerID == middleware.GetUserIDFromContext(c)
}

// findManagedTemplate loads the template of the :id path parameter and checks
// that the caller may change it. It writes the error response (404, 403) and
// returns nil on failure.
func (h *TemplateHandler) findManagedTemplate(c *gin.Context) *models.StackTemplate {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgTemplateIDRequired})
		return nil
	}
	tmpl, err := h.templateRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityTemplate)
		c.JSON(status, gin.H{"error": message})
		return nil
	}
	if !canManageTemplate(c, tmpl) {
		c.JSON(http.StatusForbidden, gin.H{"error": msgTemplateForbidden})
		return nil
	}
	return tmpl
}

// UpdateTemplate godoc
// @Summary     Update a stack template
// @Description Update the working copy (draft) of a stack template (template owner or admin). Allowed while the template is published: users keep getting the latest published snapshot until the next publish.
// @Description Partial update: only the fields in the body change. version is trimmed.
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       id       path     string                true "Template ID"
// @Param       template body     updateTemplateRequest true "Fields to change"
// @Success     200      {object} models.StackTemplate
// @Failure     400      {object} map[string]string
// @Failure     401      {object} map[string]string
// @Failure     403      {object} map[string]string
// @Failure     404      {object} map[string]string
// @Failure     500      {object} map[string]string
// @Router      /api/v1/templates/{id} [put]
func (h *TemplateHandler) UpdateTemplate(c *gin.Context) {
	existing := h.findManagedTemplate(c)
	if existing == nil {
		return
	}

	var update updateTemplateRequest
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	if update.Name != nil {
		existing.Name = *update.Name
	}
	if update.Description != nil {
		existing.Description = *update.Description
	}
	if update.Category != nil {
		existing.Category = *update.Category
	}
	if update.Version != nil {
		existing.Version = strings.TrimSpace(*update.Version)
	}
	if update.DefaultBranch != nil {
		existing.DefaultBranch = *update.DefaultBranch
	}
	existing.UpdatedAt = time.Now().UTC()

	if err := existing.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(existing.Version) > maxPublishVersionLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Version must be at most %d characters", maxPublishVersionLength)})
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
// @Description Delete a stack template if no definitions link to it (template owner or admin)
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       id  path     string true "Template ID"
// @Success     204 "No Content"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/templates/{id} [delete]
func (h *TemplateHandler) DeleteTemplate(c *gin.Context) {
	tmpl := h.findManagedTemplate(c)
	if tmpl == nil {
		return
	}
	id := tmpl.ID

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

// publishTemplateRequest is the optional body of POST /templates/:id/publish.
type publishTemplateRequest struct {
	// Version of the new snapshot. Default: the version of the working copy.
	Version string `json:"version"`
	// ChangeSummary is stored with the snapshot.
	ChangeSummary string `json:"change_summary"`
}

// publishTemplateResponse is the response of POST /templates/:id/publish: the
// template plus the snapshot that users now get.
type publishTemplateResponse struct {
	models.StackTemplate
	PublishedVersion      string `json:"published_version"`
	PublishedVersionID    string `json:"published_version_id"`
	SnapshotCreated       bool   `json:"snapshot_created"`
	HasUnpublishedChanges bool   `json:"has_unpublished_changes"`
}

// Publish limits.
const (
	maxPublishVersionLength = 50
	maxChangeSummaryLength  = 2000
)

// errVersionExists means that the template already has a snapshot with the
// requested version string.
type errVersionExists struct{ version string }

func (e *errVersionExists) Error() string {
	return fmt.Sprintf("Version %s already exists", e.version)
}

// errPublishValidation is a publish input error (HTTP 400).
type errPublishValidation struct{ msg string }

func (e *errPublishValidation) Error() string { return e.msg }

// PublishTemplate godoc
// @Summary     Publish a stack template
// @Description Store the working copy (template fields + charts) as a new version snapshot and make the template visible to all users (template owner or admin).
// @Description The body is optional. version (trimmed) defaults to the version of the working copy; the working copy takes the published version.
// @Description A version that already exists for the template gives 409 "Version x already exists".
// @Description When the working copy equals the latest snapshot (same content and version), no snapshot is created (snapshot_created=false) and the response is 200: publish is idempotent.
// @Description Use Template, Quick Deploy and definition upgrades read the latest snapshot, never the working copy.
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       id   path     string                 true  "Template ID"
// @Param       body body     publishTemplateRequest false "Version and change summary (optional)"
// @Success     200  {object} publishTemplateResponse
// @Failure     400  {object} map[string]string
// @Failure     401  {object} map[string]string
// @Failure     403  {object} map[string]string
// @Failure     404  {object} map[string]string
// @Failure     409  {object} map[string]string "Version already exists"
// @Failure     500  {object} map[string]string
// @Router      /api/v1/templates/{id}/publish [post]
func (h *TemplateHandler) PublishTemplate(c *gin.Context) {
	var req publishTemplateRequest
	if c.Request.Body != nil {
		if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
			return
		}
	}

	tmpl := h.findManagedTemplate(c)
	if tmpl == nil {
		return
	}

	updated, version, created, err := h.publishWorkingCopy(c.Request.Context(), tmpl.ID, req.Version, req.ChangeSummary, middleware.GetUserIDFromContext(c))
	if err != nil {
		h.respondPublishError(c, tmpl.ID, err)
		return
	}

	resp := publishTemplateResponse{StackTemplate: *updated, SnapshotCreated: created}
	if version != nil {
		resp.PublishedVersion = version.Version
		resp.PublishedVersionID = version.ID
	}
	c.JSON(http.StatusOK, resp)
}

// respondPublishError writes the HTTP error for a publish error.
func (h *TemplateHandler) respondPublishError(c *gin.Context, templateID string, err error) {
	var exists *errVersionExists
	var invalid *errPublishValidation
	switch {
	case errors.As(err, &exists):
		c.JSON(http.StatusConflict, gin.H{"error": exists.Error()})
	case errors.As(err, &invalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": invalid.Error()})
	default:
		status, message := mapError(err, entityTemplate)
		if status == http.StatusInternalServerError {
			slog.Error("failed to publish template", "template_id", templateID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
	}
}

// validatePublishInput checks a publish version (trimmed) and change summary.
// It returns the trimmed version.
func validatePublishInput(version, changeSummary string) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return "", &errPublishValidation{msg: "Version is required to publish"}
	}
	if len(version) > maxPublishVersionLength {
		return "", &errPublishValidation{msg: fmt.Sprintf("Version must be at most %d characters", maxPublishVersionLength)}
	}
	if len(changeSummary) > maxChangeSummaryLength {
		return "", &errPublishValidation{msg: fmt.Sprintf("Change summary must be at most %d characters", maxChangeSummaryLength)}
	}
	return version, nil
}

// publishWorkingCopy publishes the working copy of a template in one
// transaction: it locks the template row (SELECT ... FOR UPDATE), stores a
// snapshot unless the working copy equals the latest snapshot, and marks the
// template as published. It returns the updated template, the snapshot that
// users now get and whether a new snapshot was created.
func (h *TemplateHandler) publishWorkingCopy(ctx context.Context, templateID, version, changeSummary, userID string) (*models.StackTemplate, *models.TemplateVersion, bool, error) {
	// Without version support (legacy constructor) publish only flips the flag.
	if h.versionRepo == nil {
		tmpl, err := h.templateRepo.FindByID(templateID)
		if err != nil {
			return nil, nil, false, err
		}
		if version == "" {
			version = tmpl.Version
		}
		if version, err = validatePublishInput(version, changeSummary); err != nil {
			return nil, nil, false, err
		}
		tmpl.Version = version
		tmpl.IsPublished = true
		tmpl.UpdatedAt = time.Now().UTC()
		return tmpl, nil, false, h.templateRepo.Update(tmpl)
	}
	if h.txRunner == nil {
		return nil, nil, false, fmt.Errorf("publish requires a transaction runner")
	}

	var (
		tmpl      *models.StackTemplate
		published *models.TemplateVersion
		created   bool
	)
	err := h.txRunner.RunInTx(func(repos database.TxRepos) error {
		locked, err := repos.StackTemplate.FindByIDForUpdate(templateID)
		if err != nil {
			return err
		}
		v, c, err := publishInTx(ctx, repos, locked, version, changeSummary, userID)
		if err != nil {
			return err
		}
		tmpl, published, created = locked, v, c
		return nil
	})
	if err != nil {
		return nil, nil, false, err
	}
	return tmpl, published, created, nil
}

// publishInTx is the publish core. It runs inside a transaction (repos are
// the transactional repositories). version defaults to the working copy
// version. When the working copy (with the target version) equals the latest
// snapshot, no snapshot is created; otherwise the version must be new for the
// template. tmpl is updated in place.
func publishInTx(ctx context.Context, repos database.TxRepos, tmpl *models.StackTemplate, version, changeSummary, userID string) (*models.TemplateVersion, bool, error) {
	if version == "" {
		version = tmpl.Version
	}
	version, err := validatePublishInput(version, changeSummary)
	if err != nil {
		return nil, false, err
	}

	charts, err := repos.TemplateChart.ListByTemplate(tmpl.ID)
	if err != nil {
		return nil, false, fmt.Errorf("list template charts: %w", err)
	}

	candidate := *tmpl
	candidate.Version = version
	candidate.IsPublished = true
	snapshot := models.NewTemplateSnapshot(&candidate, charts)

	versions, err := repos.TemplateVersion.ListByTemplate(ctx, tmpl.ID)
	if err != nil {
		return nil, false, fmt.Errorf("list template versions: %w", err)
	}

	var published *models.TemplateVersion
	created := false
	// legacyVersion is the version string of a legacy latest snapshot. A
	// legacy snapshot does not hold the chart source fields (chart_version,
	// chart_path, ...), so it always counts as changed, and its version
	// string may be reused once to store the full format.
	legacyVersion := ""
	createdAt := time.Now().UTC()
	if len(versions) > 0 {
		latest := versions[0]
		var latestSnapshot models.TemplateSnapshot
		if err := json.Unmarshal([]byte(latest.Snapshot), &latestSnapshot); err != nil {
			return nil, false, fmt.Errorf("unmarshal template version %s: %w", latest.ID, err)
		}
		switch {
		case latestSnapshot.SchemaVersion < models.TemplateSnapshotSchemaVersion:
			legacyVersion = latest.Version
		case models.SameTemplateContent(latestSnapshot, snapshot):
			published = &latest
		}
		// The new snapshot must sort first, also with clock skew between
		// replicas (the template row lock serializes publishes).
		if !createdAt.After(latest.CreatedAt) {
			createdAt = latest.CreatedAt.Add(time.Millisecond)
		}
	}

	if published == nil {
		for _, v := range versions {
			if v.Version == version && !(legacyVersion != "" && version == legacyVersion) {
				return nil, false, &errVersionExists{version: version}
			}
		}
		snapshotBytes, err := json.Marshal(snapshot)
		if err != nil {
			return nil, false, fmt.Errorf("marshal template snapshot: %w", err)
		}
		published = &models.TemplateVersion{
			ID:            uuid.New().String(),
			TemplateID:    tmpl.ID,
			Version:       version,
			Snapshot:      string(snapshotBytes),
			ChangeSummary: changeSummary,
			CreatedBy:     userID,
			CreatedAt:     createdAt,
		}
		if err := repos.TemplateVersion.Create(ctx, published); err != nil {
			return nil, false, fmt.Errorf("create template version: %w", err)
		}
		created = true
	}

	if !tmpl.IsPublished || tmpl.Version != version {
		tmpl.IsPublished = true
		tmpl.Version = version
		tmpl.UpdatedAt = time.Now().UTC()
		if err := repos.StackTemplate.Update(tmpl); err != nil {
			return nil, false, err
		}
	}
	return published, created, nil
}

// UnpublishTemplate godoc
// @Summary     Unpublish a stack template
// @Description Hide a template from regular users (template owner or admin). Use Template and Quick Deploy return 409 until the next publish. The version history stays.
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       id  path     string true "Template ID"
// @Success     200 {object} models.StackTemplate
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/templates/{id}/unpublish [post]
func (h *TemplateHandler) UnpublishTemplate(c *gin.Context) {
	tmpl := h.findManagedTemplate(c)
	if tmpl == nil {
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

// instantiateTemplateRequest is the body of POST /templates/:id/instantiate.
type instantiateTemplateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// ChartOverrides replace the default values of charts in the new
	// definition. Key: a published_charts[].id, a working copy charts[].id
	// (mapped by chart name) or a chart name. Keys for charts that are not in
	// the published snapshot are ignored.
	ChartOverrides map[string]string `json:"chart_overrides"`
}

// InstantiateTemplate godoc
// @Summary     Instantiate a template
// @Description Create a StackDefinition and ChartConfigs from the latest published snapshot of a template (not the working copy). Definition names are unique per owner.
// @Description A template that is not published, or has no snapshot, gives 409 "Template has no published version".
// @Description chart_overrides replace chart default values; keys are published_charts[].id, working copy charts[].id (mapped by chart name) or chart names. Keys for charts not in the published snapshot are ignored.
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       id   path     string                true "Template ID"
// @Param       body body     instantiateTemplateRequest true "Definition name, description and optional chart_overrides (default values per chart)"
// @Success     201  {object} DefinitionWithChartsResponse
// @Failure     400  {object} map[string]string
// @Failure     401  {object} map[string]string
// @Failure     404  {object} map[string]string
// @Failure     409  {object} map[string]string "The caller already has a definition with this name, or the template has no published version"
// @Failure     500  {object} map[string]string
// @Router      /api/v1/templates/{id}/instantiate [post]
func (h *TemplateHandler) InstantiateTemplate(c *gin.Context) {
	id := c.Param("id")
	tmpl, err := h.templateRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityTemplate)
		c.JSON(status, gin.H{"error": message})
		return
	}

	var input instantiateTemplateRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}
	if input.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Name is required"})
		return
	}

	// Use the latest published snapshot, never the working copy.
	release, err := usableTemplateRelease(c.Request.Context(), tmpl, h.versionRepo, h.chartRepo)
	if err != nil {
		if errors.Is(err, errNoPublishedVersion) {
			c.JSON(http.StatusConflict, gin.H{"error": msgNoPublishedVersion})
			return
		}
		slog.Error("failed to read published template version", "template_id", tmpl.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	overrides, err := resolveChartOverrides(input.ChartOverrides, release.Charts, func() ([]models.TemplateChartConfig, error) {
		return h.chartRepo.ListByTemplate(tmpl.ID)
	})
	if err != nil {
		slog.Error("failed to resolve chart overrides", "template_id", tmpl.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	releaseCharts := make([]models.TemplateChartConfig, len(release.Charts))
	copy(releaseCharts, release.Charts)
	for i := range releaseCharts {
		if values, ok := overrides[releaseCharts[i].ChartName]; ok {
			releaseCharts[i].DefaultValues = values
		}
	}

	now := time.Now().UTC()
	def := &models.StackDefinition{
		ID:                    uuid.New().String(),
		Name:                  input.Name,
		Description:           input.Description,
		OwnerID:               middleware.GetUserIDFromContext(c),
		SourceTemplateID:      tmpl.ID,
		SourceTemplateVersion: release.Version.Version,
		DefaultBranch:         release.Snapshot.Template.DefaultBranch,
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	if h.definitionRepo != nil && respondDefinitionNameTaken(c, h.definitionRepo, def.OwnerID, def.Name, "") {
		return
	}

	var chartConfigs []models.ChartConfig
	txErr := h.txRunner.RunInTx(func(repos database.TxRepos) error {
		if err := repos.StackDefinition.Create(def); err != nil {
			return err
		}
		ccs, copyErr := copyTemplateChartsToDefinitionTx(releaseCharts, def.ID, now, repos)
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

	c.JSON(http.StatusCreated, DefinitionWithChartsResponse{
		StackDefinition: *def,
		Charts:          chartConfigs,
	})
}

// copyTemplateChartsToDefinitionTx creates definition chart configs from the
// given (released) template charts within a transaction.
func copyTemplateChartsToDefinitionTx(templateCharts []models.TemplateChartConfig, defID string, now time.Time, repos database.TxRepos) ([]models.ChartConfig, error) {
	chartConfigs := make([]models.ChartConfig, 0, len(templateCharts))
	for _, tc := range templateCharts {
		cc := models.ChartConfig{
			ID:                uuid.New().String(),
			StackDefinitionID: defID,
			ChartName:         tc.ChartName,
			RepositoryURL:     tc.RepositoryURL,
			SourceRepoURL:     tc.SourceRepoURL,
			BuildPipelineID:   tc.BuildPipelineID,
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

	c.JSON(http.StatusCreated, clone)
}
