package handlers

import (
	"net/http"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// AddChartConfig godoc
// @Summary     Add a chart to a definition
// @Description Add a new chart configuration to a stack definition
// @Tags        chart-configs
// @Accept      json
// @Produce     json
// @Param       id    path     string            true "Definition ID"
// @Param       chart body     models.ChartConfig true "Chart config"
// @Success     201   {object} models.ChartConfig
// @Failure     400   {object} map[string]string
// @Failure     404   {object} map[string]string
// @Router      /api/v1/stack-definitions/{id}/charts [post]
func (h *DefinitionHandler) AddChartConfig(c *gin.Context) {
	defID := c.Param("id")

	// Verify definition exists.
	if _, err := h.definitionRepo.FindByID(defID); err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	var chart models.ChartConfig
	if err := c.ShouldBindJSON(&chart); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	chart.ID = uuid.New().String()
	chart.StackDefinitionID = defID
	chart.CreatedAt = time.Now().UTC()

	if err := chart.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.chartRepo.Create(&chart); err != nil {
		status, message := mapError(err, entityChartConfig)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusCreated, chart)
}

// GetChartConfig godoc
// @Summary     Get a chart config
// @Description Fetch a single chart configuration within a stack definition
// @Tags        chart-configs
// @Produce     json
// @Param       id      path     string             true "Definition ID"
// @Param       chartId path     string             true "Chart config ID"
// @Success     200     {object} models.ChartConfig
// @Failure     404     {object} map[string]string
// @Failure     500     {object} map[string]string
// @Router      /api/v1/stack-definitions/{id}/charts/{chartId} [get]
func (h *DefinitionHandler) GetChartConfig(c *gin.Context) {
	chart, ok := h.requireChartInDefinition(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, chart)
}

// requireChartInDefinition loads the chart by chartId and verifies it belongs
// to the stack definition identified by the `id` path param. On any failure it
// writes the appropriate error response (404 when the chart is missing or
// belongs to a different definition; 500 on repository errors via mapError)
// and returns (nil, false). Callers should return immediately when ok is
// false.
//
// The cross-definition mismatch is reported as a generic "Chart config not
// found" — exposing whether a given chart ID exists under a different
// definition would leak the existence of unrelated records.
func (h *DefinitionHandler) requireChartInDefinition(c *gin.Context) (*models.ChartConfig, bool) {
	defID := c.Param("id")
	chartID := c.Param("chartId")

	chart, err := h.chartRepo.FindByID(chartID)
	if err != nil {
		status, message := mapError(err, entityChartConfig)
		c.JSON(status, gin.H{"error": message})
		return nil, false
	}
	if chart.StackDefinitionID != defID {
		c.JSON(http.StatusNotFound, gin.H{"error": entityChartConfig + " not found"})
		return nil, false
	}
	return chart, true
}

// chartConfigUpdateRequest is the body type for PUT /charts/{chartId}.
//
// Fields are pointers so a JSON document with the key absent leaves the
// existing value untouched, while an explicitly present value replaces it.
// The contract is PATCH-like semantics for any client that sends a subset
// of fields: unmentioned fields (repository_url, chart_path,
// build_pipeline_id, deploy_order, ...) are preserved rather than wiped to
// the zero value on every PUT.
//
// Caveat: setting a string field to "" is treated as "replace with empty
// string", which then runs through models.ChartConfig.Validate(). Fields
// with non-empty validation (notably ChartName) will cause the request to
// fail with HTTP 400 — callers wanting to clear such fields cannot do so
// via this endpoint.
type chartConfigUpdateRequest struct {
	ChartName       *string `json:"chart_name,omitempty"`
	RepositoryURL   *string `json:"repository_url,omitempty"`
	SourceRepoURL   *string `json:"source_repo_url,omitempty"`
	BuildPipelineID *string `json:"build_pipeline_id,omitempty"`
	ChartPath       *string `json:"chart_path,omitempty"`
	ChartVersion    *string `json:"chart_version,omitempty"`
	DefaultValues   *string `json:"default_values,omitempty"`
	DeployOrder     *int    `json:"deploy_order,omitempty"`
}

// UpdateChartConfig godoc
// @Summary     Update a chart config
// @Description Partial-update a chart configuration within a stack definition.
// @Description Only fields present in the request body are modified; absent
// @Description fields are preserved from the existing record.
// @Tags        chart-configs
// @Accept      json
// @Produce     json
// @Param       id      path     string                   true "Definition ID"
// @Param       chartId path     string                   true "Chart config ID"
// @Param       chart   body     chartConfigUpdateRequest true "Updated chart config"
// @Success     200     {object} models.ChartConfig
// @Failure     400     {object} map[string]string
// @Failure     404     {object} map[string]string
// @Failure     500     {object} map[string]string
// @Router      /api/v1/stack-definitions/{id}/charts/{chartId} [put]
func (h *DefinitionHandler) UpdateChartConfig(c *gin.Context) {
	existing, ok := h.requireChartInDefinition(c)
	if !ok {
		return
	}

	var update chartConfigUpdateRequest
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	if update.ChartName != nil {
		existing.ChartName = *update.ChartName
	}
	if update.RepositoryURL != nil {
		existing.RepositoryURL = *update.RepositoryURL
	}
	if update.SourceRepoURL != nil {
		existing.SourceRepoURL = *update.SourceRepoURL
	}
	if update.BuildPipelineID != nil {
		existing.BuildPipelineID = *update.BuildPipelineID
	}
	if update.ChartPath != nil {
		existing.ChartPath = *update.ChartPath
	}
	if update.ChartVersion != nil {
		existing.ChartVersion = *update.ChartVersion
	}
	if update.DefaultValues != nil {
		existing.DefaultValues = *update.DefaultValues
	}
	if update.DeployOrder != nil {
		existing.DeployOrder = *update.DeployOrder
	}

	if err := existing.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.chartRepo.Update(existing); err != nil {
		status, message := mapError(err, entityChartConfig)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, existing)
}

// DeleteChartConfig godoc
// @Summary     Delete a chart config
// @Description Remove a chart configuration from a stack definition
// @Tags        chart-configs
// @Produce     json
// @Param       id      path     string true "Definition ID"
// @Param       chartId path     string true "Chart config ID"
// @Success     204     "No Content"
// @Failure     404     {object} map[string]string
// @Router      /api/v1/stack-definitions/{id}/charts/{chartId} [delete]
func (h *DefinitionHandler) DeleteChartConfig(c *gin.Context) {
	chart, ok := h.requireChartInDefinition(c)
	if !ok {
		return
	}
	defID := c.Param("id")
	chartID := c.Param("chartId")

	// Check if the definition was created from a template with a required chart.
	if defID != "" && h.templateChartRepo != nil {
		def, err := h.definitionRepo.FindByID(defID)
		if err == nil && def.SourceTemplateID != "" {
			templateCharts, err := h.templateChartRepo.ListByTemplate(def.SourceTemplateID)
			if err == nil {
				for _, tc := range templateCharts {
					if tc.ChartName == chart.ChartName && tc.Required {
						c.JSON(http.StatusConflict, gin.H{"error": "Cannot delete required chart: " + chart.ChartName})
						return
					}
				}
			}
		}
	}

	if err := h.chartRepo.Delete(chartID); err != nil {
		status, message := mapError(err, entityChartConfig)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}
