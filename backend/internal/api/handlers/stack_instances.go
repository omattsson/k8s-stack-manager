package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/cluster"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// NamespaceConflictResponse is the response returned when a namespace is already in use.
type NamespaceConflictResponse struct {
	Error       string   `json:"error"`
	Message     string   `json:"message"`
	Suggestions []string `json:"suggestions"`
}

// rfc1123InvalidChars matches any character not allowed in an RFC1123 label.
var rfc1123InvalidChars = regexp.MustCompile(`[^a-z0-9-]`)

// rfc1123ConsecutiveDashes collapses multiple consecutive dashes into one.
var rfc1123ConsecutiveDashes = regexp.MustCompile(`-{2,}`)

// sanitizeRFC1123Label sanitizes a string into a valid RFC1123 DNS label:
// lowercase, only [a-z0-9-], collapse consecutive dashes, trim leading/trailing
// dashes, max 63 chars. Returns "default" if the result would be empty.
func sanitizeRFC1123Label(s string) string {
	s = strings.ToLower(s)
	s = rfc1123InvalidChars.ReplaceAllString(s, "-")
	s = rfc1123ConsecutiveDashes.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = s[:63]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		return "default"
	}
	return s
}

// buildNamespace constructs a namespace in the form stack-{instance}-{owner},
// sanitizing both parts and truncating to fit within 63 characters.
func buildNamespace(instancePart, ownerPart string) string {
	prefix := "stack-"
	sanitizedOwner := sanitizeRFC1123Label(ownerPart)
	// Reserve room for "stack-" + "-" + owner
	maxInstanceLen := 63 - len(prefix) - 1 - len(sanitizedOwner)
	sanitizedInstance := sanitizeRFC1123Label(instancePart)
	if maxInstanceLen < 1 {
		maxInstanceLen = 1
	}
	if len(sanitizedInstance) > maxInstanceLen {
		sanitizedInstance = sanitizedInstance[:maxInstanceLen]
		sanitizedInstance = strings.TrimRight(sanitizedInstance, "-")
	}
	namespace := fmt.Sprintf("%s%s-%s", prefix, sanitizedInstance, sanitizedOwner)
	if len(namespace) > 63 {
		namespace = namespace[:63]
		namespace = strings.TrimRight(namespace, "-")
	}
	return namespace
}

// InstanceHandler handles stack instance, value override, and values export endpoints.
type InstanceHandler struct {
	instanceRepo       models.StackInstanceRepository
	overrideRepo       models.ValueOverrideRepository
	branchOverrideRepo models.ChartBranchOverrideRepository
	definitionRepo     models.StackDefinitionRepository
	chartConfigRepo    models.ChartConfigRepository
	templateRepo       models.StackTemplateRepository
	templateChartRepo  models.TemplateChartConfigRepository
	valuesGen          *helm.ValuesGenerator
	userRepo           models.UserRepository
	deployManager      *deployer.Manager
	k8sWatcher         *k8s.Watcher
	registry           *cluster.Registry
	deployLogRepo      models.DeploymentLogRepository
}

// NewInstanceHandler creates a new InstanceHandler.
func NewInstanceHandler(
	instanceRepo models.StackInstanceRepository,
	overrideRepo models.ValueOverrideRepository,
	branchOverrideRepo models.ChartBranchOverrideRepository,
	definitionRepo models.StackDefinitionRepository,
	chartConfigRepo models.ChartConfigRepository,
	templateRepo models.StackTemplateRepository,
	templateChartRepo models.TemplateChartConfigRepository,
	valuesGen *helm.ValuesGenerator,
	userRepo models.UserRepository,
) *InstanceHandler {
	return &InstanceHandler{
		instanceRepo:       instanceRepo,
		overrideRepo:       overrideRepo,
		branchOverrideRepo: branchOverrideRepo,
		definitionRepo:     definitionRepo,
		chartConfigRepo:    chartConfigRepo,
		templateRepo:       templateRepo,
		templateChartRepo:  templateChartRepo,
		valuesGen:          valuesGen,
		userRepo:           userRepo,
	}
}

