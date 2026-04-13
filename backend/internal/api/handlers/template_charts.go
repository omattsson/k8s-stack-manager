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
// @Description Add a new chart configuration to a stack template
// @Tags        template-charts
// @Accept      json
// @Produce     json
// @Param       id    path     string                      true "Template ID"
// @Param       chart body     models.TemplateChartConfig   true "Chart config"
// @Success     201   {object} models.TemplateChartConfig
// @Failure     400   {object} map[string]string
// @Failure     404   {object} map[string]string
// @Router      /api/v1/templates/{id}/charts [post]
func (h *TemplateHandler) AddTemplateChart(c *gin.Context) {
	templateID := c.Param("id")

	// Verify template exists.
	if _, err := h.templateRepo.FindByID(templateID); err != nil {
		status, message := mapError(err, entityTemplate)
		c.JSON(status, gin.H{"error": message})
		return
	}

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
// @Description Update a chart configuration within a stack template
// @Tags        template-charts
// @Accept      json
// @Produce     json
// @Param       id      path     string                      true "Template ID"
// @Param       chartId path     string                      true "Chart config ID"
// @Param       chart   body     models.TemplateChartConfig   true "Updated chart config"
// @Success     200     {object} models.TemplateChartConfig
// @Failure     400     {object} map[string]string
// @Failure     404     {object} map[string]string
// @Router      /api/v1/templates/{id}/charts/{chartId} [put]
func (h *TemplateHandler) UpdateTemplateChart(c *gin.Context) {
	chartID := c.Param("chartId")

	existing, err := h.chartRepo.FindByID(chartID)
	if err != nil {
		status, message := mapError(err, entityTemplateChart)
		c.JSON(status, gin.H{"error": message})
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
// @Description Remove a chart configuration from a stack template
// @Tags        template-charts
// @Produce     json
// @Param       id      path     string true "Template ID"
// @Param       chartId path     string true "Chart config ID"
// @Success     204     "No Content"
// @Failure     404     {object} map[string]string
// @Router      /api/v1/templates/{id}/charts/{chartId} [delete]
func (h *TemplateHandler) DeleteTemplateChart(c *gin.Context) {
	chartID := c.Param("chartId")

	if err := h.chartRepo.Delete(chartID); err != nil {
		status, message := mapError(err, entityTemplateChart)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}
