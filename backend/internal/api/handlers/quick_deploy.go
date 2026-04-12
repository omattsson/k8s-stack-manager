package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/internal/websocket"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// QuickDeployHandler composes template instantiation, instance creation,
// optional branch overrides, and deployment into a single API call.
type QuickDeployHandler struct {
	templateRepo       models.StackTemplateRepository
	templateChartRepo  models.TemplateChartConfigRepository
	definitionRepo     models.StackDefinitionRepository
	chartConfigRepo    models.ChartConfigRepository
	instanceRepo       models.StackInstanceRepository
	branchOverrideRepo models.ChartBranchOverrideRepository
	overrideRepo       models.ValueOverrideRepository
	valuesGen          *helm.ValuesGenerator
	deployManager      *deployer.Manager
	userRepo           models.UserRepository
	deployLogRepo      models.DeploymentLogRepository
	auditRepo          models.AuditLogRepository
	hub                websocket.BroadcastSender
	registry           *cluster.Registry
	k8sWatcher         *k8s.Watcher
	defaultTTLMinutes  int
	txRunner           database.TxRunner
}

// NewQuickDeployHandler creates a new QuickDeployHandler with all required dependencies.
func NewQuickDeployHandler(
	templateRepo models.StackTemplateRepository,
	templateChartRepo models.TemplateChartConfigRepository,
	definitionRepo models.StackDefinitionRepository,
	chartConfigRepo models.ChartConfigRepository,
	instanceRepo models.StackInstanceRepository,
	branchOverrideRepo models.ChartBranchOverrideRepository,
	overrideRepo models.ValueOverrideRepository,
	valuesGen *helm.ValuesGenerator,
	deployManager *deployer.Manager,
	userRepo models.UserRepository,
	deployLogRepo models.DeploymentLogRepository,
	auditRepo models.AuditLogRepository,
	hub websocket.BroadcastSender,
	registry *cluster.Registry,
	k8sWatcher *k8s.Watcher,
	defaultTTLMinutes int,
) *QuickDeployHandler {
	return &QuickDeployHandler{
		templateRepo:       templateRepo,
		templateChartRepo:  templateChartRepo,
		definitionRepo:     definitionRepo,
		chartConfigRepo:    chartConfigRepo,
		instanceRepo:       instanceRepo,
		branchOverrideRepo: branchOverrideRepo,
		overrideRepo:       overrideRepo,
		valuesGen:          valuesGen,
		deployManager:      deployManager,
		userRepo:           userRepo,
		deployLogRepo:      deployLogRepo,
		auditRepo:          auditRepo,
		hub:                hub,
		registry:           registry,
		k8sWatcher:         k8sWatcher,
		defaultTTLMinutes:  defaultTTLMinutes,
	}
}

// SetTxRunner sets an optional TxRunner for transactional multi-entity operations.
func (h *QuickDeployHandler) SetTxRunner(tx database.TxRunner) {
	h.txRunner = tx
}

// quickDeployRequest is the request body for Quick Deploy.
type quickDeployRequest struct {
	InstanceName        string            `json:"instance_name"`
	InstanceDescription string            `json:"instance_description"`
	Branch              string            `json:"branch"`
	ClusterID           string            `json:"cluster_id"`
	TTLMinutes          *int              `json:"ttl_minutes"`
	BranchOverrides     map[string]string `json:"branch_overrides"`
}

// quickDeployResponse is the response for a successful Quick Deploy.
type quickDeployResponse struct {
	Instance   *models.StackInstance   `json:"instance"`
	Definition *models.StackDefinition `json:"definition"`
	LogID      string                  `json:"log_id"`
}

