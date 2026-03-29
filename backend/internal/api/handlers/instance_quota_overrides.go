package handlers

import (
	"log/slog"
	"net/http"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// Instance quota override handler message constants.
const (
	entityInstanceQuotaOverride = "Instance quota override"
)

const logKeyIQOInstanceID = "instance_id"



// InstanceQuotaOverrideHandler handles per-instance resource quota override endpoints.
type InstanceQuotaOverrideHandler struct {
	overrideRepo models.InstanceQuotaOverrideRepository
	instanceRepo models.StackInstanceRepository
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

	userID := middleware.GetUserIDFromContext(c)
	role := middleware.GetRoleFromContext(c)
	if inst.OwnerID != userID && role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "You do not have permission to view this instance"})
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
// @Description Upsert the per-instance resource quota override for a stack instance
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

	userID := middleware.GetUserIDFromContext(c)
	role := middleware.GetRoleFromContext(c)
	if inst.OwnerID != userID && role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "You do not have permission to modify this instance"})
		return
	}

	var input setQuotaOverrideRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	override := &models.InstanceQuotaOverride{
		StackInstanceID: instanceID,
		CPURequest:      input.CPURequest,
		CPULimit:        input.CPULimit,
		MemoryRequest:   input.MemoryRequest,
		MemoryLimit:     input.MemoryLimit,
		StorageLimit:    input.StorageLimit,
		PodLimit:        input.PodLimit,
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

	userID := middleware.GetUserIDFromContext(c)
	role := middleware.GetRoleFromContext(c)
	if inst.OwnerID != userID && role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "You do not have permission to modify this instance"})
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
