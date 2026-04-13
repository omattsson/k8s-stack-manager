package handlers

import (
	"fmt"
	"log/slog"
	"net/http"

	"backend/internal/api/middleware"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
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

	userID := middleware.GetUserIDFromContext(c)
	role := middleware.GetRoleFromContext(c)
	isPrivileged := role == "admin" || role == "devops"

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

		// Authorization: non-privileged users can only operate on their own instances.
		if !isPrivileged && inst.OwnerID != userID {
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

		// Build locked values map from template.
		lockedMap := make(map[string]string)
		if def.SourceTemplateID != "" && h.templateChartRepo != nil {
			templateCharts, tcErr := h.templateChartRepo.ListByTemplate(def.SourceTemplateID)
			if tcErr != nil {
				return "", fmt.Errorf("failed to list template chart configs")
			}
			for _, tc := range templateCharts {
				lockedMap[tc.ChartName] = tc.LockedValues
			}
		}

		// Build overrides map.
		overridesMap := make(map[string]string)
		overrides, err := h.overrideRepo.ListByInstance(inst.ID)
		if err != nil {
			return "", fmt.Errorf("failed to list value overrides")
		}
		for _, ov := range overrides {
			overridesMap[ov.ChartConfigID] = ov.Values
		}

		// Build per-chart branch override map.
		branchMap := make(map[string]string)
		if h.branchOverrideRepo != nil {
			branchOverrides, boErr := h.branchOverrideRepo.List(inst.ID)
			if boErr != nil {
				return "", fmt.Errorf("failed to list branch overrides")
			}
			for _, bo := range branchOverrides {
				branchMap[bo.ChartConfigID] = bo.Branch
			}
		}

		if inst.Namespace == "" {
			return "", fmt.Errorf("instance namespace is empty")
		}

		ownerName := resolveOwnerName(h.userRepo, inst.OwnerID)

		templateVars := helm.TemplateVars{
			Branch:       inst.Branch,
			ImageTag:     helm.SanitizeImageTag(inst.Branch),
			Namespace:    inst.Namespace,
			InstanceName: inst.Name,
			StackName:    def.Name,
			Owner:        ownerName,
		}

		// Generate values YAML for each chart.
		var chartInfos []deployer.ChartDeployInfo
		for _, ch := range charts {
			params := helm.GenerateParams{
				ChartName:      ch.ChartName,
				DefaultValues:  ch.DefaultValues,
				LockedValues:   lockedMap[ch.ChartName],
				OverrideValues: overridesMap[ch.ID],
				ChartBranch:    branchMap[ch.ID],
				TemplateVars:   templateVars,
			}

			yamlData, genErr := h.valuesGen.GenerateValues(c.Request.Context(), params)
			if genErr != nil {
				return "", fmt.Errorf("failed to generate values")
			}

			chartInfos = append(chartInfos, deployer.ChartDeployInfo{
				ChartConfig: ch,
				ValuesYAML:  yamlData,
			})
		}

		req := deployer.DeployRequest{
			Instance:   inst,
			Definition: def,
			Charts:     chartInfos,
		}

		logID, err := h.deployManager.Deploy(c.Request.Context(), req)
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

		logID, err := h.deployManager.StopWithCharts(c.Request.Context(), inst, chartInfos)
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

		logID, err := h.deployManager.Clean(c.Request.Context(), inst, charts)
		if err != nil {
			return "", fmt.Errorf("failed to start clean operation")
		}

		return logID, nil
	})
}

// BulkDelete godoc
// @Summary     Bulk delete stack instances
// @Description Delete multiple stack instances in a single request. Processes instances sequentially.
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
	h.executeBulkOperation(c, "delete", func(_ *gin.Context, inst *models.StackInstance) (string, error) {
		if h.txRunner != nil {
			// Transactional path — branch override cleanup + instance delete are atomic.
			txErr := h.txRunner.RunInTx(func(repos database.TxRepos) error {
				if err := repos.BranchOverride.DeleteByInstance(inst.ID); err != nil {
					return err
				}
				return repos.StackInstance.Delete(inst.ID)
			})
			if txErr != nil {
				slog.Error("failed to delete instance in bulk operation", "instance_id", inst.ID, "error", txErr)
				return "", fmt.Errorf("failed to delete instance")
			}
			return "", nil
		}

		slog.Error("txRunner not configured for BulkDelete", "instance_id", inst.ID)
		return "", fmt.Errorf("failed to delete instance")
	})
}
