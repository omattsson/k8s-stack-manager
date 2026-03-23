package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"backend/internal/cluster"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	apimachineryversion "k8s.io/apimachinery/pkg/version"
)

// CreateClusterRequest is the input payload for creating a cluster.
type CreateClusterRequest struct {
	Name                string `json:"name" binding:"required"`
	Description         string `json:"description"`
	APIServerURL        string `json:"api_server_url" binding:"required"`
	KubeconfigData      string `json:"kubeconfig_data"`
	KubeconfigPath      string `json:"kubeconfig_path"`
	Region              string `json:"region"`
	MaxNamespaces       int    `json:"max_namespaces"`
	MaxInstancesPerUser int    `json:"max_instances_per_user"`
	IsDefault           bool   `json:"is_default"`
}

// UpdateClusterRequest is the input payload for updating a cluster.
type UpdateClusterRequest struct {
	Name                *string `json:"name,omitempty"`
	Description         *string `json:"description,omitempty"`
	APIServerURL        *string `json:"api_server_url,omitempty"`
	KubeconfigData      *string `json:"kubeconfig_data,omitempty"`
	KubeconfigPath      *string `json:"kubeconfig_path,omitempty"`
	Region              *string `json:"region,omitempty"`
	MaxNamespaces       *int    `json:"max_namespaces,omitempty"`
	MaxInstancesPerUser *int    `json:"max_instances_per_user,omitempty"`
	IsDefault           *bool   `json:"is_default,omitempty"`
}

