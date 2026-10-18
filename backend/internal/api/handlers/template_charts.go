package handlers

import (
	"net/http"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Template chart handler message constants.
const (
	entityTemplateChart = "Template chart"
)


// AddTemplateChart godoc
// @Summary     Add a chart to a template
// @Description Add a new chart configuration to the working copy (draft) of a stack template. Users get the change after the next publish. Template owner or admin only.
// @Tags        template-charts
// @Accept      json
// @Produce     json
// @Param       id    path     string                      true "Template ID"
// @Param       chart body     models.TemplateChartConfig   true "Chart config"
// @Success     201   {object} models.TemplateChartConfig
// @Failure     400   {object} map[string]string
// @Failure     401   {object} map[string]string
// @Failure     403   {object} map[string]string
// @Failure     404   {object} map[string]string
// @Failure     500   {object} map[string]string
// @Router      /api/v1/templates/{id}/charts [post]
func (h *TemplateHandler) AddTemplateChart(c *gin.Context) {
	// Verify the template exists and the caller is its owner or an admin.
	tmpl := h.findManagedTemplate(c)
	if tmpl == nil {
		return
	}
	templateID := tmpl.ID

	var chart models.TemplateChartConfig
	if err := c.ShouldBindJSON(&chart); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	chart.ID = uuid.New().String()
	chart.StackTemplateID = templateID
	chart.CreatedAt = time.Now().UTC()

	if err := chart.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.chartRepo.Create(&chart); err != nil {
		status, message := mapError(err, entityTemplateChart)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusCreated, chart)
}

// UpdateTemplateChart godoc
// @Summary     Update a template chart
// @Description Update a chart configuration in the working copy (draft) of a stack template. Users get the change after the next publish. Template owner or admin only.
// @Tags        template-charts
// @Accept      json
// @Produce     json
// @Param       id      path     string                      true "Template ID"
// @Param       chartId path     string                      true "Chart config ID"
// @Param       chart   body     models.TemplateChartConfig   true "Updated chart config"
// @Success     200     {object} models.TemplateChartConfig
// @Failure     400     {object} map[string]string
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Failure     404     {object} map[string]string
// @Failure     500     {object} map[string]string
// @Router      /api/v1/templates/{id}/charts/{chartId} [put]
func (h *TemplateHandler) UpdateTemplateChart(c *gin.Context) {
	existing := h.findManagedTemplateChart(c)
	if existing == nil {
		return
	}

	var update models.TemplateChartConfig
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	existing.ChartName = update.ChartName
	existing.RepositoryURL = update.RepositoryURL
	existing.SourceRepoURL = update.SourceRepoURL
	existing.BuildPipelineID = update.BuildPipelineID
	existing.ChartPath = update.ChartPath
	existing.ChartVersion = update.ChartVersion
	existing.DefaultValues = update.DefaultValues
	existing.LockedValues = update.LockedValues
	existing.DeployOrder = update.DeployOrder
	existing.Required = update.Required

	if err := existing.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.chartRepo.Update(existing); err != nil {
		status, message := mapError(err, entityTemplateChart)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, existing)
}

// DeleteTemplateChart godoc
// @Summary     Delete a template chart
// @Description Remove a chart configuration from the working copy (draft) of a stack template. Users get the change after the next publish. Template owner or admin only.
// @Tags        template-charts
// @Produce     json
// @Param       id      path     string true "Template ID"
// @Param       chartId path     string true "Chart config ID"
// @Success     204     "No Content"
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Failure     404     {object} map[string]string
// @Failure     500     {object} map[string]string
// @Router      /api/v1/templates/{id}/charts/{chartId} [delete]
func (h *TemplateHandler) DeleteTemplateChart(c *gin.Context) {
	chart := h.findManagedTemplateChart(c)
	if chart == nil {
		return
	}

	if err := h.chartRepo.Delete(chart.ID); err != nil {
		status, message := mapError(err, entityTemplateChart)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}

// findManagedTemplateChart loads the :chartId chart of the :id template and
// checks that the caller is the template owner or an admin. A chart of
// another template gives 404. It writes the error response and returns nil on
// failure.
func (h *TemplateHandler) findManagedTemplateChart(c *gin.Context) *models.TemplateChartConfig {
	tmpl := h.findManagedTemplate(c)
	if tmpl == nil {
		return nil
	}
	chart, err := h.chartRepo.FindByID(c.Param("chartId"))
	if err != nil {
		status, message := mapError(err, entityTemplateChart)
		c.JSON(status, gin.H{"error": message})
		return nil
	}
	if chart.StackTemplateID != tmpl.ID {
		c.JSON(http.StatusNotFound, gin.H{"error": entityTemplateChart + " not found"})
		return nil
	}
	return chart
}
