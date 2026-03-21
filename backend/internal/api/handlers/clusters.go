package handlers

import (
	"log/slog"
	"net/http"

	"backend/internal/cluster"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// CreateClusterRequest is the input payload for creating a cluster.
type CreateClusterRequest struct {
	Name           string `json:"name" binding:"required"`
	Description    string `json:"description"`
	APIServerURL   string `json:"api_server_url" binding:"required"`
	KubeconfigData string `json:"kubeconfig_data" binding:"required"`
	Region         string `json:"region"`
	MaxNamespaces  int    `json:"max_namespaces"`
	IsDefault      bool   `json:"is_default"`
}

// UpdateClusterRequest is the input payload for updating a cluster.
type UpdateClusterRequest struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	APIServerURL   string `json:"api_server_url"`
	KubeconfigData string `json:"kubeconfig_data"`
	Region         string `json:"region"`
	MaxNamespaces  *int   `json:"max_namespaces,omitempty"`
	IsDefault      *bool  `json:"is_default,omitempty"`
}

// ClusterHandler provides CRUD endpoints for cluster management.
type ClusterHandler struct {
	clusterRepo  models.ClusterRepository
	registry     *cluster.Registry
	instanceRepo models.StackInstanceRepository
}

// NewClusterHandler creates a new ClusterHandler with the given dependencies.
func NewClusterHandler(
	clusterRepo models.ClusterRepository,
	registry *cluster.Registry,
	instanceRepo models.StackInstanceRepository,
) *ClusterHandler {
	return &ClusterHandler{
		clusterRepo:  clusterRepo,
		registry:     registry,
		instanceRepo: instanceRepo,
	}
}

// ListClusters godoc
// @Summary      List all clusters
// @Description  Returns all registered clusters. Kubeconfig data is never included in responses.
// @Tags         clusters
// @Produce      json
// @Success      200  {array}   models.Cluster
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/clusters [get]
// @Security     BearerAuth
func (h *ClusterHandler) ListClusters(c *gin.Context) {
	clusters, err := h.clusterRepo.List()
	if err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, clusters)
}

// CreateCluster godoc
// @Summary      Register a new cluster
// @Description  Registers a new Kubernetes cluster with the provided kubeconfig data.
// @Tags         clusters
// @Accept       json
// @Produce      json
// @Param        cluster  body  CreateClusterRequest  true  "Cluster registration payload"
// @Success      201  {object}  models.Cluster
// @Failure      400  {object}  map[string]string
// @Failure      409  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/clusters [post]
// @Security     BearerAuth
func (h *ClusterHandler) CreateCluster(c *gin.Context) {
	var req CreateClusterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	cl := &models.Cluster{
		Name:           req.Name,
		Description:    req.Description,
		APIServerURL:   req.APIServerURL,
		KubeconfigData: req.KubeconfigData,
		Region:         req.Region,
		MaxNamespaces:  req.MaxNamespaces,
		IsDefault:      false,
		HealthStatus:   models.ClusterUnreachable,
	}

	if err := cl.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.clusterRepo.Create(cl); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if req.IsDefault {
		if err := h.clusterRepo.SetDefault(cl.ID); err != nil {
			slog.Error("Failed to set new cluster as default", "cluster_id", cl.ID, "error", err)
		}
		cl.IsDefault = true
	}

	// Clear kubeconfig from the response (json:"-" already handles this,
	// but be explicit for defense in depth).
	cl.KubeconfigData = ""
	cl.KubeconfigPath = ""

	c.JSON(http.StatusCreated, cl)
}

// GetCluster godoc
// @Summary      Get cluster details
// @Description  Returns a single cluster by ID. Kubeconfig data is never included.
// @Tags         clusters
// @Produce      json
// @Param        id  path  string  true  "Cluster ID"
// @Success      200  {object}  models.Cluster
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/clusters/{id} [get]
// @Security     BearerAuth
func (h *ClusterHandler) GetCluster(c *gin.Context) {
	id := c.Param("id")

	cl, err := h.clusterRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, cl)
}

