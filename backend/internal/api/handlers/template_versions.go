package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// Template version handler message constants.
const (
	entityTemplateVersion = "Template version"
)


// workingCopyVersionID is the version ID that selects the working copy
// (draft) of a template in the version diff.
const workingCopyVersionID = "working"

// TemplateVersionHandler handles template version endpoints.
type TemplateVersionHandler struct {
	versionRepo  models.TemplateVersionRepository
	templateRepo models.StackTemplateRepository
	chartRepo    models.TemplateChartConfigRepository
	userRepo     models.UserRepository
}

// WithTemplateCharts attaches the template chart repository. The version
// diff needs it to compare the working copy (left/right=working). Returns h
// for chaining.
func (h *TemplateVersionHandler) WithTemplateCharts(repo models.TemplateChartConfigRepository) *TemplateVersionHandler {
	h.chartRepo = repo
	return h
}

// WithUserRepo attaches the user repository. Version responses then include
// created_by_username. Returns h for chaining.
func (h *TemplateVersionHandler) WithUserRepo(repo models.UserRepository) *TemplateVersionHandler {
	h.userRepo = repo
	return h
}

// usernames returns user ID -> username for the given IDs with one batch
// query. Lookup errors are logged; the result is then empty.
func (h *TemplateVersionHandler) usernames(ids ...string) map[string]string {
	out := make(map[string]string)
	if h.userRepo == nil {
		return out
	}
	set := make(map[string]struct{}, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := set[id]; ok {
			continue
		}
		set[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return out
	}
	users, err := h.userRepo.FindByIDs(unique)
	if err != nil {
		slog.Warn("failed to batch-fetch template version authors", "error", err)
		return out
	}
	for id, u := range users {
		if u != nil {
			out[id] = u.Username
		}
	}
	return out
}

// templateVersionListItem is one entry of GET /templates/:id/versions.
type templateVersionListItem struct {
	models.TemplateVersion
	CreatedByUsername string `json:"created_by_username,omitempty"`
}

// NewTemplateVersionHandler creates a new TemplateVersionHandler.
func NewTemplateVersionHandler(
	versionRepo models.TemplateVersionRepository,
	templateRepo models.StackTemplateRepository,
) *TemplateVersionHandler {
	return &TemplateVersionHandler{
		versionRepo:  versionRepo,
		templateRepo: templateRepo,
	}
}

// ListVersions godoc
// @Summary     List template versions
// @Description List all version snapshots for a template, ordered newest first. The first entry is the published version that users get. created_by_username is the author's username (omitted when unknown).
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       id  path     string true "Template ID"
// @Success     200 {array}  templateVersionListItem
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/templates/{id}/versions [get]
func (h *TemplateVersionHandler) ListVersions(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Template ID is required"})
		return
	}

	// Verify template exists.
	if _, err := h.templateRepo.FindByID(id); err != nil {
		status, message := mapError(err, entityTemplate)
		c.JSON(status, gin.H{"error": message})
		return
	}

	versions, err := h.versionRepo.ListByTemplate(c.Request.Context(), id)
	if err != nil {
		status, message := mapError(err, entityTemplateVersion)
		c.JSON(status, gin.H{"error": message})
		return
	}

	authorIDs := make([]string, 0, len(versions))
	for _, v := range versions {
		authorIDs = append(authorIDs, v.CreatedBy)
	}
	names := h.usernames(authorIDs...)

	items := make([]templateVersionListItem, 0, len(versions))
	for _, v := range versions {
		items = append(items, templateVersionListItem{TemplateVersion: v, CreatedByUsername: names[v.CreatedBy]})
	}
	c.JSON(http.StatusOK, items)
}

// versionDetailResponse is the response for GetVersion, including the parsed snapshot.
type versionDetailResponse struct {
	ID            string `json:"id"`
	TemplateID    string `json:"template_id"`
	Version       string `json:"version"`
	ChangeSummary string `json:"change_summary"`
	CreatedBy     string `json:"created_by"`
	// CreatedByUsername is the author's username (omitted when unknown).
	CreatedByUsername string                  `json:"created_by_username,omitempty"`
	CreatedAt         time.Time               `json:"created_at"`
	Snapshot          models.TemplateSnapshot `json:"snapshot"`
}

