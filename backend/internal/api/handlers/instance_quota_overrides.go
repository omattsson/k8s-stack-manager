package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

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
	// quotaClients writes the effective quota to the instance namespace
	// after a change (nil: the change applies at the next deploy).
	quotaClients quotaClientGetter
	// applyDone, when set, runs after each background quota write (tests).
	applyDone func()
}

// quotaApplyTimeout limits the background quota write after an override change.
const quotaApplyTimeout = 30 * time.Second

// WithClusterQuotas attaches the cluster quota repository and the cluster
// registry (to resolve the default cluster). SetQuotaOverride then validates
// the effective quota (cluster quota merged with the override, as the
// deployer applies it), so for example an override request above the cluster
// limit is a 400. With a registry, a set or delete also writes the effective
// quota to the namespace of a deployed instance at once. Returns h for
// chaining. Without it only the override itself is validated.
func (h *InstanceQuotaOverrideHandler) WithClusterQuotas(repo models.ResourceQuotaRepository, registry *cluster.Registry) *InstanceQuotaOverrideHandler {
	h.clusterQuotaRepo = repo
	if registry != nil {
		h.clusterResolver = registry
		h.quotaClients = registryQuotaClients(registry)
	}
	return h
}

// shouldApplyQuota reports whether an override change is written to the
// namespace of inst at once: the instance has a namespace and is not a draft
// (no namespace yet) or cleaning (the namespace goes away).
func shouldApplyQuota(inst *models.StackInstance) bool {
	if inst.Namespace == "" {
		return false
	}
	return inst.Status != models.StackStatusDraft && inst.Status != models.StackStatusCleaning
}

// applyQuotaInBackground writes the effective quota of inst to its namespace
// in a background goroutine, when there is a quota client and
// shouldApplyQuota is true.
func (h *InstanceQuotaOverrideHandler) applyQuotaInBackground(inst *models.StackInstance) {
	if h.quotaClients == nil || !shouldApplyQuota(inst) {
		return
	}
	instCopy := *inst
	go func() {
		if h.applyDone != nil {
			defer h.applyDone()
		}
		ctx, cancel := context.WithTimeout(context.Background(), quotaApplyTimeout)
		defer cancel()
		if err := h.applyEffectiveQuota(ctx, &instCopy); err != nil {
			slog.Warn("Failed to apply the quota override to the namespace; the next deploy applies it",
				logKeyIQOInstanceID, instCopy.ID, "namespace", instCopy.Namespace, "error", err)
		}
	}()
}

// applyEffectiveQuota writes the effective quota of inst (the stored cluster
// quota merged with the stored override, read under the namespace lock) to
// its namespace. A missing namespace or an empty quota writes nothing.
func (h *InstanceQuotaOverrideHandler) applyEffectiveQuota(ctx context.Context, inst *models.StackInstance) error {
	clusterID := inst.ClusterID
	if clusterID == "" && h.clusterResolver != nil {
		resolved, err := h.clusterResolver.ResolveClusterID("")
		if err != nil {
			return fmt.Errorf("resolve default cluster: %w", err)
		}
		clusterID = resolved
	}
	client, err := h.quotaClients(clusterID)
	if err != nil {
		return fmt.Errorf("get k8s client: %w", err)
	}
	exists, err := client.NamespaceExists(ctx, inst.Namespace)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	src := quotaSources{clusterQuotas: h.clusterQuotaRepo, overrides: h.overrideRepo}
	wrote, err := writeNamespaceQuota(ctx, client, src, clusterID, inst.ID, inst.Namespace)
	if err != nil {
		return err
	}
	if wrote {
		slog.Info("Applied the effective quota to the namespace after a quota override change",
			logKeyIQOInstanceID, inst.ID, "namespace", inst.Namespace)
	}
	return nil
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
	_, quota, err := resolveClusterQuota(ctx, repo, resolver, clusterID)
	return quota, err
}