// UpdateCluster godoc
// @Summary      Update a cluster
// @Description  Updates cluster metadata and/or kubeconfig. If kubeconfig is updated, the cached client is invalidated.
// @Tags         clusters
// @Accept       json
// @Produce      json
// @Param        id       path  string                true  "Cluster ID"
// @Param        cluster  body  UpdateClusterRequest  true  "Cluster update payload"
// @Success      200  {object}  models.Cluster
// @Failure      400  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/clusters/{id} [put]
// @Security     BearerAuth
func (h *ClusterHandler) UpdateCluster(c *gin.Context) {
	id := c.Param("id")

	existing, err := h.clusterRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	var req UpdateClusterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	kubeconfigChanged := false
	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.Description != "" {
		existing.Description = req.Description
	}
	if req.APIServerURL != "" {
		existing.APIServerURL = req.APIServerURL
	}
	if req.KubeconfigData != "" {
		existing.KubeconfigData = req.KubeconfigData
		kubeconfigChanged = true
	}
	if req.Region != "" {
		existing.Region = req.Region
	}
	if req.MaxNamespaces != nil {
		existing.MaxNamespaces = *req.MaxNamespaces
	}
	if req.IsDefault != nil {
		if *req.IsDefault && !existing.IsDefault {
			// Use SetDefault to atomically unset the old default and set the new one.
			if err := h.clusterRepo.SetDefault(id); err != nil {
				status, message := mapError(err, "Cluster")
				c.JSON(status, gin.H{"error": message})
				return
			}
			existing.IsDefault = true
		} else if !*req.IsDefault && existing.IsDefault {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot unset default cluster via update; use SetDefault to change the default cluster"})
			return
		}
	}

	if err := h.clusterRepo.Update(existing); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Invalidate cached clients as needed.
	if h.registry != nil {
		if kubeconfigChanged {
			h.registry.InvalidateClient(id)
		}
		if req.IsDefault != nil {
			h.registry.InvalidateDefault()
		}
	}

	// Clear kubeconfig from the response.
	existing.KubeconfigData = ""
	existing.KubeconfigPath = ""

	c.JSON(http.StatusOK, existing)
}

// DeleteCluster godoc
// @Summary      Delete a cluster
// @Description  Removes a cluster registration. Blocked if any stack instances reference this cluster.
// @Tags         clusters
// @Produce      json
// @Param        id  path  string  true  "Cluster ID"
// @Success      204
// @Failure      404  {object}  map[string]string
// @Failure      409  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/clusters/{id} [delete]
// @Security     BearerAuth
func (h *ClusterHandler) DeleteCluster(c *gin.Context) {
	id := c.Param("id")

	// Verify cluster exists.
	if _, err := h.clusterRepo.FindByID(id); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Check for instances using this cluster.
	instances, err := h.instanceRepo.FindByCluster(id)
	if err != nil {
		slog.Error("Failed to check cluster instances", "cluster_id", id, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}
	if len(instances) > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Cannot delete cluster: stack instances still reference this cluster"})
		return
	}

	if err := h.clusterRepo.Delete(id); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Invalidate cached client.
	if h.registry != nil {
		h.registry.InvalidateClient(id)
	}

	c.Status(http.StatusNoContent)
}

// TestClusterConnection godoc
// @Summary      Test cluster connectivity
// @Description  Tests connectivity to a cluster by attempting to reach the Kubernetes API server.
// @Tags         clusters
// @Produce      json
// @Param        id  path  string  true  "Cluster ID"
// @Success      200  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      502  {object}  map[string]string
// @Failure      503  {object}  map[string]string
// @Router       /api/v1/clusters/{id}/test [post]
// @Security     BearerAuth
func (h *ClusterHandler) TestClusterConnection(c *gin.Context) {
	id := c.Param("id")

	// Verify cluster exists.
	if _, err := h.clusterRepo.FindByID(id); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if h.registry == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Cluster client registry is not available"})
		return
	}

	k8sClient, err := h.registry.GetK8sClient(id)
	if err != nil {
		slog.Error("Failed to build k8s client for cluster", "cluster_id", id, "error", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to connect to cluster"})
		return
	}

	// Attempt a lightweight API call to verify connectivity.
	version, err := k8sClient.Clientset().Discovery().ServerVersion()
	if err != nil {
		slog.Error("Cluster connectivity test failed", "cluster_id", id, "error", err)
		// Invalidate the client since it failed.
		h.registry.InvalidateClient(id)
		c.JSON(http.StatusBadGateway, gin.H{"status": "error", "message": "Cluster is unreachable"})
		return
	}

	// Invalidate so next access rebuilds from (possibly updated) kubeconfig.
	h.registry.InvalidateClient(id)

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Connection successful", "server_version": version.GitVersion})
}

// SetDefaultCluster godoc
// @Summary      Set a cluster as the default
// @Description  Sets the specified cluster as the default cluster for deployments.
// @Tags         clusters
// @Produce      json
// @Param        id  path  string  true  "Cluster ID"
// @Success      200  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/clusters/{id}/default [post]
// @Security     BearerAuth
func (h *ClusterHandler) SetDefaultCluster(c *gin.Context) {
	id := c.Param("id")

	if err := h.clusterRepo.SetDefault(id); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if h.registry != nil {
		h.registry.InvalidateDefault()
	}

	c.JSON(http.StatusOK, gin.H{"message": "Default cluster updated"})
}