// NewInstanceHandlerWithDeployer creates an InstanceHandler with Phase 3 deployment capabilities.
func NewInstanceHandlerWithDeployer(
	instanceRepo models.StackInstanceRepository,
	overrideRepo models.ValueOverrideRepository,
	branchOverrideRepo models.ChartBranchOverrideRepository,
	definitionRepo models.StackDefinitionRepository,
	chartConfigRepo models.ChartConfigRepository,
	templateRepo models.StackTemplateRepository,
	templateChartRepo models.TemplateChartConfigRepository,
	valuesGen *helm.ValuesGenerator,
	userRepo models.UserRepository,
	deployManager *deployer.Manager,
	k8sWatcher *k8s.Watcher,
	registry *cluster.Registry,
	deployLogRepo models.DeploymentLogRepository,
) *InstanceHandler {
	return &InstanceHandler{
		instanceRepo:       instanceRepo,
		overrideRepo:       overrideRepo,
		branchOverrideRepo: branchOverrideRepo,
		definitionRepo:     definitionRepo,
		chartConfigRepo:    chartConfigRepo,
		templateRepo:       templateRepo,
		templateChartRepo:  templateChartRepo,
		valuesGen:          valuesGen,
		userRepo:           userRepo,
		deployManager:      deployManager,
		k8sWatcher:         k8sWatcher,
		registry:           registry,
		deployLogRepo:      deployLogRepo,
	}
}

// ListInstances godoc
// @Summary     List stack instances
// @Description List all stack instances, optionally filtered by owner
// @Tags        stack-instances
// @Produce     json
// @Param       owner query    string false "Filter by owner (use 'me' for current user)"
// @Success     200   {array}  models.StackInstance
// @Failure     500   {object} map[string]string
// @Router      /api/v1/stack-instances [get]
func (h *InstanceHandler) ListInstances(c *gin.Context) {
	owner := c.Query("owner")

	var instances []models.StackInstance
	var err error
	if owner == "me" {
		userID := middleware.GetUserIDFromContext(c)
		instances, err = h.instanceRepo.ListByOwner(userID)
	} else {
		instances, err = h.instanceRepo.List()
	}
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, instances)
}

// CreateInstance godoc
// @Summary     Create a stack instance
// @Description Create a new stack instance from a definition
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       instance body     models.StackInstance true "Instance object"
// @Success     201      {object} models.StackInstance
// @Failure     400      {object} map[string]string
// @Failure     409      {object} NamespaceConflictResponse "Namespace already exists"
// @Router      /api/v1/stack-instances [post]
func (h *InstanceHandler) CreateInstance(c *gin.Context) {
	var inst models.StackInstance
	if err := c.ShouldBindJSON(&inst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	inst.ID = uuid.New().String()
	inst.OwnerID = middleware.GetUserIDFromContext(c)
	inst.Status = models.StackStatusDraft
	now := time.Now().UTC()
	inst.CreatedAt = now
	inst.UpdatedAt = now

	if inst.Branch == "" {
		inst.Branch = "master"
	}

	// Resolve or validate ClusterID using the registry when available.
	// - If empty, resolve to the current default cluster so the persisted
	//   value is explicit and won't shift if the default changes.
	// - If non-empty, validate that it refers to a known cluster to avoid
	//   persisting invalid references that will only fail at deploy time.
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
	owner := middleware.GetUsernameFromContext(c)
	if inst.Namespace == "" {
		inst.Namespace = buildNamespace(inst.Name, owner)
	}

	if err := inst.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check namespace uniqueness.
	// NOTE: This is a TOCTOU check — concurrent creates can still race past it.
	// For strict uniqueness, a storage-level constraint (e.g. unique index or
	// namespace-reservation entity) would be needed.
	if h.checkNamespaceUniqueness(c, inst.Namespace, inst.Name) {
		return
	}

	// Verify definition exists.
	if _, err := h.definitionRepo.FindByID(inst.StackDefinitionID); err != nil {
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if err := h.instanceRepo.Create(&inst); err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusCreated, inst)
}

// GetInstance godoc
// @Summary     Get a stack instance
// @Description Get a stack instance by ID
// @Tags        stack-instances
// @Produce     json
// @Param       id  path     string true "Instance ID"
// @Success     200 {object} models.StackInstance
// @Failure     404 {object} map[string]string
// @Router      /api/v1/stack-instances/{id} [get]
func (h *InstanceHandler) GetInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance ID is required"})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, inst)
}