// GetVersion godoc
// @Summary     Get a template version
// @Description Get a specific template version with its parsed snapshot and created_by_username
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       id        path     string true "Template ID"
// @Param       versionId path     string true "Version ID"
// @Success     200       {object} versionDetailResponse
// @Failure     400       {object} map[string]string
// @Failure     401       {object} map[string]string
// @Failure     404       {object} map[string]string
// @Failure     500       {object} map[string]string
// @Router      /api/v1/templates/{id}/versions/{versionId} [get]
func (h *TemplateVersionHandler) GetVersion(c *gin.Context) {
	templateID := c.Param("id")
	versionID := c.Param("versionId")
	if templateID == "" || versionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Template ID and Version ID are required"})
		return
	}

	version, err := h.versionRepo.GetByID(c.Request.Context(), templateID, versionID)
	if err != nil {
		status, message := mapError(err, entityTemplateVersion)
		c.JSON(status, gin.H{"error": message})
		return
	}

	var snapshot models.TemplateSnapshot
	if err := json.Unmarshal([]byte(version.Snapshot), &snapshot); err != nil {
		slog.Error("Failed to unmarshal template version snapshot", "error", err, "version_id", version.ID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	c.JSON(http.StatusOK, versionDetailResponse{
		ID:                version.ID,
		TemplateID:        version.TemplateID,
		Version:           version.Version,
		ChangeSummary:     version.ChangeSummary,
		CreatedBy:         version.CreatedBy,
		CreatedByUsername: h.usernames(version.CreatedBy)[version.CreatedBy],
		CreatedAt:         version.CreatedAt,
		Snapshot:          snapshot,
	})
}

// chartDiffEntry describes the difference for a single chart between two versions.
type chartDiffEntry struct {
	ChartName      string `json:"chart_name"`
	LeftValues     string `json:"left_values,omitempty"`
	RightValues    string `json:"right_values,omitempty"`
	LeftRepoURL    string `json:"left_repo_url,omitempty"`
	RightRepoURL   string `json:"right_repo_url,omitempty"`
	LeftLocked     string `json:"left_locked,omitempty"`
	RightLocked    string `json:"right_locked,omitempty"`
	LeftRequired   bool   `json:"left_required,omitempty"`
	RightRequired  bool   `json:"right_required,omitempty"`
	LeftSortOrder  int    `json:"left_sort_order,omitempty"`
	RightSortOrder int    `json:"right_sort_order,omitempty"`
	// Chart source fields; compared only when both sides store them
	// (snapshot schema version 1 or the working copy).
	LeftChartVersion  string `json:"left_chart_version,omitempty"`
	RightChartVersion string `json:"right_chart_version,omitempty"`
	LeftChartPath     string `json:"left_chart_path,omitempty"`
	RightChartPath    string `json:"right_chart_path,omitempty"`
	HasDifferences    bool   `json:"has_differences"`
	ChangeType        string `json:"change_type"` // "added", "removed", "modified", "unchanged"
}

// versionDiffSide is one side of the version diff. version is the version
// string (unchanged wire format). For the working copy id is "working",
// is_working_copy is true, created_at is the last update of the template and
// the created_by fields are empty.
type versionDiffSide struct {
	ID                string                  `json:"id"`
	Version           string                  `json:"version"`
	ChangeSummary     string                  `json:"change_summary,omitempty"`
	CreatedBy         string                  `json:"created_by,omitempty"`
	CreatedByUsername string                  `json:"created_by_username,omitempty"`
	CreatedAt         time.Time               `json:"created_at"`
	IsWorkingCopy     bool                    `json:"is_working_copy"`
	Snapshot          models.TemplateSnapshot `json:"snapshot"`
}

// templateFieldDiff is one template field that differs between two versions.
// Field is the JSON name of the field in the snapshot template data (name,
// description, category, default_branch or version).
type templateFieldDiff struct {
	Field string `json:"field"`
	Left  string `json:"left"`
	Right string `json:"right"`
}

// versionDiffResponse is the response of GET /templates/:id/versions/diff.
// TemplateDiffs lists only the changed template fields (an empty list when
// all are equal); the publish state is not content and is not compared.
type versionDiffResponse struct {
	Left          versionDiffSide     `json:"left"`
	Right         versionDiffSide     `json:"right"`
	TemplateDiffs []templateFieldDiff `json:"template_diffs"`
	ChartDiffs    []chartDiffEntry    `json:"chart_diffs"`
}

// computeTemplateFieldDiffs returns the template fields that differ between
// two snapshots, in a fixed order. It compares the same fields as
// models.SameTemplateContent. Never nil.
func computeTemplateFieldDiffs(left, right models.TemplateSnapshotData) []templateFieldDiff {
	fields := []struct {
		name        string
		left, right string
	}{
		{"name", left.Name, right.Name},
		{"description", left.Description, right.Description},
		{"category", left.Category, right.Category},
		{"default_branch", left.DefaultBranch, right.DefaultBranch},
		{"version", left.Version, right.Version},
	}
	diffs := make([]templateFieldDiff, 0, len(fields))
	for _, f := range fields {
		if f.left != f.right {
			diffs = append(diffs, templateFieldDiff{Field: f.name, Left: f.left, Right: f.right})
		}
	}
	return diffs
}

// DiffVersions godoc
// @Summary     Compare two template versions
// @Description Compare two template version snapshots side by side. Either side may be "working" to compare the working copy (draft), for example left=<latest version ID>&right=working shows the unpublished changes.
// @Description Only the template owner and admins can compare the working copy (others get 403).
// @Description left.version and right.version are version strings; the other side fields (id, created_by, created_by_username, created_at, is_working_copy) describe the side.
// @Description template_diffs lists the changed template fields (name, description, category, default_branch, version) with the left and right value; chart_diffs compares the charts.
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       id    path    string true "Template ID"
// @Param       left  query   string true "Left version ID, or \"working\" for the working copy"
// @Param       right query   string true "Right version ID, or \"working\" for the working copy"
// @Success     200 {object} versionDiffResponse
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/templates/{id}/versions/diff [get]
func (h *TemplateVersionHandler) DiffVersions(c *gin.Context) {
	templateID := c.Param("id")
	v1ID := c.Query("left")
	v2ID := c.Query("right")
	if templateID == "" || v1ID == "" || v2ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Template ID, left version ID, and right version ID are required"})
		return
	}

	left, ok := h.diffSide(c, templateID, v1ID)
	if !ok {
		return
	}
	right, ok := h.diffSide(c, templateID, v2ID)
	if !ok {
		return
	}

	names := h.usernames(left.CreatedBy, right.CreatedBy)
	left.CreatedByUsername = names[left.CreatedBy]
	right.CreatedByUsername = names[right.CreatedBy]

	compareSource := left.Snapshot.SchemaVersion >= 1 && right.Snapshot.SchemaVersion >= 1
	chartDiffs := computeChartDiffs(left.Snapshot.Charts, right.Snapshot.Charts, compareSource)
	if chartDiffs == nil {
		chartDiffs = []chartDiffEntry{}
	}

	c.JSON(http.StatusOK, versionDiffResponse{
		Left:          *left,
		Right:         *right,
		TemplateDiffs: computeTemplateFieldDiffs(left.Snapshot.Template, right.Snapshot.Template),
		ChartDiffs:    chartDiffs,
	})
}