// QuickDeploy godoc
// @Summary     Quick deploy from a template
// @Description Instantiate a template, create an instance, set branch overrides, and trigger deployment in a single call
// @Tags        templates
// @Accept      json
// @Produce     json
// @Param       id   path     string             true "Template ID"
// @Param       body body     quickDeployRequest  true "Quick deploy options"
// @Success     202  {object} quickDeployResponse
// @Failure     400  {object} map[string]string
// @Failure     404  {object} map[string]string
// @Failure     500  {object} map[string]string
// @Security    BearerAuth
// @Router      /api/v1/templates/{id}/quick-deploy [post]
func (h *QuickDeployHandler) QuickDeploy(c *gin.Context) {
	templateID := c.Param("id")
	if templateID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Template ID is required"})
		return
	}

	var req quickDeployRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	if req.InstanceName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "instance_name is required"})
		return
	}

	// 1. Fetch and verify template.
	tmpl, err := h.templateRepo.FindByID(templateID)
	if err != nil {
		status, message := mapError(err, entityTemplate)
		c.JSON(status, gin.H{"error": message})
		return
	}

	if !tmpl.IsPublished {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Template is not published"})
		return
	}

	// Resolve branch: use request branch, fall back to template default.
	branch := req.Branch
	if branch == "" {
		branch = tmpl.DefaultBranch
	}
	if branch == "" {
		branch = "master"
	}

	userID := middleware.GetUserIDFromContext(c)
	username := middleware.GetUsernameFromContext(c)
	now := time.Now().UTC()

	// 2. Build definition and instance models up front for validation before persistence.
	def := &models.StackDefinition{
		ID:                    uuid.New().String(),
		Name:                  req.InstanceName,
		Description:           req.InstanceDescription,
		OwnerID:               userID,
		SourceTemplateID:      tmpl.ID,
		SourceTemplateVersion: tmpl.Version,
		DefaultBranch:         tmpl.DefaultBranch,
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	// Fetch template charts (needed for chart config creation).
	templateCharts, err := h.templateChartRepo.ListByTemplate(tmpl.ID)
	if err != nil {
		status, message := mapError(err, "Template charts")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Pre-build chart config models.
	chartConfigs := make([]models.ChartConfig, 0, len(templateCharts))
	for _, tc := range templateCharts {
		chartConfigs = append(chartConfigs, models.ChartConfig{
			ID:                uuid.New().String(),
			StackDefinitionID: def.ID,
			ChartName:         tc.ChartName,
			RepositoryURL:     tc.RepositoryURL,
			SourceRepoURL:     tc.SourceRepoURL,
			ChartPath:         tc.ChartPath,
			ChartVersion:      tc.ChartVersion,
			DefaultValues:     tc.DefaultValues,
			DeployOrder:       tc.DeployOrder,
			CreatedAt:         now,
		})
	}

	// 3. Build and validate stack instance (no DB writes yet).
	inst := &models.StackInstance{
		ID:                uuid.New().String(),
		StackDefinitionID: def.ID,
		Name:              req.InstanceName,
		OwnerID:           userID,
		Branch:            branch,
		ClusterID:         req.ClusterID,
		Status:            models.StackStatusDeploying,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	// Apply TTL — nil means "use default", explicit 0 means "no expiry".
	var ttl int
	if req.TTLMinutes == nil && h.defaultTTLMinutes > 0 {
		ttl = h.defaultTTLMinutes
	} else if req.TTLMinutes != nil {
		ttl = *req.TTLMinutes
	}
	if ttl < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "TTL must not be negative"})
		return
	}
	if ttl > MaxTTLMinutes {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("TTL must not exceed %d minutes (30 days)", MaxTTLMinutes)})
		return
	}
	inst.TTLMinutes = ttl
	if ttl > 0 {
		exp := now.Add(time.Duration(ttl) * time.Minute)
		inst.ExpiresAt = &exp
	}

	// Resolve cluster ID via registry.
	if h.registry != nil {
		if inst.ClusterID == "" {
			resolved, resolveErr := h.registry.ResolveClusterID("")
			if resolveErr != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "No default cluster configured; specify cluster_id"})
				return
			}
			inst.ClusterID = resolved
		} else if !h.registry.ClusterExists(inst.ClusterID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown cluster_id"})
			return
		}
	}

	// Auto-generate namespace.
	inst.Namespace = buildNamespace(inst.Name, username)

	if err := inst.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check namespace uniqueness.
	existingInst, nsErr := h.instanceRepo.FindByNamespace(inst.Namespace)
	if nsErr != nil && !errors.Is(nsErr, dberrors.ErrNotFound) {
		slog.Error("Failed to check namespace uniqueness",
			"namespace", inst.Namespace,
			"error", nsErr,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	if existingInst != nil {
		suggestions := generateNameSuggestions(inst.Name)
		c.JSON(http.StatusConflict, NamespaceConflictResponse{
			Error:       "namespace already exists",
			Message:     fmt.Sprintf("Namespace %q is already in use", inst.Namespace),
			Suggestions: suggestions,
		})
		return
	}

	// Persist definition + chart configs + instance.
	if h.txRunner == nil {
		slog.Error("txRunner not configured for QuickDeploy")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	txErr := h.txRunner.RunInTx(func(repos database.TxRepos) error {
		if err := repos.StackDefinition.Create(def); err != nil {
			return err
		}
		for i := range chartConfigs {
			if err := repos.ChartConfig.Create(&chartConfigs[i]); err != nil {
				return err
			}
		}
		return repos.StackInstance.Create(inst)
	})
	if txErr != nil {
		slog.Error("Quick deploy transaction failed", "error", txErr)
		status, message := mapError(txErr, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// 4. Set branch overrides.
	if len(req.BranchOverrides) > 0 && h.branchOverrideRepo != nil {
		for chartConfigID, branchName := range req.BranchOverrides {
			bo := &models.ChartBranchOverride{
				ID:              uuid.New().String(),
				StackInstanceID: inst.ID,
				ChartConfigID:   chartConfigID,
				Branch:          branchName,
				UpdatedAt:       now,
			}
			if err := h.branchOverrideRepo.Set(bo); err != nil {
				slog.Error("Failed to set branch override during quick deploy",
					"instance_id", inst.ID,
					"chart_config_id", chartConfigID,
					"error", err,
				)
			}
		}
	}

	// 5. Trigger deploy.
	logID, deployErr := h.triggerDeploy(c, inst, def, chartConfigs, templateCharts, username)

	if deployErr != nil {
		slog.Error("Quick deploy failed during deployment phase",
			"instance_id", inst.ID,
			"error", deployErr,
		)
		inst.Status = models.StackStatusError
		inst.ErrorMessage = "Deployment failed"
		_ = h.instanceRepo.Update(inst)
	}

	// 6. Audit log.
	if h.auditRepo != nil {
		if auditErr := h.auditRepo.Create(&models.AuditLog{
			ID:         uuid.New().String(),
			UserID:     userID,
			Username:   username,
			Action:     "quick_deploy",
			EntityType: "stack_instance",
			EntityID:   inst.ID,
			Details:    fmt.Sprintf("Quick deployed from template %s", tmpl.Name),
			Timestamp:  now,
		}); auditErr != nil {
			slog.Error("Failed to create audit log for quick deploy", "instance_id", inst.ID, "error", auditErr)
		}
	}

	c.JSON(http.StatusAccepted, quickDeployResponse{
		Instance:   inst,
		Definition: def,
		LogID:      logID,
	})
}

// triggerDeploy generates values and starts the deployment, mirroring DeployInstance logic.
func (h *QuickDeployHandler) triggerDeploy(
	c *gin.Context,
	inst *models.StackInstance,
	def *models.StackDefinition,
	chartConfigs []models.ChartConfig,
	templateCharts []models.TemplateChartConfig,
	ownerName string,
) (string, error) {
	if h.deployManager == nil {
		return "", fmt.Errorf("deployment service not configured")
	}

	if len(chartConfigs) == 0 {
		return "", fmt.Errorf("no charts configured for this stack definition")
	}

	// Build locked values map from template charts.
	lockedMap := make(map[string]string)
	for _, tc := range templateCharts {
		lockedMap[tc.ChartName] = tc.LockedValues
	}

	// Build overrides map (empty for quick deploy — no value overrides yet).
	overridesMap := make(map[string]string)
	if h.overrideRepo != nil {
		overrides, err := h.overrideRepo.ListByInstance(inst.ID)
		if err == nil {
			for _, ov := range overrides {
				overridesMap[ov.ChartConfigID] = ov.Values
			}
		}
	}

	// Build per-chart branch override map.
	branchMap := make(map[string]string)
	if h.branchOverrideRepo != nil {
		branchOverrides, err := h.branchOverrideRepo.List(inst.ID)
		if err == nil {
			for _, bo := range branchOverrides {
				branchMap[bo.ChartConfigID] = bo.Branch
			}
		}
	}

	templateVars := helm.TemplateVars{
		Branch:       inst.Branch,
		Namespace:    inst.Namespace,
		InstanceName: inst.Name,
		StackName:    def.Name,
		Owner:        ownerName,
	}

	var chartInfos []deployer.ChartDeployInfo
	for _, ch := range chartConfigs {
		params := helm.GenerateParams{
			ChartName:      ch.ChartName,
			DefaultValues:  ch.DefaultValues,
			LockedValues:   lockedMap[ch.ChartName],
			OverrideValues: overridesMap[ch.ID],
			ChartBranch:    branchMap[ch.ID],
			TemplateVars:   templateVars,
		}

		yamlData, err := h.valuesGen.GenerateValues(c.Request.Context(), params)
		if err != nil {
			return "", fmt.Errorf("failed to generate values for chart %s: %w", ch.ChartName, err)
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

	return h.deployManager.Deploy(c.Request.Context(), req)
}

// truncate shortens a string to maxLen runes, preserving UTF-8 boundaries.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}
