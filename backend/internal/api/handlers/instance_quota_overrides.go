package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"backend/internal/api/middleware"
	"backend/internal/cluster"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// Instance quota override handler message constants.
const (
	entityInstanceQuotaOverride = "Instance quota override"
)

const logKeyIQOInstanceID = "instance_id"

// msgQuotaAboveClusterSuffix ends the 403 message for an override above the
// cluster quota.
const msgQuotaAboveClusterSuffix = "; only admin or devops can set a higher quota"



// InstanceQuotaOverrideHandler handles per-instance resource quota override endpoints.
type InstanceQuotaOverrideHandler struct {
	overrideRepo     models.InstanceQuotaOverrideRepository
	instanceRepo     models.StackInstanceRepository
	clusterQuotaRepo models.ResourceQuotaRepository
	clusterResolver  clusterIDResolver
}

// WithClusterQuotas attaches the cluster quota repository and the cluster
// registry (to resolve the default cluster). SetQuotaOverride then validates
// the effective quota (cluster quota merged with the override, as the
// deployer applies it), so for example an override request above the cluster
// limit is a 400. Returns h for chaining. Without it only the override itself
// is validated.
func (h *InstanceQuotaOverrideHandler) WithClusterQuotas(repo models.ResourceQuotaRepository, registry *cluster.Registry) *InstanceQuotaOverrideHandler {
	h.clusterQuotaRepo = repo
	if registry != nil {
		h.clusterResolver = registry
	}
	return h
}

// clusterQuotaFor returns the cluster quota of the instance's cluster (the
// default cluster when the instance has none), or nil when no cluster or no
// quota is configured. Other lookup errors are returned.
func (h *InstanceQuotaOverrideHandler) clusterQuotaFor(ctx context.Context, inst *models.StackInstance) (*models.ResourceQuotaConfig, error) {
	return lookupClusterQuota(ctx, h.clusterQuotaRepo, h.clusterResolver, inst.ClusterID)
}