// UpdateInstance godoc
// @Summary     Update a stack instance
// @Description Update a stack instance (branch, name, etc.)
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id       path     string               true "Instance ID"
// @Param       instance body     models.StackInstance   true "Instance object"
// @Success     200      {object} models.StackInstance
// @Failure     400      {object} map[string]string
// @Failure     404      {object} map[string]string
// @Router      /api/v1/stack-instances/{id} [put]
func (h *InstanceHandler) UpdateInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance ID is required"})
		return
	}

	existing, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	var update models.StackInstance
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	existing.Name = update.Name
	existing.Branch = update.Branch
	existing.Namespace = update.Namespace
	existing.UpdatedAt = time.Now().UTC()

	if err := existing.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.instanceRepo.Update(existing); err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, existing)
}

// DeleteInstance godoc
// @Summary     Delete a stack instance
// @Description Delete a stack instance
// @Tags        stack-instances
// @Produce     json
// @Param       id  path     string true "Instance ID"
// @Success     204 "No Content"
// @Failure     404 {object} map[string]string
// @Router      /api/v1/stack-instances/{id} [delete]
func (h *InstanceHandler) DeleteInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance ID is required"})
		return
	}

	// Clean up per-chart branch overrides before deleting.
	if h.branchOverrideRepo != nil {
		_ = h.branchOverrideRepo.DeleteByInstance(id)
	}

	if err := h.instanceRepo.Delete(id); err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}

