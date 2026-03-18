package handlers

import (
	"net/http"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// DefinitionHandler handles stack definition and chart config endpoints.
type DefinitionHandler struct {
	definitionRepo models.StackDefinitionRepository
	chartRepo      models.ChartConfigRepository
	instanceRepo   models.StackInstanceRepository
}

// NewDefinitionHandler creates a new DefinitionHandler.
func NewDefinitionHandler(
	definitionRepo models.StackDefinitionRepository,
	chartRepo models.ChartConfigRepository,
	instanceRepo models.StackInstanceRepository,
) *DefinitionHandler {
	return &DefinitionHandler{
		definitionRepo: definitionRepo,
		chartRepo:      chartRepo,
		instanceRepo:   instanceRepo,
	}
}

// ListDefinitions godoc
// @Summary     List stack definitions
// @Description List all stack definitions
// @Tags        stack-definitions
// @Produce     json
// @Success     200 {array}  models.StackDefinition
// @Failure     500 {object} map[string]string
// @Router      /api/v1/stack-definitions [get]
func (h *DefinitionHandler) ListDefinitions(c *gin.Context) {
	defs, err := h.definitionRepo.List()
	if err != nil {
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, defs)
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
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
		status, message := mapError(err, "Stack definition")
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "Definition ID is required"})
		return
	}

	def, err := h.definitionRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartRepo.ListByDefinition(id)
	if err != nil {
		status, message := mapError(err, "Chart configs")
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "Definition ID is required"})
		return
	}

	existing, err := h.definitionRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	var update models.StackDefinition
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
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
		status, message := mapError(err, "Stack definition")
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "Definition ID is required"})
		return
	}

	// Check for running instances.
	if h.instanceRepo != nil {
		instances, err := h.instanceRepo.List()
		if err == nil {
			for _, inst := range instances {
				if inst.StackDefinitionID == id && (inst.Status == models.StackStatusRunning || inst.Status == models.StackStatusDeploying) {
					c.JSON(http.StatusConflict, gin.H{"error": "Cannot delete definition: running instances exist"})
					return
				}
			}
		}
	}

	if err := h.definitionRepo.Delete(id); err != nil {
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}