// lookupClusterQuota returns the cluster quota of clusterID (the default
// cluster when clusterID is empty and resolver is set), or nil when no cluster
// or no quota is configured. Other lookup errors are returned.
func lookupClusterQuota(ctx context.Context, repo models.ResourceQuotaRepository, resolver clusterIDResolver, clusterID string) (*models.ResourceQuotaConfig, error) {
	if repo == nil {
		return nil, nil
	}
	if clusterID == "" {
		if resolver == nil {
			return nil, nil
		}
		resolved, err := resolver.ResolveClusterID("")
		if err != nil {
			if errors.Is(err, cluster.ErrNoDefaultCluster) || isNotFoundError(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("resolve default cluster: %w", err)
		}
		clusterID = resolved
	}
	quota, err := repo.GetByClusterID(ctx, clusterID)
	if err != nil {
		if isNotFoundError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get cluster quota: %w", err)
	}
	return quota, nil
}

// NewInstanceQuotaOverrideHandler creates a new InstanceQuotaOverrideHandler.
func NewInstanceQuotaOverrideHandler(
	overrideRepo models.InstanceQuotaOverrideRepository,
	instanceRepo models.StackInstanceRepository,
) *InstanceQuotaOverrideHandler {
	return &InstanceQuotaOverrideHandler{
		overrideRepo: overrideRepo,
		instanceRepo: instanceRepo,
	}
}

// setQuotaOverrideRequest is the request body for setting a quota override.
type setQuotaOverrideRequest struct {
	CPURequest    string `json:"cpu_request" example:"500m"`
	CPULimit      string `json:"cpu_limit" example:"2000m"`
	MemoryRequest string `json:"memory_request" example:"256Mi"`
	MemoryLimit   string `json:"memory_limit" example:"1Gi"`
	StorageLimit  string `json:"storage_limit" example:"10Gi"`
	PodLimit      *int   `json:"pod_limit" example:"20"`
}

// GetQuotaOverride godoc
// @Summary     Get quota override for an instance
// @Description Retrieve the per-instance resource quota override for a stack instance
// @Tags        stack-instances
// @Produce     json
// @Param       id  path     string true "Stack Instance ID"
// @Success     200 {object} models.InstanceQuotaOverride
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Security    BearerAuth
// @Router      /api/v1/stack-instances/{id}/quota-overrides [get]
func (h *InstanceQuotaOverrideHandler) GetQuotaOverride(c *gin.Context) {
	instanceID := c.Param("id")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		if status == http.StatusInternalServerError {
			slog.Error(msgFailedFindStackInstance, logKeyIQOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: owner, admin or devops (see canModifyInstance).
	if !requireInstanceModify(c, inst) {
		return
	}

	override, err := h.overrideRepo.GetByInstanceID(c.Request.Context(), instanceID)
	if err != nil {
		status, message := mapError(err, entityInstanceQuotaOverride)
		if status == http.StatusInternalServerError {
			slog.Error("failed to get quota override", logKeyIQOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, override)
}

// SetQuotaOverride godoc
// @Summary     Set or update quota override for an instance
// @Description PUT replaces all fields of the override: a field that is not sent is cleared (an empty quantity or a missing pod_limit means no override for that field, so the cluster quota applies). To change one field, read the override with GET, change the field and send all fields. There is no PATCH.
// @Description Upsert the per-instance resource quota override for a stack instance. Each non-empty quantity must be a valid Kubernetes quantity (for example 500m, 2, 512Mi, 10Gi) and not negative; cpu_request must not exceed cpu_limit and memory_request must not exceed memory_limit; pod_limit must not be negative. Values are trimmed. The effective quota (cluster quota of the instance's cluster merged with this override) must follow the same rules, for example "cpu_request 4 (instance override) exceeds the effective cpu_limit 2 (cluster quota)". Invalid input returns 400 with the field name. Authorization: the instance owner, admin or devops. An owner without the admin or devops role cannot set a value above the cluster quota of the instance's cluster (403, for example "cpu_limit 64 exceeds the cluster quota 16; only admin or devops can set a higher quota"); a field without a cluster value has no cap, and pod_limit 0 (no limit) counts as above a cluster pod_limit. A value equal to the stored override passes, so an owner can change one field without losing an admin grant. Admin and devops may exceed the cluster quota. The check runs on write (and on clone), not on deploy.
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id   path     string                   true "Stack Instance ID"
// @Param       body body     setQuotaOverrideRequest   true "Quota override values"
// @Success     200  {object} models.InstanceQuotaOverride
// @Failure     400  {object} map[string]string
// @Failure     403  {object} map[string]string
// @Failure     404  {object} map[string]string
// @Failure     500  {object} map[string]string
// @Security    BearerAuth
// @Router      /api/v1/stack-instances/{id}/quota-overrides [put]
func (h *InstanceQuotaOverrideHandler) SetQuotaOverride(c *gin.Context) {
	instanceID := c.Param("id")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		if status == http.StatusInternalServerError {
			slog.Error(msgFailedFindStackInstance, logKeyIQOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: owner, admin or devops (see canModifyInstance).
	if !requireInstanceModify(c, inst) {
		return
	}

	var input setQuotaOverrideRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	// Trim the quantities: Kubernetes rejects " 1Gi" at deploy time.
	override := &models.InstanceQuotaOverride{
		StackInstanceID: instanceID,
		CPURequest:      strings.TrimSpace(input.CPURequest),
		CPULimit:        strings.TrimSpace(input.CPULimit),
		MemoryRequest:   strings.TrimSpace(input.MemoryRequest),
		MemoryLimit:     strings.TrimSpace(input.MemoryLimit),
		StorageLimit:    strings.TrimSpace(input.StorageLimit),
		PodLimit:        input.PodLimit,
	}

	// Validate before the upsert so an invalid quantity is a 400 with the
	// field name, independent of the repository implementation.
	if err := override.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate the effective quota (cluster quota + this override), as the
	// deployer applies it to the namespace.
	if h.clusterQuotaRepo != nil {
		clusterQuota, err := h.clusterQuotaFor(c.Request.Context(), inst)
		if err != nil {
			slog.Error("failed to load cluster quota for override validation", logKeyIQOInstanceID, instanceID, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}
		if err := models.ValidateEffectiveQuota(clusterQuota, override); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// Only admin and devops may set a value above the cluster quota. The
		// check runs at write time only; the deploy applies the stored
		// override without a new check.
		if !isPrivilegedRole(c) {
			// A value equal to the stored override passes, so an owner can
			// change one field without losing an admin grant on another.
			existing, err := h.overrideRepo.GetByInstanceID(c.Request.Context(), instanceID)
			if err != nil {
				if !isNotFoundError(err) {
					slog.Error("failed to load quota override for cluster quota check", logKeyIQOInstanceID, instanceID, "error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
					return
				}
				existing = nil
			}
			if err := models.CheckOverrideWithinClusterQuota(clusterQuota, override, existing); err != nil {
				slog.Warn("Quota override above cluster quota denied",
					"user_id", middleware.GetUserIDFromContext(c),
					"role", middleware.GetRoleFromContext(c),
					logKeyIQOInstanceID, instanceID,
					"error", err,
				)
				c.JSON(http.StatusForbidden, gin.H{"error": err.Error() + msgQuotaAboveClusterSuffix})
				return
			}
		}
	}

	if err := h.overrideRepo.Upsert(c.Request.Context(), override); err != nil {
		status, message := mapError(err, entityInstanceQuotaOverride)
		if status == http.StatusInternalServerError {
			slog.Error("failed to upsert quota override", logKeyIQOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Re-read to return the persisted state (includes ID, timestamps).
	saved, err := h.overrideRepo.GetByInstanceID(c.Request.Context(), instanceID)
	if err != nil {
		status, message := mapError(err, entityInstanceQuotaOverride)
		if status == http.StatusInternalServerError {
			slog.Error("failed to re-read quota override after upsert", logKeyIQOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, saved)
}

// DeleteQuotaOverride godoc
// @Summary     Delete quota override for an instance
// @Description Remove the per-instance resource quota override for a stack instance
// @Tags        stack-instances
// @Produce     json
// @Param       id  path     string true "Stack Instance ID"
// @Success     204 "No Content"
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Security    BearerAuth
// @Router      /api/v1/stack-instances/{id}/quota-overrides [delete]
func (h *InstanceQuotaOverrideHandler) DeleteQuotaOverride(c *gin.Context) {
	instanceID := c.Param("id")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		if status == http.StatusInternalServerError {
			slog.Error(msgFailedFindStackInstance, logKeyIQOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: owner, admin or devops (see canModifyInstance).
	if !requireInstanceModify(c, inst) {
		return
	}

	if err := h.overrideRepo.Delete(c.Request.Context(), instanceID); err != nil {
		status, message := mapError(err, entityInstanceQuotaOverride)
		if status == http.StatusInternalServerError {
			slog.Error("failed to delete quota override", logKeyIQOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}