// CloneInstance godoc
// @Summary     Clone a stack instance
// @Description Create a new stack instance as a copy of an existing one
// @Tags        stack-instances
// @Produce     json
// @Param       id  path     string true "Instance ID"
// @Success     201 {object} models.StackInstance
// @Failure     404 {object} map[string]string
// @Failure     409 {object} NamespaceConflictResponse "Namespace already exists"
// @Router      /api/v1/stack-instances/{id}/clone [post]
func (h *InstanceHandler) CloneInstance(c *gin.Context) {
	id := c.Param("id")
	source, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	now := time.Now().UTC()
	ownerID := middleware.GetUserIDFromContext(c)
	ownerName := middleware.GetUsernameFromContext(c)

	// Truncate name before adding suffix to stay within the 50-char limit.
	// Use rune slicing to avoid splitting multi-byte UTF-8 characters.
	copySuffix := " (Copy)"
	baseRunes := []rune(source.Name)
	maxBase := models.MaxInstanceNameLength - len(copySuffix)
	if maxBase < 0 {
		maxBase = 0
	}
	if len(baseRunes) > maxBase {
		baseRunes = baseRunes[:maxBase]
	}
	cloneName := string(baseRunes) + copySuffix
	cloneNamespace := buildNamespace(cloneName, ownerName)

	clone := &models.StackInstance{
		ID:                uuid.New().String(),
		StackDefinitionID: source.StackDefinitionID,
		Name:              cloneName,
		Namespace:         cloneNamespace,
		OwnerID:           ownerID,
		Branch:            source.Branch,
		Status:            models.StackStatusDraft,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := clone.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check namespace uniqueness.
	if h.checkNamespaceUniqueness(c, clone.Namespace, cloneName) {
		return
	}

	if err := h.instanceRepo.Create(clone); err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Copy value overrides.
	overrides, err := h.overrideRepo.ListByInstance(source.ID)
	if err == nil {
		for _, ov := range overrides {
			clonedOV := &models.ValueOverride{
				ID:              uuid.New().String(),
				StackInstanceID: clone.ID,
				ChartConfigID:   ov.ChartConfigID,
				Values:          ov.Values,
				UpdatedAt:       now,
			}
			// Best-effort — don't fail the clone if override copy fails.
			_ = h.overrideRepo.Create(clonedOV)
		}
	}

	c.JSON(http.StatusCreated, clone)
}

// ExportChartValues godoc
// @Summary     Export chart values
// @Description Generate and export merged values.yaml for a specific chart
// @Tags        stack-instances
// @Produce     application/x-yaml
// @Param       id      path     string true "Instance ID"
// @Param       chartId path     string true "Chart config ID"
// @Success     200     {string} string "YAML content"
// @Failure     404     {object} map[string]string
// @Failure     500     {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/values/{chartId} [get]
func (h *InstanceHandler) ExportChartValues(c *gin.Context) {
	instanceID := c.Param("id")
	chartID := c.Param("chartId")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	chart, err := h.chartConfigRepo.FindByID(chartID)
	if err != nil {
		status, message := mapError(err, "Chart config")
		c.JSON(status, gin.H{"error": message})
		return
	}

	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Get locked values from the source template, if any.
	var lockedValues string
	if def.SourceTemplateID != "" && h.templateChartRepo != nil {
		templateCharts, err := h.templateChartRepo.ListByTemplate(def.SourceTemplateID)
		if err == nil {
			for _, tc := range templateCharts {
				if tc.ChartName == chart.ChartName {
					lockedValues = tc.LockedValues
					break
				}
			}
		}
	}

	// Get value overrides.
	var overrideValues string
	override, err := h.overrideRepo.FindByInstanceAndChart(instanceID, chartID)
	if err == nil && override != nil {
		overrideValues = override.Values
	}

	// Resolve owner username for template vars.
	ownerName := resolveOwnerName(h.userRepo, inst.OwnerID)

	params := helm.GenerateParams{
		ChartName:      chart.ChartName,
		DefaultValues:  chart.DefaultValues,
		LockedValues:   lockedValues,
		OverrideValues: overrideValues,
		TemplateVars: helm.TemplateVars{
			Branch:       inst.Branch,
			Namespace:    inst.Namespace,
			InstanceName: inst.Name,
			StackName:    def.Name,
			Owner:        ownerName,
		},
	}

	yamlData, err := h.valuesGen.GenerateValues(c.Request.Context(), params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.Data(http.StatusOK, "application/x-yaml", yamlData)
}

// ExportAllValues godoc
// @Summary     Export all chart values
// @Description Generate and export merged values for all charts as a zip archive
// @Tags        stack-instances
// @Produce     application/zip
// @Param       id  path     string true "Instance ID"
// @Success     200 {file}   file   "ZIP archive"
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/values [get]
func (h *InstanceHandler) ExportAllValues(c *gin.Context) {
	instanceID := c.Param("id")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		status, message := mapError(err, "Chart configs")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Build locked values map from template.
	lockedMap := make(map[string]string) // chartName → lockedValues
	if def.SourceTemplateID != "" && h.templateChartRepo != nil {
		templateCharts, err := h.templateChartRepo.ListByTemplate(def.SourceTemplateID)
		if err == nil {
			for _, tc := range templateCharts {
				lockedMap[tc.ChartName] = tc.LockedValues
			}
		}
	}

	// Build overrides map.
	overridesMap := make(map[string]string) // chartConfigID → values
	overrides, err := h.overrideRepo.ListByInstance(instanceID)
	if err == nil {
		for _, ov := range overrides {
			overridesMap[ov.ChartConfigID] = ov.Values
		}
	}

	ownerName := resolveOwnerName(h.userRepo, inst.OwnerID)

	var chartValues []helm.ChartValues
	for _, ch := range charts {
		chartValues = append(chartValues, helm.ChartValues{
			ChartName:      ch.ChartName,
			DefaultValues:  ch.DefaultValues,
			LockedValues:   lockedMap[ch.ChartName],
			OverrideValues: overridesMap[ch.ID],
		})
	}

	params := helm.GenerateAllParams{
		Charts: chartValues,
		TemplateVars: helm.TemplateVars{
			Branch:       inst.Branch,
			Namespace:    inst.Namespace,
			InstanceName: inst.Name,
			StackName:    def.Name,
			Owner:        ownerName,
		},
	}

	allValues, err := h.valuesGen.ExportAsZip(c.Request.Context(), params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s-values.zip", inst.Name))
	c.Data(http.StatusOK, "application/zip", allValues)
}

// DeployInstance godoc
// @Summary     Deploy a stack instance
// @Description Trigger Helm deployment for a stack instance
// @Tags        stack-instances
// @Produce     json
// @Param       id path string true "Instance ID"
// @Success     202 {object} map[string]string "Deployment started"
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string "Already deploying"
// @Router      /api/v1/stack-instances/{id}/deploy [post]
func (h *InstanceHandler) DeployInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance ID is required"})
		return
	}

	if h.deployManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Deployment service not configured"})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Allow deploy from draft, stopped, error, or running (upgrade).
	switch inst.Status {
	case models.StackStatusDraft, models.StackStatusStopped, models.StackStatusError, models.StackStatusRunning:
		// OK — running triggers a helm upgrade with the latest values.
	case models.StackStatusDeploying, models.StackStatusQueued, models.StackStatusStopping:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Cannot deploy: instance is currently %s", inst.Status)})
		return
	default:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Cannot deploy: instance is in state %s", inst.Status)})
		return
	}

	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		status, message := mapError(err, "Chart configs")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if len(charts) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No charts configured for this stack definition"})
		return
	}

	// Build locked values map from template.
	lockedMap := make(map[string]string)
	if def.SourceTemplateID != "" && h.templateChartRepo != nil {
		templateCharts, err := h.templateChartRepo.ListByTemplate(def.SourceTemplateID)
		if err != nil {
			slog.Error("Failed to list template chart configs",
				"template_id", def.SourceTemplateID,
				"instance_id", id,
				"error", err,
			)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
			return
		}
		for _, tc := range templateCharts {
			lockedMap[tc.ChartName] = tc.LockedValues
		}
	}

	// Build overrides map.
	overridesMap := make(map[string]string)
	overrides, err := h.overrideRepo.ListByInstance(inst.ID)
	if err != nil {
		slog.Error("Failed to list value overrides",
			"instance_id", id,
			"error", err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}
	for _, ov := range overrides {
		overridesMap[ov.ChartConfigID] = ov.Values
	}

	// Build per-chart branch override map.
	branchMap := make(map[string]string)
	if h.branchOverrideRepo != nil {
		branchOverrides, err := h.branchOverrideRepo.List(inst.ID)
		if err != nil {
			slog.Error("Failed to list branch overrides",
				"instance_id", id,
				"error", err,
			)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
			return
		}
		for _, bo := range branchOverrides {
			branchMap[bo.ChartConfigID] = bo.Branch
		}
	}

	if inst.Namespace == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance namespace is empty"})
		return
	}

	ownerName := resolveOwnerName(h.userRepo, inst.OwnerID)

	templateVars := helm.TemplateVars{
		Branch:       inst.Branch,
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

		yamlData, err := h.valuesGen.GenerateValues(c.Request.Context(), params)
		if err != nil {
			slog.Error("Failed to generate values for chart",
				"chart", ch.ChartName,
				"instance_id", id,
				"error", err,
			)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
			return
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
		Owner:      ownerName,
	}

	logID, err := h.deployManager.Deploy(c.Request.Context(), req)
	if err != nil {
		slog.Error("Failed to start deployment",
			"instance_id", id,
			"error", err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"log_id": logID, "message": "Deployment started"})
}

// StopInstance godoc
// @Summary     Stop a stack instance
// @Description Trigger Helm uninstall for a stack instance
// @Tags        stack-instances
// @Produce     json
// @Param       id path string true "Instance ID"
// @Success     202 {object} map[string]string "Stop initiated"
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string "Not running"
// @Router      /api/v1/stack-instances/{id}/stop [post]
func (h *InstanceHandler) StopInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance ID is required"})
		return
	}

	if h.deployManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Deployment service not configured"})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Only allow stop from running or deploying.
	switch inst.Status {
	case models.StackStatusRunning, models.StackStatusDeploying:
		// OK
	default:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Cannot stop: instance is currently %s", inst.Status)})
		return
	}

	// Fetch chart configs so StopWithCharts can run helm uninstall per chart.
	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		status, message := mapError(err, "Chart configs")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if len(charts) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No charts configured for this stack definition"})
		return
	}

	var chartInfos []deployer.ChartDeployInfo
	for _, ch := range charts {
		chartInfos = append(chartInfos, deployer.ChartDeployInfo{
			ChartConfig: ch,
		})
	}

	logID, err := h.deployManager.StopWithCharts(c.Request.Context(), inst, chartInfos)
	if err != nil {
		slog.Error("Failed to start stop operation",
			"instance_id", id,
			"error", err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"log_id": logID, "message": "Stop initiated"})
}

