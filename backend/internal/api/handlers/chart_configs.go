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
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	var chart models.ChartConfig
	if err := c.ShouldBindJSON(&chart); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
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
		status, message := mapError(err, "Chart config")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusCreated, chart)
}

// UpdateChartConfig godoc
// @Summary     Update a chart config
// @Description Update a chart configuration within a stack definition
// @Tags        chart-configs
// @Accept      json
// @Produce     json
// @Param       id      path     string            true "Definition ID"
// @Param       chartId path     string            true "Chart config ID"
// @Param       chart   body     models.ChartConfig true "Updated chart config"
// @Success     200     {object} models.ChartConfig
// @Failure     400     {object} map[string]string
// @Failure     404     {object} map[string]string
// @Router      /api/v1/stack-definitions/{id}/charts/{chartId} [put]
func (h *DefinitionHandler) UpdateChartConfig(c *gin.Context) {
	chartID := c.Param("chartId")

	existing, err := h.chartRepo.FindByID(chartID)
	if err != nil {
		status, message := mapError(err, "Chart config")
		c.JSON(status, gin.H{"error": message})
		return
	}

	var update models.ChartConfig
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	existing.ChartName = update.ChartName
	existing.RepositoryURL = update.RepositoryURL
	existing.SourceRepoURL = update.SourceRepoURL
	existing.ChartPath = update.ChartPath
	existing.ChartVersion = update.ChartVersion
	existing.DefaultValues = update.DefaultValues
	existing.DeployOrder = update.DeployOrder

	if err := existing.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.chartRepo.Update(existing); err != nil {
		status, message := mapError(err, "Chart config")
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
	defID := c.Param("id")
	chartID := c.Param("chartId")

	// Look up the chart config to get its ChartName.
	chart, err := h.chartRepo.FindByID(chartID)
	if err != nil {
		status, message := mapError(err, "Chart config")
		c.JSON(status, gin.H{"error": message})
		return
	}

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
		status, message := mapError(err, "Chart config")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}
