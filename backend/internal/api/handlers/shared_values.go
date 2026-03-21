package handlers

import (
	"log/slog"
	"net/http"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// SharedValuesHandler provides CRUD endpoints for cluster-scoped shared values.
type SharedValuesHandler struct {
	repo        models.SharedValuesRepository
	clusterRepo models.ClusterRepository
}

// NewSharedValuesHandler creates a new SharedValuesHandler with the given dependencies.
func NewSharedValuesHandler(repo models.SharedValuesRepository, clusterRepo models.ClusterRepository) *SharedValuesHandler {
	return &SharedValuesHandler{
		repo:        repo,
		clusterRepo: clusterRepo,
	}
}

// ListSharedValues godoc
// @Summary      List shared values for a cluster
// @Description  Returns all shared values for the specified cluster, sorted by priority (lowest first).
// @Tags         shared-values
// @Produce      json
// @Param        id  path     string true "Cluster ID"
// @Success      200 {array}  models.SharedValues
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/clusters/{id}/shared-values [get]
// @Security     BearerAuth
func (h *SharedValuesHandler) ListSharedValues(c *gin.Context) {
	clusterID := c.Param("id")

	if _, err := h.clusterRepo.FindByID(clusterID); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	values, err := h.repo.ListByCluster(clusterID)
	if err != nil {
		status, message := mapError(err, "Shared values")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, values)
}

// CreateSharedValues godoc
// @Summary      Create shared values for a cluster
// @Description  Creates a new shared values entry for the specified cluster.
// @Tags         shared-values
// @Accept       json
// @Produce      json
// @Param        id   path     string              true "Cluster ID"
// @Param        body body     models.SharedValues true "Shared values payload"
// @Success      201  {object} models.SharedValues
// @Failure      400  {object} map[string]string
// @Failure      404  {object} map[string]string
// @Failure      500  {object} map[string]string
// @Router       /api/v1/clusters/{id}/shared-values [post]
// @Security     BearerAuth
func (h *SharedValuesHandler) CreateSharedValues(c *gin.Context) {
	clusterID := c.Param("id")

	if _, err := h.clusterRepo.FindByID(clusterID); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	var sv models.SharedValues
	if err := c.ShouldBindJSON(&sv); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	sv.ID = uuid.New().String()
	sv.ClusterID = clusterID

	if err := sv.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.repo.Create(&sv); err != nil {
		slog.Error("Failed to create shared values", "cluster_id", clusterID, "error", err)
		status, message := mapError(err, "Shared values")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusCreated, sv)
}

// UpdateSharedValues godoc
// @Summary      Update shared values
// @Description  Updates an existing shared values entry for the specified cluster.
// @Tags         shared-values
// @Accept       json
// @Produce      json
// @Param        id      path     string              true "Cluster ID"
// @Param        valueId path     string              true "Shared values ID"
// @Param        body    body     models.SharedValues true "Shared values payload"
// @Success      200     {object} models.SharedValues
// @Failure      400     {object} map[string]string
// @Failure      404     {object} map[string]string
// @Failure      500     {object} map[string]string
// @Router       /api/v1/clusters/{id}/shared-values/{valueId} [put]
// @Security     BearerAuth
func (h *SharedValuesHandler) UpdateSharedValues(c *gin.Context) {
	clusterID := c.Param("id")
	valueID := c.Param("valueId")

	if _, err := h.clusterRepo.FindByID(clusterID); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	existing, err := h.repo.FindByID(valueID)
	if err != nil {
		status, message := mapError(err, "Shared values")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if existing.ClusterID != clusterID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Shared values not found"})
		return
	}

	var input models.SharedValues
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	// Preserve ID and ClusterID from the URL.
	input.ID = valueID
	input.ClusterID = clusterID
	input.CreatedAt = existing.CreatedAt

	if err := input.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.repo.Update(&input); err != nil {
		slog.Error("Failed to update shared values", "id", valueID, "error", err)
		status, message := mapError(err, "Shared values")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, input)
}

// DeleteSharedValues godoc
// @Summary      Delete shared values
// @Description  Deletes a shared values entry from the specified cluster.
// @Tags         shared-values
// @Param        id      path string true "Cluster ID"
// @Param        valueId path string true "Shared values ID"
// @Success      204
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/clusters/{id}/shared-values/{valueId} [delete]
// @Security     BearerAuth
func (h *SharedValuesHandler) DeleteSharedValues(c *gin.Context) {
	clusterID := c.Param("id")
	valueID := c.Param("valueId")

	existing, err := h.repo.FindByID(valueID)
	if err != nil {
		status, message := mapError(err, "Shared values")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if existing.ClusterID != clusterID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Shared values not found"})
		return
	}

	if err := h.repo.Delete(valueID); err != nil {
		slog.Error("Failed to delete shared values", "id", valueID, "error", err)
		status, message := mapError(err, "Shared values")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}