// diffSide loads one side of the diff: a snapshot by version ID, or the
// working copy for "working". It writes the error response and returns false
// on failure.
func (h *TemplateVersionHandler) diffSide(c *gin.Context, templateID, versionID string) (*versionDiffSide, bool) {
	if versionID == workingCopyVersionID {
		if h.chartRepo == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Working copy comparison is not available"})
			return nil, false
		}
		tmpl, err := h.templateRepo.FindByID(templateID)
		if err != nil {
			status, message := mapError(err, entityTemplate)
			c.JSON(status, gin.H{"error": message})
			return nil, false
		}
		// The working copy is visible only to the owner and admins.
		if !canManageTemplate(c, tmpl) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Only the template owner or an admin can see the working copy"})
			return nil, false
		}
		charts, err := h.chartRepo.ListByTemplate(templateID)
		if err != nil {
			status, message := mapError(err, entityTemplateCharts)
			c.JSON(status, gin.H{"error": message})
			return nil, false
		}
		return &versionDiffSide{
			ID:            workingCopyVersionID,
			Version:       tmpl.Version,
			CreatedAt:     tmpl.UpdatedAt,
			IsWorkingCopy: true,
			Snapshot:      models.NewTemplateSnapshot(tmpl, charts),
		}, true
	}

	version, err := h.versionRepo.GetByID(c.Request.Context(), templateID, versionID)
	if err != nil {
		status, message := mapError(err, entityTemplateVersion)
		c.JSON(status, gin.H{"error": message})
		return nil, false
	}
	var snapshot models.TemplateSnapshot
	if err := json.Unmarshal([]byte(version.Snapshot), &snapshot); err != nil {
		slog.Error("Failed to unmarshal template version snapshot", "error", err, "version_id", version.ID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return nil, false
	}
	return &versionDiffSide{
		ID:            version.ID,
		Version:       version.Version,
		ChangeSummary: version.ChangeSummary,
		CreatedBy:     version.CreatedBy,
		CreatedAt:     version.CreatedAt,
		Snapshot:      snapshot,
	}, true
}