// ClusterHandler provides CRUD endpoints for cluster management.
type ClusterHandler struct {
	clusterRepo  models.ClusterRepository
	registry     *cluster.Registry
	instanceRepo models.StackInstanceRepository
	quotaRepo    models.ResourceQuotaRepository
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

// NewClusterHandlerWithQuotas creates a ClusterHandler with resource quota support.
func NewClusterHandlerWithQuotas(
	clusterRepo models.ClusterRepository,
	registry *cluster.Registry,
	instanceRepo models.StackInstanceRepository,
	quotaRepo models.ResourceQuotaRepository,
) *ClusterHandler {
	return &ClusterHandler{
		clusterRepo:  clusterRepo,
		registry:     registry,
		instanceRepo: instanceRepo,
		quotaRepo:    quotaRepo,
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
		Name:                req.Name,
		Description:         req.Description,
		APIServerURL:        req.APIServerURL,
		KubeconfigData:      req.KubeconfigData,
		KubeconfigPath:      req.KubeconfigPath,
		Region:              req.Region,
		MaxNamespaces:       req.MaxNamespaces,
		MaxInstancesPerUser: req.MaxInstancesPerUser,
		IsDefault:           false,
		HealthStatus:        models.ClusterUnreachable,
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
			// Return 201 with consistent schema; surface warning via header.
			cl.KubeconfigData = ""
			cl.KubeconfigPath = ""
			c.Header("Warning", `199 - "Cluster created but could not be set as default; it remains non-default"`)
			c.JSON(http.StatusCreated, cl)
			return
		}
		cl.IsDefault = true
		if h.registry != nil {
			h.registry.InvalidateDefault()
		}
	} else {
		// Auto-default: if no default cluster exists yet, make this one the default
		// so that instance creation (which resolves empty cluster_id to the default)
		// doesn't fail with a 400.
		_, err := h.clusterRepo.FindDefault()
		if err != nil && errors.Is(err, dberrors.ErrNotFound) {
			if setErr := h.clusterRepo.SetDefault(cl.ID); setErr != nil {
				slog.Error("Failed to auto-set first cluster as default", "cluster_id", cl.ID, "error", setErr)
			} else {
				cl.IsDefault = true
				if h.registry != nil {
					h.registry.InvalidateDefault()
				}
			}
		}
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
	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.APIServerURL != nil {
		existing.APIServerURL = *req.APIServerURL
	}
	if req.KubeconfigData != nil {
		existing.KubeconfigData = *req.KubeconfigData
		existing.KubeconfigPath = "" // mutual exclusion
		kubeconfigChanged = true
	}
	if req.KubeconfigPath != nil {
		existing.KubeconfigPath = *req.KubeconfigPath
		existing.KubeconfigData = "" // mutual exclusion
		kubeconfigChanged = true
	}
	if req.Region != nil {
		existing.Region = *req.Region
	}
	if req.MaxNamespaces != nil {
		existing.MaxNamespaces = *req.MaxNamespaces
	}
	if req.MaxInstancesPerUser != nil {
		existing.MaxInstancesPerUser = *req.MaxInstancesPerUser
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

	// Re-validate after applying updates (e.g. kubeconfig mutual exclusion).
	if err := existing.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
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

	// Invalidate cached client and default routing.
	if h.registry != nil {
		h.registry.InvalidateClient(id)
		h.registry.InvalidateDefault()
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

	// Attempt a lightweight API call to verify connectivity with a timeout
	// so that hung network calls don't block the request goroutine indefinitely.
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	var version *apimachineryversion.Info
	if restClient := k8sClient.Clientset().Discovery().RESTClient(); restClient != nil {
		result := restClient.Get().AbsPath("/version").Do(ctx)
		if err := result.Error(); err != nil {
			slog.Error("Cluster connectivity test failed", "cluster_id", id, "error", err)
			h.registry.InvalidateClient(id)
			c.JSON(http.StatusBadGateway, gin.H{"status": "error", "message": "Cluster is unreachable"})
			return
		}
		raw, _ := result.Raw()
		v := &apimachineryversion.Info{}
		if jsonErr := json.Unmarshal(raw, v); jsonErr == nil {
			version = v
		}
	} else {
		// Fallback for test fakes that don't implement RESTClient.
		v, err := k8sClient.Clientset().Discovery().ServerVersion()
		if err != nil {
			slog.Error("Cluster connectivity test failed", "cluster_id", id, "error", err)
			h.registry.InvalidateClient(id)
			c.JSON(http.StatusBadGateway, gin.H{"status": "error", "message": "Cluster is unreachable"})
			return
		}
		version = v
	}

	// Invalidate so next access rebuilds from (possibly updated) kubeconfig.
	h.registry.InvalidateClient(id)

	gitVersion := ""
	if version != nil {
		gitVersion = version.GitVersion
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Connection successful", "server_version": gitVersion})
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

// GetClusterHealthSummary godoc
// @Summary      Get cluster health summary
// @Description  Returns node count, CPU/memory totals, and namespace count for a cluster.
// @Tags         clusters
// @Produce      json
// @Security     BearerAuth
// @Param        id  path  string  true  "Cluster ID"
// @Success      200  {object}  k8s.ClusterSummary
// @Failure      400  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/clusters/{id}/health/summary [get]
func (h *ClusterHandler) GetClusterHealthSummary(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster ID is required"})
		return
	}

	if _, err := h.clusterRepo.FindByID(id); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if h.registry == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Cluster client registry is not available"})
		return
	}

	k8sClient, err := h.registry.GetK8sClient(id)
	if err != nil {
		slog.Error("Failed to get K8s client", "error", err, "cluster_id", id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to connect to cluster"})
		return
	}

	summary, err := k8sClient.GetClusterSummary(c.Request.Context())
	if err != nil {
		slog.Error("Failed to get cluster summary", "error", err, "cluster_id", id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve cluster health"})
		return
	}

	c.JSON(http.StatusOK, summary)
}

// GetClusterNodes godoc
// @Summary      Get cluster node statuses
// @Description  Returns per-node health, conditions, and capacity for a cluster.
// @Tags         clusters
// @Produce      json
// @Security     BearerAuth
// @Param        id  path  string  true  "Cluster ID"
// @Success      200  {array}   k8s.NodeStatus
// @Failure      400  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/clusters/{id}/health/nodes [get]
func (h *ClusterHandler) GetClusterNodes(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster ID is required"})
		return
	}

	if _, err := h.clusterRepo.FindByID(id); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if h.registry == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Cluster client registry is not available"})
		return
	}

	k8sClient, err := h.registry.GetK8sClient(id)
	if err != nil {
		slog.Error("Failed to get K8s client", "error", err, "cluster_id", id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to connect to cluster"})
		return
	}

	nodes, err := k8sClient.GetNodeStatuses(c.Request.Context())
	if err != nil {
		slog.Error("Failed to get node statuses", "error", err, "cluster_id", id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve node statuses"})
		return
	}

	c.JSON(http.StatusOK, nodes)
}

// GetClusterNamespaces godoc
// @Summary      Get cluster namespaces
// @Description  Returns all stack-* namespaces in the cluster.
// @Tags         clusters
// @Produce      json
// @Security     BearerAuth
// @Param        id  path  string  true  "Cluster ID"
// @Success      200  {array}   k8s.NamespaceInfo
// @Failure      400  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/clusters/{id}/namespaces [get]
func (h *ClusterHandler) GetClusterNamespaces(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cluster ID is required"})
		return
	}

	if _, err := h.clusterRepo.FindByID(id); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if h.registry == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Cluster client registry is not available"})
		return
	}

	k8sClient, err := h.registry.GetK8sClient(id)
	if err != nil {
		slog.Error("Failed to get K8s client", "error", err, "cluster_id", id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to connect to cluster"})
		return
	}

	namespaces, err := k8sClient.ListStackNamespaces(c.Request.Context())
	if err != nil {
		slog.Error("Failed to list cluster namespaces", "error", err, "cluster_id", id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve namespaces"})
		return
	}

	c.JSON(http.StatusOK, namespaces)
}

// UpdateQuotaRequest is the input payload for creating or updating resource quotas.
type UpdateQuotaRequest struct {
	CPURequest    string `json:"cpu_request"`
	CPULimit      string `json:"cpu_limit"`
	MemoryRequest string `json:"memory_request"`
	MemoryLimit   string `json:"memory_limit"`
	StorageLimit  string `json:"storage_limit"`
	PodLimit      int    `json:"pod_limit"`
}

// GetQuotas godoc
// @Summary      Get resource quota config for a cluster
// @Description  Returns the resource quota configuration for a cluster, or 404 if not set.
// @Tags         clusters
// @Produce      json
// @Param        id  path  string  true  "Cluster ID"
// @Success      200  {object}  models.ResourceQuotaConfig
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/clusters/{id}/quotas [get]
// @Security     BearerAuth
func (h *ClusterHandler) GetQuotas(c *gin.Context) {
	id := c.Param("id")

	// Verify cluster exists.
	if _, err := h.clusterRepo.FindByID(id); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if h.quotaRepo == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Resource quota management is not available"})
		return
	}

	quota, err := h.quotaRepo.GetByClusterID(c.Request.Context(), id)
	if err != nil {
		status, message := mapError(err, "Resource quota config")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, quota)
}

// UpdateQuotas godoc
// @Summary      Create or update resource quota config for a cluster
// @Description  Creates or updates the resource quota configuration for a cluster. Admin only.
// @Tags         clusters
// @Accept       json
// @Produce      json
// @Param        id     path  string              true  "Cluster ID"
// @Param        quota  body  UpdateQuotaRequest   true  "Quota configuration"
// @Success      200  {object}  models.ResourceQuotaConfig
// @Failure      400  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/clusters/{id}/quotas [put]
// @Security     BearerAuth
func (h *ClusterHandler) UpdateQuotas(c *gin.Context) {
	id := c.Param("id")

	// Verify cluster exists.
	if _, err := h.clusterRepo.FindByID(id); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if h.quotaRepo == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Resource quota management is not available"})
		return
	}

	var req UpdateQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	config := &models.ResourceQuotaConfig{
		ClusterID:     id,
		CPURequest:    req.CPURequest,
		CPULimit:      req.CPULimit,
		MemoryRequest: req.MemoryRequest,
		MemoryLimit:   req.MemoryLimit,
		StorageLimit:  req.StorageLimit,
		PodLimit:      req.PodLimit,
	}

	if err := config.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.quotaRepo.Upsert(c.Request.Context(), config); err != nil {
		status, message := mapError(err, "Resource quota config")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Re-read the saved config so timestamps and ID are populated.
	saved, err := h.quotaRepo.GetByClusterID(c.Request.Context(), id)
	if err != nil {
		slog.Error("Failed to read back saved quota config", "cluster_id", id, "error", err)
		c.JSON(http.StatusOK, config)
		return
	}

	c.JSON(http.StatusOK, saved)
}

// DeleteQuotas godoc
// @Summary      Delete resource quota config for a cluster
// @Description  Removes the resource quota configuration for a cluster. Admin only.
// @Tags         clusters
// @Produce      json
// @Param        id  path  string  true  "Cluster ID"
// @Success      204
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/clusters/{id}/quotas [delete]
// @Security     BearerAuth
func (h *ClusterHandler) DeleteQuotas(c *gin.Context) {
	id := c.Param("id")

	// Verify cluster exists.
	if _, err := h.clusterRepo.FindByID(id); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if h.quotaRepo == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Resource quota management is not available"})
		return
	}

	if err := h.quotaRepo.Delete(c.Request.Context(), id); err != nil {
		status, message := mapError(err, "Resource quota config")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}

// NamespaceResourceUsage represents resource usage for a single namespace.
type NamespaceResourceUsage struct {
	Namespace   string `json:"namespace"`
	CPUUsed     string `json:"cpu_used"`
	CPULimit    string `json:"cpu_limit"`
	MemoryUsed  string `json:"memory_used"`
	MemoryLimit string `json:"memory_limit"`
	PodCount    int    `json:"pod_count"`
	PodLimit    int    `json:"pod_limit"`
}

// ClusterUtilization represents aggregated resource utilization for a cluster.
type ClusterUtilization struct {
	ClusterID  string                   `json:"cluster_id"`
	Namespaces []NamespaceResourceUsage `json:"namespaces"`
}

// GetUtilization godoc
// @Summary      Get cluster-wide resource utilization
// @Description  Returns per-namespace resource usage for all stack namespaces in the cluster.
// @Tags         clusters
// @Produce      json
// @Param        id  path  string  true  "Cluster ID"
// @Success      200  {object}  ClusterUtilization
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/clusters/{id}/utilization [get]
// @Security     BearerAuth
func (h *ClusterHandler) GetUtilization(c *gin.Context) {
	id := c.Param("id")

	// Verify cluster exists.
	if _, err := h.clusterRepo.FindByID(id); err != nil {
		status, message := mapError(err, "Cluster")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if h.registry == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Cluster client registry is not available"})
		return
	}

	k8sClient, err := h.registry.GetK8sClient(id)
	if err != nil {
		slog.Error("Failed to get K8s client for utilization", "error", err, "cluster_id", id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to connect to cluster"})
		return
	}

	ctx := c.Request.Context()
	namespaces, err := k8sClient.ListStackNamespaces(ctx)
	if err != nil {
		slog.Error("Failed to list namespaces for utilization", "error", err, "cluster_id", id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve namespaces"})
		return
	}

	var usages []NamespaceResourceUsage
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 10) // bound concurrency to 10
	usages = make([]NamespaceResourceUsage, len(namespaces))

	for i, ns := range namespaces {
		wg.Add(1)
		go func(idx int, nsName string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			usage, err := k8sClient.GetNamespaceResourceUsage(ctx, nsName)
			if err != nil {
				slog.Warn("Failed to get resource usage for namespace", "namespace", nsName, "error", err)
				mu.Lock()
				usages[idx] = NamespaceResourceUsage{Namespace: nsName}
				mu.Unlock()
				return
			}
			mu.Lock()
			usages[idx] = NamespaceResourceUsage{
				Namespace:   nsName,
				CPUUsed:     usage.CPUUsed,
				CPULimit:    usage.CPULimit,
				MemoryUsed:  usage.MemoryUsed,
				MemoryLimit: usage.MemoryLimit,
				PodCount:    usage.PodCount,
				PodLimit:    usage.PodLimit,
			}
			mu.Unlock()
		}(i, ns.Name)
	}
	wg.Wait()

	c.JSON(http.StatusOK, ClusterUtilization{
		ClusterID:  id,
		Namespaces: usages,
	})
}
