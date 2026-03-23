package handlers

import (
	"encoding/json"
	"net/http"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// TemplateVersionHandler handles template version endpoints.
type TemplateVersionHandler struct {
	versionRepo  models.TemplateVersionRepository
	templateRepo models.StackTemplateRepository
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
// @Description List all version snapshots for a template, ordered newest first
// @Tags        templates
// @Produce     json
// @Param       id  path     string true "Template ID"
// @Success     200 {array}  models.TemplateVersion
// @Failure     400 {object} map[string]string
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
		status, message := mapError(err, "Template")
		c.JSON(status, gin.H{"error": message})
		return
	}

	versions, err := h.versionRepo.ListByTemplate(c.Request.Context(), id)
	if err != nil {
		status, message := mapError(err, "Template version")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, versions)
}

// versionDetailResponse is the response for GetVersion, including the parsed snapshot.
type versionDetailResponse struct {
	ID            string                  `json:"id"`
	TemplateID    string                  `json:"template_id"`
	Version       string                  `json:"version"`
	ChangeSummary string                  `json:"change_summary"`
	CreatedBy     string                  `json:"created_by"`
	CreatedAt     string                  `json:"created_at"`
	Snapshot      models.TemplateSnapshot `json:"snapshot"`
}

// GetVersion godoc
// @Summary     Get a template version
// @Description Get a specific template version with its parsed snapshot
// @Tags        templates
// @Produce     json
// @Param       id        path     string true "Template ID"
// @Param       versionId path     string true "Version ID"
// @Success     200       {object} versionDetailResponse
// @Failure     400       {object} map[string]string
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

	version, err := h.versionRepo.GetByID(c.Request.Context(), versionID)
	if err != nil {
		status, message := mapError(err, "Template version")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Verify the version belongs to the requested template.
	if version.TemplateID != templateID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Template version not found"})
		return
	}

	var snapshot models.TemplateSnapshot
	if err := json.Unmarshal([]byte(version.Snapshot), &snapshot); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":             version.ID,
		"template_id":    version.TemplateID,
		"version":        version.Version,
		"change_summary": version.ChangeSummary,
		"created_by":     version.CreatedBy,
		"created_at":     version.CreatedAt,
		"snapshot":       snapshot,
	})
}

// chartDiffEntry describes the difference for a single chart between two versions.
type chartDiffEntry struct {
	ChartName      string `json:"chart_name"`
	LeftValues     string `json:"left_values,omitempty"`
	RightValues    string `json:"right_values,omitempty"`
	HasDifferences bool   `json:"has_differences"`
	ChangeType     string `json:"change_type"` // "added", "removed", "modified", "unchanged"
}

// DiffVersions godoc
// @Summary     Compare two template versions
// @Description Compare two template version snapshots side by side
// @Tags        templates
// @Produce     json
// @Param       id   path     string true "Template ID"
// @Param       left  query   string true "Left version ID"
// @Param       right query   string true "Right version ID"
// @Success     200 {object} map[string]interface{}
// @Failure     400 {object} map[string]string
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

	left, err := h.versionRepo.GetByID(c.Request.Context(), v1ID)
	if err != nil {
		status, message := mapError(err, "Template version")
		c.JSON(status, gin.H{"error": message})
		return
	}
	if left.TemplateID != templateID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Template version not found"})
		return
	}

	right, err := h.versionRepo.GetByID(c.Request.Context(), v2ID)
	if err != nil {
		status, message := mapError(err, "Template version")
		c.JSON(status, gin.H{"error": message})
		return
	}
	if right.TemplateID != templateID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Template version not found"})
		return
	}

	var leftSnapshot, rightSnapshot models.TemplateSnapshot
	if err := json.Unmarshal([]byte(left.Snapshot), &leftSnapshot); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}
	if err := json.Unmarshal([]byte(right.Snapshot), &rightSnapshot); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	chartDiffs := computeChartDiffs(leftSnapshot.Charts, rightSnapshot.Charts)

	c.JSON(http.StatusOK, gin.H{
		"left": gin.H{
			"version":  left.Version,
			"snapshot": leftSnapshot,
		},
		"right": gin.H{
			"version":  right.Version,
			"snapshot": rightSnapshot,
		},
		"chart_diffs": chartDiffs,
	})
}

// computeChartDiffs compares two sets of chart snapshots and returns per-chart diffs.
func computeChartDiffs(leftCharts, rightCharts []models.TemplateChartSnapshotData) []chartDiffEntry {
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
				ChartName:      ch.ChartName,
				LeftValues:     ch.DefaultValues,
				HasDifferences: true,
				ChangeType:     "removed",
			})
			continue
		}
		hasDiff := ch.DefaultValues != rch.DefaultValues ||
			ch.LockedValues != rch.LockedValues ||
			ch.RepoURL != rch.RepoURL ||
			ch.IsRequired != rch.IsRequired ||
			ch.SortOrder != rch.SortOrder
		changeType := "unchanged"
		if hasDiff {
			changeType = "modified"
		}
		diffs = append(diffs, chartDiffEntry{
			ChartName:      ch.ChartName,
			LeftValues:     ch.DefaultValues,
			RightValues:    rch.DefaultValues,
			HasDifferences: hasDiff,
			ChangeType:     changeType,
		})
	}

	for _, ch := range rightCharts {
		if seen[ch.ChartName] {
			continue
		}
		diffs = append(diffs, chartDiffEntry{
			ChartName:      ch.ChartName,
			RightValues:    ch.DefaultValues,
			HasDifferences: true,
			ChangeType:     "added",
		})
	}

	return diffs
}