// computeChartDiffs compares two sets of chart snapshots and returns per-chart
// diffs. compareSource also compares chart_version and chart_path (only when
// both sides store them).
func computeChartDiffs(leftCharts, rightCharts []models.TemplateChartSnapshotData, compareSource bool) []chartDiffEntry {
	leftMap := make(map[string]models.TemplateChartSnapshotData, len(leftCharts))
	for _, ch := range leftCharts {
		leftMap[ch.ChartName] = ch
	}
	rightMap := make(map[string]models.TemplateChartSnapshotData, len(rightCharts))
	for _, ch := range rightCharts {
		rightMap[ch.ChartName] = ch
	}

	// Track all chart names.
	seen := make(map[string]bool)
	var diffs []chartDiffEntry

	for _, ch := range leftCharts {
		seen[ch.ChartName] = true
		rch, inRight := rightMap[ch.ChartName]
		if !inRight {
			diffs = append(diffs, chartDiffEntry{
				ChartName:     ch.ChartName,
				LeftValues:    ch.DefaultValues,
				LeftRepoURL:   ch.RepoURL,
				LeftLocked:    ch.LockedValues,
				LeftRequired:  ch.IsRequired,
				LeftSortOrder: ch.SortOrder,
				HasDifferences: true,
				ChangeType:     "removed",
			})
			continue
		}
		hasDiff := models.NormalizeValues(ch.DefaultValues) != models.NormalizeValues(rch.DefaultValues) ||
			models.NormalizeValues(ch.LockedValues) != models.NormalizeValues(rch.LockedValues) ||
			ch.RepoURL != rch.RepoURL ||
			ch.IsRequired != rch.IsRequired ||
			ch.SortOrder != rch.SortOrder
		if compareSource && (ch.ChartVersion != rch.ChartVersion || ch.ChartPath != rch.ChartPath) {
			hasDiff = true
		}
		changeType := "unchanged"
		if hasDiff {
			changeType = "modified"
		}
		diffs = append(diffs, chartDiffEntry{
			ChartName:         ch.ChartName,
			LeftValues:        ch.DefaultValues,
			RightValues:       rch.DefaultValues,
			LeftRepoURL:       ch.RepoURL,
			RightRepoURL:      rch.RepoURL,
			LeftLocked:        ch.LockedValues,
			RightLocked:       rch.LockedValues,
			LeftRequired:      ch.IsRequired,
			RightRequired:     rch.IsRequired,
			LeftSortOrder:     ch.SortOrder,
			RightSortOrder:    rch.SortOrder,
			LeftChartVersion:  ch.ChartVersion,
			RightChartVersion: rch.ChartVersion,
			LeftChartPath:     ch.ChartPath,
			RightChartPath:    rch.ChartPath,
			HasDifferences:    hasDiff,
			ChangeType:        changeType,
		})
	}

	for _, ch := range rightCharts {
		if seen[ch.ChartName] {
			continue
		}
		diffs = append(diffs, chartDiffEntry{
			ChartName:      ch.ChartName,
			RightValues:    ch.DefaultValues,
			RightRepoURL:   ch.RepoURL,
			RightLocked:    ch.LockedValues,
			RightRequired:  ch.IsRequired,
			RightSortOrder: ch.SortOrder,
			HasDifferences: true,
			ChangeType:     "added",
		})
	}

	// Sort by chart name for deterministic output.
	sort.Slice(diffs, func(i, j int) bool {
		return diffs[i].ChartName < diffs[j].ChartName
	})

	return diffs
}
