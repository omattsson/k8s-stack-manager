package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"backend/internal/api/middleware"
	"backend/internal/deployer"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// MaxBulkInstances is the maximum number of instances allowed per bulk operation.
const MaxBulkInstances = 50

// BulkOperationRequest is the request body for bulk operations.
type BulkOperationRequest struct {
	InstanceIDs []string `json:"instance_ids" binding:"required"`
}

// BulkOperationResultItem represents the result of a single instance in a bulk operation.
type BulkOperationResultItem struct {
	InstanceID   string `json:"instance_id"`
	InstanceName string `json:"instance_name"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	LogID        string `json:"log_id,omitempty"`
}

// BulkOperationResponse is the response body for bulk operations.
type BulkOperationResponse struct {
	Total     int                       `json:"total"`
	Succeeded int                       `json:"succeeded"`
	Failed    int                       `json:"failed"`
	Results   []BulkOperationResultItem `json:"results"`
}

// bulkOperationFunc is the signature for a function that operates on a single instance.
// It receives the instance and returns an optional log ID, and an error.
type bulkOperationFunc func(c *gin.Context, inst *models.StackInstance) (string, error)

// executeBulkOperation is a shared helper that validates the request, checks
// authorization per instance, and invokes the given operation for each instance.
func (h *InstanceHandler) executeBulkOperation(c *gin.Context, opName string, op bulkOperationFunc) {
	var req BulkOperationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format: instance_ids is required"})
		return
	}

	if len(req.InstanceIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "instance_ids must not be empty"})
		return
	}

	if len(req.InstanceIDs) > MaxBulkInstances {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Too many instances: maximum is %d", MaxBulkInstances)})
		return
	}

	resp := BulkOperationResponse{
		Total:   len(req.InstanceIDs),
		Results: make([]BulkOperationResultItem, 0, len(req.InstanceIDs)),
	}

	for _, id := range req.InstanceIDs {
		result := BulkOperationResultItem{InstanceID: id}

		// Fetch instance.
		inst, err := h.instanceRepo.FindByID(id)
		if err != nil {
			result.Status = "error"
			result.Error = "not found"
			resp.Failed++
			resp.Results = append(resp.Results, result)
			continue
		}
		result.InstanceName = inst.Name

		// Authorization: same rule as the single-instance endpoints
		// (owner, admin or devops; see canModifyInstance).
		if !canModifyInstance(c, inst) {
			result.Status = "error"
			result.Error = "forbidden"
			resp.Failed++
			resp.Results = append(resp.Results, result)
			continue
		}

		// Execute the operation.
		logID, err := op(c, inst)
		if err != nil {
			result.Status = "error"
			result.Error = err.Error()
			resp.Failed++
			slog.Warn("bulk "+opName+" failed for instance",
				"instance_id", id,
				"error", err,
			)
		} else {
			result.Status = "success"
			result.LogID = logID
			resp.Succeeded++
		}

		resp.Results = append(resp.Results, result)
	}

	c.JSON(http.StatusOK, resp)
}

// BulkDeploy godoc
// @Summary     Bulk deploy stack instances
// @Description Deploy multiple stack instances in a single request. Processes instances sequentially.
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       request body     BulkOperationRequest true "Instance IDs to deploy"
// @Success     200     {object} BulkOperationResponse
// @Failure     400     {object} map[string]string
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Router      /api/v1/stack-instances/bulk/deploy [post]
func (h *InstanceHandler) BulkDeploy(c *gin.Context) {
	h.executeBulkOperation(c, "deploy", func(c *gin.Context, inst *models.StackInstance) (string, error) {
		if h.deployManager == nil {
			return "", fmt.Errorf("deployment service not configured")
		}

		// Status check — same as single DeployInstance.
		switch inst.Status {
		case models.StackStatusDraft, models.StackStatusStopped, models.StackStatusError, models.StackStatusRunning:
			// OK
		default:
			return "", fmt.Errorf("cannot deploy: instance is currently %s", inst.Status)
		}

		def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
		if err != nil {
			return "", fmt.Errorf("stack definition not found")
		}

		charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
		if err != nil {
			return "", fmt.Errorf("failed to list chart configs")
		}

		if len(charts) == 0 {
			return "", fmt.Errorf("no charts configured for this stack definition")
		}

		if inst.Namespace == "" {
			return "", fmt.Errorf("instance namespace is empty")
		}

		// Same values pipeline as DeployInstance: shared values, defaults,
		// overrides, locked values and branch overrides. Fail closed.
		valuesMap, branchMap, err := h.buildChartValuesAndBranches(c.Request.Context(), inst, def, charts)
		if err != nil {
			slog.Error("bulk deploy: failed to build chart values", logKeyInstanceID, inst.ID, "error", err)
			return "", fmt.Errorf("failed to generate values")
		}

		var chartInfos []deployer.ChartDeployInfo
		for _, ch := range charts {
			chartInfos = append(chartInfos, deployer.ChartDeployInfo{
				ChartConfig: ch,
				ValuesYAML:  []byte(valuesMap[ch.ChartName]),
				Branch:      branchMap[ch.ID],
			})
		}

		// Record the rendered values like DeployInstance, so deploy preview
		// compares against what this deploy applied.
		var lastDeployedValues string
		if encoded, encErr := json.Marshal(valuesMap); encErr == nil {
			lastDeployedValues = string(encoded)
		}

		req := deployer.DeployRequest{
			Instance:           inst,
			Definition:         def,
			Charts:             chartInfos,
			LastDeployedValues: lastDeployedValues,
			UserID:             middleware.GetUserIDFromContext(c),
		}

		logID, err := h.deployManager.Deploy(hookTriggerCtx(c), req)
		if err != nil {
			return "", fmt.Errorf("failed to start deployment")
		}

		return logID, nil
	})
}

// BulkStop godoc
// @Summary     Bulk stop stack instances
// @Description Stop multiple stack instances in a single request. Processes instances sequentially.
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       request body     BulkOperationRequest true "Instance IDs to stop"
// @Success     200     {object} BulkOperationResponse
// @Failure     400     {object} map[string]string
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Router      /api/v1/stack-instances/bulk/stop [post]
func (h *InstanceHandler) BulkStop(c *gin.Context) {
	h.executeBulkOperation(c, "stop", func(c *gin.Context, inst *models.StackInstance) (string, error) {
		if h.deployManager == nil {
			return "", fmt.Errorf("deployment service not configured")
		}

		// Status check — same as single StopInstance.
		switch inst.Status {
		case models.StackStatusRunning, models.StackStatusDeploying:
			// OK
		default:
			return "", fmt.Errorf("cannot stop: instance is currently %s", inst.Status)
		}

		def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
		if err != nil {
			return "", fmt.Errorf("stack definition not found")
		}

		charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
		if err != nil {
			return "", fmt.Errorf("failed to list chart configs")
		}

		if len(charts) == 0 {
			return "", fmt.Errorf("no charts configured for this stack definition")
		}

		var chartInfos []deployer.ChartDeployInfo
		for _, ch := range charts {
			chartInfos = append(chartInfos, deployer.ChartDeployInfo{
				ChartConfig: ch,
			})
		}

		logID, err := h.deployManager.StopWithCharts(hookTriggerCtx(c), inst, chartInfos)
		if err != nil {
			return "", fmt.Errorf("failed to start stop operation")
		}

		return logID, nil
	})
}

// BulkClean godoc
// @Summary     Bulk clean stack instances
// @Description Clean multiple stack instances in a single request. Uninstalls Helm releases and deletes namespaces.
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       request body     BulkOperationRequest true "Instance IDs to clean"
// @Success     200     {object} BulkOperationResponse
// @Failure     400     {object} map[string]string
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Router      /api/v1/stack-instances/bulk/clean [post]
func (h *InstanceHandler) BulkClean(c *gin.Context) {
	h.executeBulkOperation(c, "clean", func(c *gin.Context, inst *models.StackInstance) (string, error) {
		if h.deployManager == nil {
			return "", fmt.Errorf("deployment service not configured")
		}

		// Status check — same as single CleanInstance.
		switch inst.Status {
		case models.StackStatusRunning, models.StackStatusStopped, models.StackStatusError:
			// OK
		default:
			return "", fmt.Errorf("cannot clean: instance is currently %s", inst.Status)
		}

		def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
		if err != nil {
			return "", fmt.Errorf("stack definition not found")
		}

		charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
		if err != nil {
			return "", fmt.Errorf("failed to list chart configs")
		}

		logID, err := h.deployManager.Clean(hookTriggerCtx(c), inst, charts)
		if errors.Is(err, models.ErrCleanConflict) {
			return "", errors.New(msgCleanConflict)
		}
		if err != nil {
			return "", fmt.Errorf("failed to start clean operation")
		}

		return logID, nil
	})
}

// BulkDelete godoc
// @Summary     Bulk delete stack instances
// @Description Delete multiple stack instances in a single request. Processes instances sequentially with the rules of DELETE /stack-instances/{id}: an instance with cluster resources (running, partial, stopped, error) is cleaned first (Helm uninstall and namespace delete) and deleted when the clean completes — its result has status "success" and the log_id of the clean. "success" with a log_id means that the clean started; the row is removed when the clean finishes (WebSocket message instance.deleted). A failed clean keeps the instance with status error. A draft instance is deleted at once. An instance with an operation in progress (checked before pre-instance-delete fires), one that a concurrent delete already started, or one that a pre-instance-delete hook rejects, gets status "error".
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       request body     BulkOperationRequest true "Instance IDs to delete"
// @Success     200     {object} BulkOperationResponse
// @Failure     400     {object} map[string]string
// @Failure     401     {object} map[string]string
// @Failure     403     {object} map[string]string
// @Router      /api/v1/stack-instances/bulk/delete [post]
func (h *InstanceHandler) BulkDelete(c *gin.Context) {
	h.executeBulkOperation(c, "delete", func(c *gin.Context, inst *models.StackInstance) (string, error) {
		// The same code path as the single delete (status rules, clean
		// first, hooks, followers, notifications).
		logID, delErr := h.deleteInstance(c, inst)
		if delErr != nil {
			return "", delErr
		}
		return logID, nil
	})
}