// CleanInstance godoc
// @Summary     Clean a stack instance namespace
// @Description Uninstall all Helm releases and delete the K8s namespace, returning the instance to draft status
// @Tags        stack-instances
// @Produce     json
// @Param       id path string true "Instance ID"
// @Success     202 {object} map[string]string "Namespace cleanup initiated"
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string "Invalid status for clean"
// @Failure     503 {object} map[string]string "Deployment service not configured"
// @Router      /api/v1/stack-instances/{id}/clean [post]
func (h *InstanceHandler) CleanInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance ID is required"})
		return
	}

	if h.deployManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Deployment service not configured"})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Note: status check is not atomic with the update in Manager.Clean().
	// Concurrent API calls could race. The frontend mitigates this by
	// disabling buttons optimistically. A per-instance mutex would fix this
	// but is deferred as a known limitation shared with Deploy/Stop.
	switch inst.Status {
	case models.StackStatusRunning, models.StackStatusStopped, models.StackStatusError:
		// OK
	default:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Cannot clean: instance is currently %s", inst.Status)})
		return
	}

	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, "Stack definition")
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		status, message := mapError(err, "Chart configs")
		c.JSON(status, gin.H{"error": message})
		return
	}

	logID, err := h.deployManager.Clean(c.Request.Context(), inst, charts)
	if err != nil {
		slog.Error("Failed to start clean operation",
			"instance_id", id,
			"error", err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"log_id": logID, "message": "Namespace cleanup initiated"})
}