// resolveClusterQuota returns the cluster ID that applies to clusterID (the
// default cluster when clusterID is empty and resolver is set; empty when no
// default cluster exists) and the quota of that cluster (nil when repo is nil
// or no quota is configured). Other errors are returned.
func resolveClusterQuota(ctx context.Context, repo models.ResourceQuotaRepository, resolver clusterIDResolver, clusterID string) (string, *models.ResourceQuotaConfig, error) {
	if clusterID == "" && resolver != nil {
		resolved, err := resolver.ResolveClusterID("")
		if err != nil {
			if errors.Is(err, cluster.ErrNoDefaultCluster) || isNotFoundError(err) {
				return "", nil, nil
			}
			return "", nil, fmt.Errorf("resolve default cluster: %w", err)
		}
		clusterID = resolved
	}
	if repo == nil || clusterID == "" {
		return clusterID, nil, nil
	}
	quota, err := repo.GetByClusterID(ctx, clusterID)
	if err != nil {
		if isNotFoundError(err) {
			return clusterID, nil, nil
		}
		return clusterID, nil, fmt.Errorf("get cluster quota: %w", err)
	}
	return clusterID, quota, nil
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
	// Per-container LimitRange values and the namespace ephemeral-storage total.
	ContainerCPURequest              string `json:"container_cpu_request" example:"100m"`
	ContainerCPULimit                string `json:"container_cpu_limit" example:"500m"`
	ContainerCPUMax                  string `json:"container_cpu_max" example:"2"`
	ContainerMemoryRequest           string `json:"container_memory_request" example:"128Mi"`
	ContainerMemoryLimit             string `json:"container_memory_limit" example:"512Mi"`
	ContainerMemoryMax               string `json:"container_memory_max" example:"4Gi"`
	EphemeralStorageLimit            string `json:"ephemeral_storage_limit" example:"20Gi"`
	ContainerEphemeralStorageRequest string `json:"container_ephemeral_storage_request" example:"128Mi"`
	ContainerEphemeralStorageLimit   string `json:"container_ephemeral_storage_limit" example:"1Gi"`
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), instanceID)
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

// EffectiveQuotaValues holds the quota values that apply to the namespace of
// a stack instance. An empty quantity means no value; pod_limit 0 means no
// limit.
type EffectiveQuotaValues struct {
	CPURequest                       string `json:"cpu_request" example:"500m"`
	CPULimit                         string `json:"cpu_limit" example:"2"`
	MemoryRequest                    string `json:"memory_request" example:"1Gi"`
	MemoryLimit                      string `json:"memory_limit" example:"2Gi"`
	StorageLimit                     string `json:"storage_limit" example:"10Gi"`
	EphemeralStorageLimit            string `json:"ephemeral_storage_limit" example:"4Gi"`
	ContainerCPURequest              string `json:"container_cpu_request" example:"100m"`
	ContainerCPULimit                string `json:"container_cpu_limit" example:"500m"`
	ContainerCPUMax                  string `json:"container_cpu_max" example:"1"`
	ContainerMemoryRequest           string `json:"container_memory_request" example:"128Mi"`
	ContainerMemoryLimit             string `json:"container_memory_limit" example:"512Mi"`
	ContainerMemoryMax               string `json:"container_memory_max" example:"1Gi"`
	ContainerEphemeralStorageRequest string `json:"container_ephemeral_storage_request" example:"128Mi"`
	ContainerEphemeralStorageLimit   string `json:"container_ephemeral_storage_limit" example:"1Gi"`
	PodLimit                         int    `json:"pod_limit" example:"10"`
}

// EffectiveQuotaResponse is the response of GET /stack-instances/:id/quota.
type EffectiveQuotaResponse struct {
	Effective EffectiveQuotaValues          `json:"effective"`
	Cluster   *models.ResourceQuotaConfig   `json:"cluster"`
	Override  *models.InstanceQuotaOverride `json:"override"`
	// Sources maps each field of effective to "override", "cluster" or "none".
	Sources   map[string]string `json:"sources"`
	ClusterID string            `json:"cluster_id"`
	// InvalidReason is the validation error of the merged quota (empty when valid).
	InvalidReason string `json:"invalid_reason,omitempty" example:"cpu_request 3 (instance override) exceeds the effective cpu_limit 2 (cluster quota)"`
	CanModify     bool   `json:"can_modify"`
	// Valid is false when the merged quota breaks a quota rule; a deploy then fails.
	Valid bool `json:"valid"`
}

func effectiveQuotaValues(q *models.ResourceQuotaConfig) EffectiveQuotaValues {
	return EffectiveQuotaValues{
		CPURequest:                       q.CPURequest,
		CPULimit:                         q.CPULimit,
		MemoryRequest:                    q.MemoryRequest,
		MemoryLimit:                      q.MemoryLimit,
		StorageLimit:                     q.StorageLimit,
		EphemeralStorageLimit:            q.EphemeralStorageLimit,
		ContainerCPURequest:              q.ContainerCPURequest,
		ContainerCPULimit:                q.ContainerCPULimit,
		ContainerCPUMax:                  q.ContainerCPUMax,
		ContainerMemoryRequest:           q.ContainerMemoryRequest,
		ContainerMemoryLimit:             q.ContainerMemoryLimit,
		ContainerMemoryMax:               q.ContainerMemoryMax,
		ContainerEphemeralStorageRequest: q.ContainerEphemeralStorageRequest,
		ContainerEphemeralStorageLimit:   q.ContainerEphemeralStorageLimit,
		PodLimit:                         q.PodLimit,
	}
}

// GetEffectiveQuota godoc
// @Summary     Get the effective quota of an instance
// @Description Returns the quota that applies to the namespace of the instance: the cluster quota of the instance's cluster (the default cluster when the instance has none) merged with the instance quota override, with the same merge as the deployer. A non-empty override quantity replaces the cluster value, and an override pod_limit (also 0) replaces the cluster pod_limit. sources names the origin of each field of effective: override, cluster or none. In effective, an empty quantity means no value and pod_limit 0 means no limit. cluster and override are null when they are not set; cluster_id is empty when the instance has no cluster and no default cluster exists. valid is false when the merged quota breaks a quota rule (models.ValidateEffectiveQuota, the check of the deployer and of the background quota write); invalid_reason then names the field, the values and their source, and a deploy of the instance fails until the quota is fixed. can_modify is true when the caller may set or delete the override (the owner, admin or devops). Any authenticated user can read it.
// @Tags        stack-instances
// @Produce     json
// @Param       id  path     string true "Stack Instance ID"
// @Success     200 {object} EffectiveQuotaResponse
// @Failure     401 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Security    BearerAuth
// @Router      /api/v1/stack-instances/{id}/quota [get]
func (h *InstanceQuotaOverrideHandler) GetEffectiveQuota(c *gin.Context) {
	inst, err := h.instanceRepo.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		if status == http.StatusInternalServerError {
			slog.Error(msgFailedFindStackInstance, logKeyIQOInstanceID, c.Param("id"), "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	ctx := c.Request.Context()
	clusterID, clusterQuota, err := resolveClusterQuota(ctx, h.clusterQuotaRepo, h.clusterResolver, inst.ClusterID)
	if err != nil {
		slog.Error("failed to load cluster quota for the effective quota", logKeyIQOInstanceID, inst.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	override, err := h.overrideRepo.GetByInstanceID(ctx, inst.ID)
	if err != nil {
		if !isNotFoundError(err) {
			slog.Error("failed to get quota override for the effective quota", logKeyIQOInstanceID, inst.ID, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}
		override = nil
	}

	resp := EffectiveQuotaResponse{
		Effective: effectiveQuotaValues(models.MergeQuotaOverride(clusterQuota, override)),
		Cluster:   clusterQuota,
		Override:  override,
		Sources:   models.QuotaSources(clusterQuota, override),
		ClusterID: clusterID,
		CanModify: canModifyInstance(c, inst),
		Valid:     true,
	}
	if err := models.ValidateEffectiveQuota(clusterQuota, override); err != nil {
		resp.Valid = false
		resp.InvalidReason = err.Error()
	}
	c.JSON(http.StatusOK, resp)
}

// SetQuotaOverride godoc
// @Summary     Set or update quota override for an instance
// @Description PUT replaces all fields of the override: a field that is not sent is cleared (an empty quantity or a missing pod_limit means no override for that field, so the cluster quota applies). To change one field, read the override with GET, change the field and send all fields. There is no PATCH.
// @Description Upsert the per-instance resource quota override for a stack instance. Each non-empty quantity must be a valid Kubernetes quantity (for example 500m, 2, 512Mi, 10Gi) and not negative; cpu_request must not exceed cpu_limit and memory_request must not exceed memory_limit; pod_limit must not be negative. The container_* fields (LimitRange defaultRequest, default and max per container) and ephemeral_storage_limit follow the same per-field merge: a non-empty value replaces the cluster value. Per container, request <= limit <= max, and a container default must not exceed the namespace value of the same resource. Values are trimmed. The effective quota (cluster quota of the instance's cluster merged with this override) must follow the same rules, for example "cpu_request 4 (instance override) exceeds the effective cpu_limit 2 (cluster quota)". Invalid input returns 400 with the field name. Authorization: the instance owner, admin or devops. An owner without the admin or devops role cannot set a value above the cluster quota of the instance's cluster (403, for example "cpu_limit 64 exceeds the cluster quota 16; only admin or devops can set a higher quota"); the check covers every field, also the container_* fields and ephemeral_storage_limit; a field without a cluster value has no cap, and pod_limit 0 (no limit) counts as above a cluster pod_limit. A value equal to the stored override passes, so an owner can change one field without losing an admin grant. Admin and devops may exceed the cluster quota. The check runs on write (and on clone), not on deploy. After the write, the backend applies the effective quota to the namespace of a deployed instance in the background (not for a draft or cleaning instance). The backend logs a failure, and the next deploy applies the quota.
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), instanceID)
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

		ContainerCPURequest:              strings.TrimSpace(input.ContainerCPURequest),
		ContainerCPULimit:                strings.TrimSpace(input.ContainerCPULimit),
		ContainerCPUMax:                  strings.TrimSpace(input.ContainerCPUMax),
		ContainerMemoryRequest:           strings.TrimSpace(input.ContainerMemoryRequest),
		ContainerMemoryLimit:             strings.TrimSpace(input.ContainerMemoryLimit),
		ContainerMemoryMax:               strings.TrimSpace(input.ContainerMemoryMax),
		EphemeralStorageLimit:            strings.TrimSpace(input.EphemeralStorageLimit),
		ContainerEphemeralStorageRequest: strings.TrimSpace(input.ContainerEphemeralStorageRequest),
		ContainerEphemeralStorageLimit:   strings.TrimSpace(input.ContainerEphemeralStorageLimit),
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

	// A client that leaves must not cancel the write and then skip the
	// namespace apply and the audit entry (#656).
	wctx := context.WithoutCancel(c.Request.Context())
	if err := h.overrideRepo.Upsert(wctx, override); err != nil {
		status, message := mapError(err, entityInstanceQuotaOverride)
		if status == http.StatusInternalServerError {
			slog.Error("failed to upsert quota override", logKeyIQOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Re-read to return the persisted state (includes ID, timestamps).
	saved, err := h.overrideRepo.GetByInstanceID(wctx, instanceID)
	if err != nil {
		status, message := mapError(err, entityInstanceQuotaOverride)
		if status == http.StatusInternalServerError {
			slog.Error("failed to re-read quota override after upsert", logKeyIQOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	h.applyQuotaInBackground(inst)
	c.JSON(http.StatusOK, saved)
}

// DeleteQuotaOverride godoc
// @Summary     Delete quota override for an instance
// @Description Remove the per-instance resource quota override for a stack instance. The backend then applies the cluster quota to the namespace of a deployed instance in the background (not for a draft or cleaning instance). Without a cluster quota, the namespace keeps its current ResourceQuota and LimitRange until the instance is cleaned.
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), instanceID)
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

	// See SetQuotaOverride.
	if err := h.overrideRepo.Delete(context.WithoutCancel(c.Request.Context()), instanceID); err != nil {
		status, message := mapError(err, entityInstanceQuotaOverride)
		if status == http.StatusInternalServerError {
			slog.Error("failed to delete quota override", logKeyIQOInstanceID, instanceID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	h.applyQuotaInBackground(inst)
	c.Status(http.StatusNoContent)
}