// GetDeployLog godoc
// @Summary     Get deployment logs
// @Description Get deployment log history for a stack instance
// @Tags        stack-instances
// @Produce     json
// @Param       id path string true "Instance ID"
// @Success     200 {array} models.DeploymentLog
// @Failure     404 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/deploy-log [get]
func (h *InstanceHandler) GetDeployLog(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance ID is required"})
		return
	}

	if h.deployLogRepo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Deployment log service not configured"})
		return
	}

	// Verify instance exists.
	if _, err := h.instanceRepo.FindByID(id); err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	logs, err := h.deployLogRepo.ListByInstance(c.Request.Context(), id)
	if err != nil {
		status, message := mapError(err, "Deployment log")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, logs)
}

// GetInstanceStatus godoc
// @Summary     Get instance K8s status
// @Description Get detailed Kubernetes resource status for a stack instance
// @Tags        stack-instances
// @Produce     json
// @Param       id path string true "Instance ID"
// @Success     200 {object} k8s.NamespaceStatus
// @Failure     404 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/status [get]
func (h *InstanceHandler) GetInstanceStatus(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance ID is required"})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "Stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Try cached status from watcher first.
	if h.k8sWatcher != nil {
		if nsStatus, ok := h.k8sWatcher.GetStatus(id); ok {
			c.JSON(http.StatusOK, nsStatus)
			return
		}
	}

	// Fall back to direct query if we have a cluster registry.
	if h.registry != nil {
		client, clientErr := h.registry.GetK8sClient(inst.ClusterID)
		if clientErr != nil {
			slog.Warn("Failed to get k8s client for instance status",
				"instance_id", id,
				"cluster_id", inst.ClusterID,
				"error", clientErr,
			)
			// Distinguish unknown cluster from connectivity/internal errors.
			var dbErr *dberrors.DatabaseError
			if errors.As(clientErr, &dbErr) && errors.Is(dbErr.Unwrap(), dberrors.ErrNotFound) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown cluster_id"})
			} else {
				c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to connect to cluster"})
			}
			return
		}
		nsStatus, err := client.GetNamespaceStatus(c.Request.Context(), inst.Namespace)
		if err != nil {
			slog.Error("Failed to get namespace status",
				"instance_id", id,
				"namespace", inst.Namespace,
				"error", err,
			)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
			return
		}
		c.JSON(http.StatusOK, nsStatus)
		return
	}

	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "K8s monitoring not configured"})
}

// checkNamespaceUniqueness checks whether the given namespace is already in use.
// If it is, it returns true and writes a 409 response with suggestions.
// The caller should return immediately when this returns true.
func (h *InstanceHandler) checkNamespaceUniqueness(c *gin.Context, namespace, instanceName string) bool {
	existing, err := h.instanceRepo.FindByNamespace(namespace)
	if err != nil {
		// Not found is the happy path — namespace is available.
		if errors.Is(err, dberrors.ErrNotFound) {
			return false
		}
		// Unexpected error — log and respond with 500.
		slog.Error("Failed to check namespace uniqueness",
			"namespace", namespace,
			"error", err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return true
	}

	// Namespace is taken — log details server-side but don't leak the other user's instance name.
	slog.Info("Namespace conflict detected",
		"namespace", namespace,
		"existing_instance_id", existing.ID,
		"existing_instance_name", existing.Name,
	)
	suggestions := generateNameSuggestions(instanceName)
	c.JSON(http.StatusConflict, NamespaceConflictResponse{
		Error:       "namespace already exists",
		Message:     fmt.Sprintf("Namespace %q is already in use", namespace),
		Suggestions: suggestions,
	})
	return true
}

// generateNameSuggestions returns up to 3 alternative instance name suggestions by
// appending -2, -3, -4 to the base instance name. The frontend uses these as
// instance names (not namespaces), so they are returned without the stack- prefix.
// Suggestions are trimmed to respect the 50-character instance name limit.
func generateNameSuggestions(instanceName string) []string {
	suggestions := make([]string, 0, 3)
	for _, suffix := range []string{"-2", "-3", "-4"} {
		base := instanceName
		maxBaseLen := models.MaxInstanceNameLength - len(suffix)
		if maxBaseLen <= 0 {
			continue
		}
		baseRunes := []rune(base)
		if len(baseRunes) > maxBaseLen {
			base = string(baseRunes[:maxBaseLen])
		}
		suggestions = append(suggestions, base+suffix)
	}
	return suggestions
}

func resolveOwnerName(userRepo models.UserRepository, ownerID string) string {
	if userRepo == nil {
		return ownerID
	}
	user, err := userRepo.FindByID(ownerID)
	if err != nil {
		return ownerID
	}
	return user.Username
}
