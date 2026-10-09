package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/cluster"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/helm"
	"backend/internal/hooks"
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

// ChartDeployPreview holds a per-chart comparison between previously deployed
// values and the values that would be deployed now.
type ChartDeployPreview struct {
	ChartName      string `json:"chart_name"`
	PreviousValues string `json:"previous_values"`
	PendingValues  string `json:"pending_values"`
	HasChanges     bool   `json:"has_changes"`
}

// DeployPreviewResponse is the response for the deploy-preview endpoint.
type DeployPreviewResponse struct {
	InstanceID   string               `json:"instance_id"`
	InstanceName string               `json:"instance_name"`
	Charts       []ChartDeployPreview `json:"charts"`
	// ValuesDrift is true when the running values come from a rollback (the
	// last operation was a successful rollback) and the stored overrides
	// produce different values: the next deploy undoes the rollback.
	ValuesDrift bool `json:"values_drift"`
	// Warning explains ValuesDrift when it is true.
	Warning string `json:"warning,omitempty"`
}

// MaxTTLMinutes is the maximum allowed TTL value (30 days).
const MaxTTLMinutes = 43200

// Stack instance handler message constants.
const (
	msgInstanceIDRequired    = "Instance ID is required"
	msgDeployerNotConfigured = "Deployment service not configured"
	msgTTLExceedsMax         = "TTL must not exceed %d minutes (30 days)"
)

// Slog structured logging key constants.
const logKeyInstanceID = "instance_id"

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
	versionRepo        models.TemplateVersionRepository
	valuesGen          *helm.ValuesGenerator
	userRepo           models.UserRepository
	deployManager      *deployer.Manager
	k8sWatcher         *k8s.Watcher
	registry           *cluster.Registry
	deployLogRepo      models.DeploymentLogRepository
	clusterRepo        models.ClusterRepository
	defaultTTLMinutes  int
	txRunner           database.TxRunner
	hooks              *hooks.Dispatcher
	actions            *hooks.ActionRegistry
	notifier           deployer.LifecycleNotifier
	sharedValuesRepo   models.SharedValuesRepository
	clusterQuotaRepo   models.ResourceQuotaRepository
}

// WithClusterQuotas attaches the cluster quota repository. Clone then copies
// the quota override for a caller without the admin or devops role only when
// the override stays within the cluster quota. Returns h for chaining.
// Without it the clone copies the override without this check.
func (h *InstanceHandler) WithClusterQuotas(repo models.ResourceQuotaRepository) *InstanceHandler {
	h.clusterQuotaRepo = repo
	return h
}

// WithSharedValues attaches the cluster shared values repository. Every values
// generation path (deploy, bulk deploy, deploy preview, export, compare)
// merges the shared values of the instance's cluster as the lowest layer.
// Returns h for chaining. Without it no shared values are applied.
func (h *InstanceHandler) WithSharedValues(repo models.SharedValuesRepository) *InstanceHandler {
	h.sharedValuesRepo = repo
	return h
}

// WithTemplateVersions attaches the template version repository. Template
// locked values then come from the latest published snapshot of the source
// template, not from its working copy. Returns h for chaining. Without it the
// working copy is used (legacy behaviour).
func (h *InstanceHandler) WithTemplateVersions(repo models.TemplateVersionRepository) *InstanceHandler {
	h.versionRepo = repo
	return h
}

// WithHooks attaches a webhook dispatcher for instance lifecycle events
// (pre/post instance-create, pre/post instance-delete). Returns h for chaining.
// Pass nil (or skip the call entirely) to disable hook dispatch.
func (h *InstanceHandler) WithHooks(d *hooks.Dispatcher) *InstanceHandler {
	h.hooks = d
	return h
}

// WithActions attaches an action registry for the generic
// POST /api/v1/stack-instances/:id/actions/:name route. Pass nil to disable.
func (h *InstanceHandler) WithActions(r *hooks.ActionRegistry) *InstanceHandler {
	h.actions = r
	return h
}

// WithNotifier attaches a lifecycle notifier for in-app notification creation
// on instance create/delete events. Pass nil to disable.
func (h *InstanceHandler) WithNotifier(n deployer.LifecycleNotifier) *InstanceHandler {
	h.notifier = n
	return h
}

// instanceRefFor builds a hooks.InstanceRef snapshot from a model.
// Returns nil when instance is nil so callers can pass through.
func instanceRefFor(instance *models.StackInstance) *hooks.InstanceRef {
	if instance == nil {
		return nil
	}
	return &hooks.InstanceRef{
		ID:                instance.ID,
		Name:              instance.Name,
		Namespace:         instance.Namespace,
		OwnerID:           instance.OwnerID,
		StackDefinitionID: instance.StackDefinitionID,
		Branch:            instance.Branch,
		ClusterID:         instance.ClusterID,
		Status:            instance.Status,
	}
}

// fireInstanceHook dispatches event with an envelope built from instance.
// No-ops when no dispatcher is attached or instance is nil.
func (h *InstanceHandler) fireInstanceHook(ctx context.Context, event string, instance *models.StackInstance) error {
	if h.hooks == nil || instance == nil {
		return nil
	}
	return h.hooks.Fire(ctx, event, hooks.EventEnvelope{InstanceRef: instanceRefFor(instance)})
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
	defaultTTLMinutes int,
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
		defaultTTLMinutes:  defaultTTLMinutes,
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
	clusterRepo models.ClusterRepository,
	defaultTTLMinutes int,
	txRunner database.TxRunner,
) (*InstanceHandler, error) {
	if txRunner == nil {
		return nil, fmt.Errorf("txRunner must not be nil")
	}
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
		clusterRepo:        clusterRepo,
		defaultTTLMinutes:  defaultTTLMinutes,
		txRunner:           txRunner,
	}, nil
}

// listPageSizeDefault is the default page size for paginated list queries.
const listPageSizeDefault = 25

// listPageSizeMax caps the maximum page size a client can request.
const listPageSizeMax = 100

// ListInstances godoc
// @Summary     List stack instances
// @Description List stack instances with server-side pagination, newest first. Supports page/pageSize or legacy limit/offset params.
// @Description The filters name, status, cluster_id, definition_id and owner combine (AND). total is the number of instances that match the filters.
// @Description owner is "me" (the authenticated user), a username, or a user ID. A value in UUID form is matched as a user ID first, then as a username. An unknown username or ID gives an empty list.
// @Description The list is paged (default pageSize 25); with owner=me or name the total is the real number of matches, not the page size.
// @Description Each instance includes owner_username, definition_name and cluster_name. A field is omitted when the owner, definition or cluster no longer exists; cluster_name is also omitted when cluster_id is empty (older instances).
// @Tags        stack-instances
// @Produce     json
// @Param       owner         query    string false "Filter by owner: 'me', a username, or a user ID"
// @Param       name          query    string false "Filter by exact instance name"
// @Param       status        query    string false "Filter by status" Enums(draft, queued, deploying, stabilizing, running, stopping, stopped, cleaning, partial, error)
// @Param       cluster_id    query    string false "Filter by cluster ID"
// @Param       definition_id query    string false "Filter by stack definition ID"
// @Param       page          query    int    false "Page number (1-based, default: 1)"
// @Param       pageSize      query    int    false "Results per page (default: 25, max: 100)"
// @Param       limit         query    int    false "Legacy: maximum number of results"
// @Param       offset        query    int    false "Legacy: number of results to skip"
// @Success     200   {object} map[string]interface{} "data: []StackInstance, total: int, page: int, pageSize: int"
// @Failure     400   {object} map[string]string "Unknown status, or a filter value that is too long"
// @Failure     401   {object} map[string]string "owner=me without an authenticated user"
// @Failure     500   {object} map[string]string
// @Router      /api/v1/stack-instances [get]
func (h *InstanceHandler) ListInstances(c *gin.Context) {
	filter := models.StackInstanceFilter{
		Name:         c.Query("name"),
		Status:       c.Query("status"),
		ClusterID:    c.Query("cluster_id"),
		DefinitionID: c.Query("definition_id"),
	}
	if filter.Status != "" && !models.IsValidStackStatus(filter.Status) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid status filter"})
		return
	}
	if !validIDFilter(c, "cluster_id", filter.ClusterID) || !validIDFilter(c, "definition_id", filter.DefinitionID) {
		return
	}
	ownerID, ok := resolveOwnerFilter(c, h.userRepo, c.Query("owner"))
	if !ok {
		return
	}
	filter.OwnerID = ownerID

	page, pageSize, offset := listPagination(c)

	instances, total, err := h.instanceRepo.ListPaged(filter, pageSize, offset)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}
	if instances == nil {
		instances = []models.StackInstance{}
	}
	h.setInstanceListNames(instances)

	c.JSON(http.StatusOK, gin.H{
		"data":     instances,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
}

// GetRecentInstances godoc
// @Summary     Get recent stack instances for the authenticated user
// @Description Returns the 5 most recently updated stack instances owned by the current user
// @Tags        stack-instances
// @Produce     json
// @Security    BearerAuth
// @Success     200  {array}  models.StackInstance
// @Failure     500  {object} map[string]string
// @Router      /api/v1/stack-instances/recent [get]
func (h *InstanceHandler) GetRecentInstances(c *gin.Context) {
	userID := middleware.GetUserIDFromContext(c)

	instances, err := h.instanceRepo.ListByOwner(userID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Sort by UpdatedAt descending.
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].UpdatedAt.After(instances[j].UpdatedAt)
	})

	// Take at most 5.
	if len(instances) > 5 {
		instances = instances[:5]
	}
	h.setInstanceListNames(instances)

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
// @Failure     400      {object} map[string]string "Invalid body, or name is not a DNS label (lowercase a-z, 0-9, '-', start and end alphanumeric, at most 50 characters)"
// @Failure     401      {object} map[string]string
// @Failure     403      {object} map[string]string "A pre-instance-create hook rejected the request"
// @Failure     404      {object} map[string]string "Stack definition not found"
// @Failure     409      {object} NamespaceConflictResponse "Namespace already exists"
// @Failure     500      {object} map[string]string
// @Router      /api/v1/stack-instances [post]
// createInstanceRequest is the JSON-binding DTO for CreateInstance.
// TTLMinutes is *int so we can distinguish "omitted" (nil → use default)
// from an explicit 0 (meaning "no expiry").
type createInstanceRequest struct {
	StackDefinitionID string `json:"stack_definition_id"`
	Name              string `json:"name"`
	Branch            string `json:"branch"`
	ClusterID         string `json:"cluster_id"`
	TTLMinutes        *int   `json:"ttl_minutes"`
}

func (h *InstanceHandler) CreateInstance(c *gin.Context) {
	var req createInstanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	var inst models.StackInstance
	inst.StackDefinitionID = req.StackDefinitionID
	inst.Name = req.Name
	inst.Branch = req.Branch
	inst.ClusterID = req.ClusterID

	inst.ID = uuid.New().String()
	inst.OwnerID = middleware.GetUserIDFromContext(c)
	inst.Status = models.StackStatusDraft
	now := time.Now().UTC()
	inst.CreatedAt = now
	inst.UpdatedAt = now

	if inst.Branch == "" {
		inst.Branch = "master"
	}

	// Apply default TTL only when the field was omitted (nil).
	// An explicit 0 means "no expiry".
	if req.TTLMinutes == nil && h.defaultTTLMinutes > 0 {
		inst.TTLMinutes = h.defaultTTLMinutes
	} else if req.TTLMinutes != nil {
		inst.TTLMinutes = *req.TTLMinutes
	}
	if inst.TTLMinutes > MaxTTLMinutes {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf(msgTTLExceedsMax, MaxTTLMinutes)})
		return
	}
	// Compute expiry timestamp from TTL.
	if inst.TTLMinutes > 0 {
		exp := now.Add(time.Duration(inst.TTLMinutes) * time.Minute)
		inst.ExpiresAt = &exp
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

	// Look up per-user instance limit from the cluster config (if any).
	var maxInstancesPerUser int
	if h.clusterRepo != nil && inst.ClusterID != "" {
		cl, clErr := h.clusterRepo.FindByID(inst.ClusterID)
		if clErr == nil && cl.MaxInstancesPerUser > 0 {
			maxInstancesPerUser = cl.MaxInstancesPerUser
		}
	}

	if err := models.ValidateInstanceName(inst.Name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
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
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Pre-instance-create hook: a subscriber with failure_policy=fail can abort
	// before any DB write. Fired after field validation, namespace uniqueness,
	// and definition existence checks so subscribers only see creates that are
	// otherwise eligible to succeed.
	if err := h.fireInstanceHook(c.Request.Context(), hooks.EventPreInstanceCreate, &inst); err != nil {
		slog.Error("pre-instance-create hook failed", "instance_name", inst.Name, "error", err)
		c.JSON(http.StatusForbidden, gin.H{"error": "pre-instance-create hook rejected the request"})
		return
	}

	if h.txRunner != nil && maxInstancesPerUser > 0 {
		// Transactional path — count check + create are serialized within
		// a transaction, closing the TOCTOU window for concurrent creates.
		limitMsg := fmt.Sprintf("Maximum instances per user reached for this cluster (limit: %d)", maxInstancesPerUser)
		txErr := h.txRunner.RunInTx(func(repos database.TxRepos) error {
			count, countErr := repos.StackInstance.CountByClusterAndOwner(inst.ClusterID, inst.OwnerID)
			if countErr != nil {
				return countErr
			}
			if count >= maxInstancesPerUser {
				return fmt.Errorf("%w: %s", ErrInstanceLimitExceeded, limitMsg)
			}
			return repos.StackInstance.Create(&inst)
		})
		if txErr != nil {
			if errors.Is(txErr, ErrInstanceLimitExceeded) {
				c.JSON(http.StatusConflict, gin.H{"error": limitMsg})
				return
			}
			status, message := mapError(txErr, entityStackInstance)
			c.JSON(status, gin.H{"error": message})
			return
		}
	} else if maxInstancesPerUser > 0 {
		// Non-transactional path — still enforce the limit (TOCTOU possible
		// but acceptable when txRunner is not configured).
		limitMsg := fmt.Sprintf("Maximum instances per user reached for this cluster (limit: %d)", maxInstancesPerUser)
		count, countErr := h.instanceRepo.CountByClusterAndOwner(inst.ClusterID, inst.OwnerID)
		if countErr != nil {
			status, message := mapError(countErr, entityStackInstance)
			c.JSON(status, gin.H{"error": message})
			return
		}
		if count >= maxInstancesPerUser {
			c.JSON(http.StatusConflict, gin.H{"error": limitMsg})
			return
		}
		if err := h.instanceRepo.Create(&inst); err != nil {
			status, message := mapError(err, entityStackInstance)
			c.JSON(status, gin.H{"error": message})
			return
		}
	} else {
		if err := h.instanceRepo.Create(&inst); err != nil {
			status, message := mapError(err, entityStackInstance)
			c.JSON(status, gin.H{"error": message})
			return
		}
	}

	// Post-instance-create hook: ignore-by-default. Cannot undo the create at
	// this point; subscribers exist for downstream notification (CMDB sync,
	// audit logs, etc).
	_ = h.fireInstanceHook(c.Request.Context(), hooks.EventPostInstanceCreate, &inst)

	if h.notifier != nil {
		_ = h.notifier.Notify(c.Request.Context(), inst.OwnerID, "instance.created", "Stack created",
			fmt.Sprintf("Stack %s has been created", inst.Name), "stack_instance", inst.ID)
	}

	h.setInstanceNames(&inst)
	c.JSON(http.StatusCreated, inst)
}

// GetInstance godoc
// @Summary     Get a stack instance
// @Description Get a stack instance by ID. values_drift is true when the running values come from a successful rollback and the stored overrides produce different values: the next deploy undoes the rollback. Only this endpoint computes values_drift; list responses omit it. owner_username, definition_name and cluster_name are omitted when the owner, definition or cluster no longer exists; cluster_name is also omitted when cluster_id is empty (older instances).
// @Tags        stack-instances
// @Produce     json
// @Param       id  path     string true "Instance ID"
// @Success     200 {object} models.StackInstance
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/stack-instances/{id} [get]
func (h *InstanceHandler) GetInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	inst.ValuesDrift = h.instanceValuesDrift(c.Request.Context(), inst)
	h.setInstanceNames(inst)

	c.JSON(http.StatusOK, inst)
}

// UpdateInstance godoc
// @Summary     Update a stack instance
// @Description Update a stack instance (branch, name, etc.). A changed name must be a DNS label (lowercase a-z, 0-9, '-', start and end alphanumeric, at most 50 characters); an unchanged name is not checked, so older instances with other names stay editable. A rename does not change the namespace.
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id       path     string               true "Instance ID"
// @Param       instance body     models.StackInstance   true "Instance object"
// @Success     200      {object} models.StackInstance
// @Failure     400      {object} map[string]string
// @Failure     403      {object} map[string]string "Caller is not the owner, an admin or a devops user"
// @Failure     404      {object} map[string]string
// @Router      /api/v1/stack-instances/{id} [put]
func (h *InstanceHandler) UpdateInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	existing, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: only the owner, an admin or a devops user may update the instance.
	if !requireInstanceModify(c, existing) {
		return
	}

	var update struct {
		Name       *string `json:"name"`
		Branch     *string `json:"branch"`
		Namespace  *string `json:"namespace"`
		TTLMinutes *int    `json:"ttl_minutes"`
	}
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	if update.Name != nil && *update.Name != existing.Name {
		// Only a changed name must follow the name rule, so instances created
		// before the rule existed can still be updated.
		if err := models.ValidateInstanceName(*update.Name); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		existing.Name = *update.Name
	}
	if update.Branch != nil {
		existing.Branch = *update.Branch
	}
	if update.Namespace != nil {
		existing.Namespace = *update.Namespace
	}
	existing.UpdatedAt = time.Now().UTC()

	// Update TTL only if the field was explicitly sent.
	if update.TTLMinutes != nil {
		ttl := *update.TTLMinutes
		if ttl > MaxTTLMinutes {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf(msgTTLExceedsMax, MaxTTLMinutes)})
			return
		}
		existing.TTLMinutes = ttl
		if ttl > 0 {
			exp := time.Now().UTC().Add(time.Duration(ttl) * time.Minute)
			existing.ExpiresAt = &exp
		} else {
			existing.ExpiresAt = nil
		}
	}

	if err := existing.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.instanceRepo.Update(existing); err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	h.setInstanceNames(existing)
	c.JSON(http.StatusOK, existing)
}

// DeleteInstance godoc
// @Summary     Delete a stack instance
// @Description Deletes a stack instance. If the instance has running resources (status running/stopped/error), a cleanup is initiated first — helm releases are uninstalled and the namespace is deleted before the database record is removed. Returns 204 for immediate deletion (draft instances) or 202 when async cleanup is required. When quick deploy created the stack definition of the instance (owner_instance_id) and no other instance uses it, the definition and its charts are deleted with the instance.
// @Tags        stack-instances
// @Produce     json
// @Param       id  path     string true "Instance ID"
// @Success     202 {object} map[string]string "Cleanup initiated, instance will be deleted after resources are removed"
// @Success     204 "No Content — instance deleted immediately (no resources to clean)"
// @Failure     403 {object} map[string]string "Caller is not the owner, an admin or a devops user, or a pre-instance-delete hook rejected the request"
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string "Instance is in a transient state (deploying/stopping/cleaning)"
// @Failure     503 {object} map[string]string "Deploy manager not configured"
// @Router      /api/v1/stack-instances/{id} [delete]
func (h *InstanceHandler) DeleteInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: only the owner, an admin or a devops user may delete the instance.
	// Checked before any side effect (hooks, status change, deploy log, Helm).
	if !requireInstanceModify(c, inst) {
		return
	}

	// Pre-instance-delete hook: a subscriber with failure_policy=fail can block
	// the delete (e.g. enforce dependency checks).
	if err := h.fireInstanceHook(c.Request.Context(), hooks.EventPreInstanceDelete, inst); err != nil {
		slog.Error("pre-instance-delete hook failed", logKeyInstanceID, id, "error", err)
		c.JSON(http.StatusForbidden, gin.H{"error": "pre-instance-delete hook rejected the request"})
		return
	}

	switch inst.Status {
	case models.StackStatusDeploying, models.StackStatusStopping, models.StackStatusCleaning, models.StackStatusQueued, models.StackStatusStabilizing:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Cannot delete: instance is currently %s", inst.Status)})
		return

	case models.StackStatusRunning, models.StackStatusPartial, models.StackStatusStopped, models.StackStatusError:
		if h.deployManager == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": msgDeployerNotConfigured})
			return
		}

		def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
		if err != nil {
			status, message := mapError(err, entityStackDefinition)
			c.JSON(status, gin.H{"error": message})
			return
		}
		charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
		if err != nil {
			status, message := mapError(err, entityChartConfigs)
			c.JSON(status, gin.H{"error": message})
			return
		}

		h.deployManager.ScheduleDeleteAfterClean(id)

		logID, err := h.deployManager.Clean(c.Request.Context(), inst, charts)
		if err != nil {
			slog.Error("Failed to start clean for delete",
				logKeyInstanceID, id, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}

		c.JSON(http.StatusAccepted, gin.H{
			"log_id":  logID,
			"message": "Cleanup initiated; instance will be deleted after resources are removed",
		})
		return

	default:
		// draft or unknown — no resources to clean, delete immediately
	}

	if h.txRunner != nil {
		txErr := database.DeleteInstanceWithOwnedDefinition(h.txRunner, inst)
		if txErr != nil {
			status, message := mapError(txErr, entityStackInstance)
			c.JSON(status, gin.H{"error": message})
			return
		}
	} else {
		slog.Error("txRunner not configured for DeleteInstance", "instance_id", id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	_ = h.fireInstanceHook(c.Request.Context(), hooks.EventPostInstanceDelete, inst)

	if h.notifier != nil {
		_ = h.notifier.Notify(c.Request.Context(), inst.OwnerID, "instance.deleted", "Stack deleted",
			fmt.Sprintf("Stack %s has been deleted", inst.Name), "stack_instance", inst.ID)
	}

	c.Status(http.StatusNoContent)
}

type invokeActionRequest struct {
	Parameters map[string]any `json:"parameters,omitempty"`
}

// InvokeAction godoc
// @Summary     Invoke a registered action against a stack instance
// @Description Dispatches to the action subscriber webhook and wraps its response in an envelope containing action, instance_id, status_code, and result fields. The subscriber's JSON body is nested under the result key. Returns 200 even for non-2xx subscriber responses — check status_code to distinguish.
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id      path     string                true "Instance ID"
// @Param       name    path     string                true "Action name"
// @Param       request body     invokeActionRequest   false "Optional parameters passed through to the subscriber"
// @Success     200 {object} map[string]any
// @Failure     400 {object} map[string]string
// @Failure     403 {object} map[string]string "Caller is not the owner, an admin or a devops user"
// @Failure     404 {object} map[string]string "Instance not found or action not registered"
// @Failure     502 {object} map[string]string "Subscriber unreachable"
// @Failure     503 {object} map[string]string "Action registry not configured"
// @Router      /api/v1/stack-instances/{id}/actions/{name} [post]
func (h *InstanceHandler) InvokeAction(c *gin.Context) {
	id := c.Param("id")
	name := c.Param("name")
	if id == "" || name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "instance id and action name are required"})
		return
	}
	if h.actions == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "action registry not configured"})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: only the owner, an admin or a devops user may run actions on the instance.
	// Checked before any side effect (hooks, status change, deploy log, Helm).
	if !requireInstanceModify(c, inst) {
		return
	}

	// Attempt to bind body when one is present — ContentLength == -1 for
	// chunked transfer-encoding, so we can't gate on that. We only skip
	// binding when the request genuinely has no body (nil or http.NoBody)
	// so actions with no parameters work with a request like POST /.../
	// without a Content-Length header. EOF on empty body is tolerated.
	var req invokeActionRequest
	if c.Request.Body != nil && c.Request.Body != http.NoBody {
		if bindErr := c.ShouldBindJSON(&req); bindErr != nil && !errors.Is(bindErr, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
			return
		}
	}

	res, invokeErr := h.actions.Invoke(c.Request.Context(), name, instanceRefFor(inst), req.Parameters)
	if invokeErr != nil {
		var unk hooks.ErrUnknownAction
		if errors.As(invokeErr, &unk) {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("unknown action %q", unk.Name)})
			return
		}
		// Log the detailed error server-side (includes internal URLs, DNS,
		// transport specifics) but return a generic message to the client
		// with the request_id as a correlation key.
		slog.Error("action invocation failed",
			logKeyInstanceID, id,
			"action", name,
			"hook_request_id", res.RequestID,
			"error", invokeErr,
		)
		c.JSON(http.StatusBadGateway, gin.H{
			"error":      "action subscriber unreachable or returned transport error",
			"action":     name,
			"request_id": res.RequestID,
		})
		return
	}

	// Validate the subscriber's body is JSON before forwarding: embedding
	// arbitrary bytes as json.RawMessage would produce invalid JSON (and a
	// 500 from gin's marshal) if the subscriber responded with plain text
	// or a truncated body.
	result := res.Body
	if !json.Valid(result) {
		slog.Error("action subscriber returned non-JSON body",
			logKeyInstanceID, id,
			"action", name,
			"status_code", res.StatusCode,
		)
		c.JSON(http.StatusBadGateway, gin.H{
			"error":       "action subscriber returned a non-JSON body",
			"action":      name,
			"status_code": res.StatusCode,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"action":      name,
		"instance_id": id,
		"status_code": res.StatusCode,
		"result":      json.RawMessage(result),
	})
}

// cloneInstanceRequest is the optional request body for CloneInstance.
type cloneInstanceRequest struct {
	// Name of the clone. Must be a DNS label (lowercase a-z, 0-9, '-', start
	// and end alphanumeric, at most 50 characters). When empty, the server
	// picks the first free name of <source>-copy, <source>-copy-2, ...
	Name string `json:"name"`
	// Branch of the clone. Default: the branch of the source.
	Branch string `json:"branch"`
	// TTLMinutes of the clone (0 = no expiry). Default: the TTL of the source.
	TTLMinutes *int `json:"ttl_minutes"`
}

// maxCloneNameAttempts limits the generated clone names that are tried.
const maxCloneNameAttempts = 50

// cloneNameCandidate returns the n-th generated clone name for base:
// <base>-copy for n = 1 and <base>-copy-<n> after that. The base is cut so the
// name fits MaxInstanceNameLength, and it stays a valid DNS label.
func cloneNameCandidate(base string, n int) string {
	suffix := "-copy"
	if n > 1 {
		suffix = fmt.Sprintf("-copy-%d", n)
	}
	maxBase := models.MaxInstanceNameLength - len(suffix)
	if len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}
	if base == "" {
		return strings.TrimPrefix(suffix, "-")
	}
	return base + suffix
}

// CloneInstance godoc
// @Summary     Clone a stack instance
// @Description Create a new draft stack instance as a copy of an existing one. The clone belongs to the caller and uses the cluster and definition of the source.
// @Description It copies the TTL (unless ttl_minutes is given), the value overrides and the branch overrides. It copies the instance quota override only when the caller owns the source or is admin or devops; for an owner without the admin or devops role only when the override stays within the cluster quota. When it is not copied for that reason (or the cluster quota lookup fails), the response has a warning and the clone uses the cluster quota.
// @Description The body is optional. Without a name, the server picks the first free name of <source>-copy, <source>-copy-2, ... (the namespace stack-<name>-<owner> must be free). A given name must be a DNS label (lowercase a-z, 0-9, '-', start and end alphanumeric, at most 50 characters).
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id   path     string               true  "Source instance ID"
// @Param       body body     cloneInstanceRequest false "Optional clone name, branch and TTL"
// @Success     201  {object} CloneInstanceResponse
// @Failure     400  {object} map[string]string "Invalid body, name or TTL"
// @Failure     401  {object} map[string]string
// @Failure     403  {object} map[string]string
// @Failure     404  {object} map[string]string
// @Failure     409  {object} NamespaceConflictResponse "Namespace already exists (given name), or no free generated name"
// @Failure     500  {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/clone [post]
func (h *InstanceHandler) CloneInstance(c *gin.Context) {
	id := c.Param("id")
	source, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	var req cloneInstanceRequest
	if c.Request.Body != nil && c.Request.Body != http.NoBody {
		if bindErr := c.ShouldBindJSON(&req); bindErr != nil && !errors.Is(bindErr, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
			return
		}
	}

	now := time.Now().UTC()
	ownerID := middleware.GetUserIDFromContext(c)
	ownerName := middleware.GetUsernameFromContext(c)

	ttl := source.TTLMinutes
	if req.TTLMinutes != nil {
		ttl = *req.TTLMinutes
	}
	if ttl < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ttl_minutes must be non-negative"})
		return
	}
	if ttl > MaxTTLMinutes {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf(msgTTLExceedsMax, MaxTTLMinutes)})
		return
	}

	branch := source.Branch
	if req.Branch != "" {
		branch = req.Branch
	}

	cloneName := req.Name
	if cloneName != "" {
		if err := models.ValidateInstanceName(cloneName); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if h.checkNamespaceUniqueness(c, buildNamespace(cloneName, ownerName), cloneName) {
			return
		}
	} else {
		// The source name can be older than the name rule; sanitize it first.
		base := sanitizeRFC1123Label(source.Name)
		for n := 1; n <= maxCloneNameAttempts; n++ {
			candidate := cloneNameCandidate(base, n)
			_, findErr := h.instanceRepo.FindByNamespace(buildNamespace(candidate, ownerName))
			if errors.Is(findErr, dberrors.ErrNotFound) {
				cloneName = candidate
				break
			}
			if findErr != nil {
				slog.Error("Failed to check namespace uniqueness for clone",
					logKeyInstanceID, id, "error", findErr)
				c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
				return
			}
		}
		if cloneName == "" {
			c.JSON(http.StatusConflict, NamespaceConflictResponse{
				Error:       "namespace already exists",
				Message:     fmt.Sprintf("No free clone name found; send a name in the request body (tried %d names)", maxCloneNameAttempts),
				Suggestions: []string{},
			})
			return
		}
	}

	clone := &models.StackInstance{
		ID:                uuid.New().String(),
		StackDefinitionID: source.StackDefinitionID,
		Name:              cloneName,
		Namespace:         buildNamespace(cloneName, ownerName),
		OwnerID:           ownerID,
		Branch:            branch,
		ClusterID:         source.ClusterID,
		Status:            models.StackStatusDraft,
		TTLMinutes:        ttl,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if ttl > 0 {
		exp := now.Add(time.Duration(ttl) * time.Minute)
		clone.ExpiresAt = &exp
	}

	if err := clone.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if h.txRunner == nil {
		slog.Error("txRunner not configured for CloneInstance", logKeyInstanceID, id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	quotaPolicy := h.cloneQuotaPolicy(c, source)

	// Instance create + override copies are atomic.
	var warning string
	txErr := h.txRunner.RunInTx(func(repos database.TxRepos) error {
		var err error
		warning, err = cloneInstanceTx(c.Request.Context(), repos, source.ID, clone, now, quotaPolicy)
		return err
	})
	if txErr != nil {
		status, message := mapError(txErr, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	h.setInstanceNames(clone)
	c.JSON(http.StatusCreated, CloneInstanceResponse{StackInstance: *clone, Warning: warning})
}

// CloneInstanceResponse is the response of the clone endpoint: the new
// instance and an optional warning.
type CloneInstanceResponse struct {
	models.StackInstance
	// Warning is set when the clone did not get the quota override of the
	// source (above the cluster quota, or the cluster quota lookup failed).
	// The clone then uses the cluster quota.
	Warning string `json:"warning,omitempty"`
}

// Clone warnings for a quota override that is not copied.
const (
	msgCloneQuotaAboveCluster = "The quota override of the source was not copied: it exceeds the cluster quota (%s). The clone uses the cluster quota; only admin or devops can grant more."
	msgCloneQuotaLookupFailed = "The quota override of the source was not copied: the cluster quota could not be checked. The clone uses the cluster quota."
)

// cloneQuotaPolicy returns the rule that decides whether the clone gets the
// quota override of source. The quota override is a resource grant, not
// configuration:
//   - A caller who may not modify the source gets no copy (nil policy): the
//     clone uses the cluster quota.
//   - Admin and devops get the copy.
//   - The owner gets the copy only when it stays within the cluster quota
//     (models.CheckOverrideWithinClusterQuota), the same rule as
//     SetQuotaOverride. Otherwise the clone uses the cluster quota and the
//     clone still succeeds. The skip is logged.
//   - If the cluster quota lookup fails, the owner gets no copy (fail closed);
//     the clone still succeeds.
//
// The policy returns whether to copy and, when it skips a copy for the owner,
// a warning for the clone response.
func (h *InstanceHandler) cloneQuotaPolicy(c *gin.Context, source *models.StackInstance) func(*models.InstanceQuotaOverride) (bool, string) {
	if !canModifyInstance(c, source) {
		return nil
	}
	copyAll := func(*models.InstanceQuotaOverride) (bool, string) { return true, "" }
	if isPrivilegedRole(c) || h.clusterQuotaRepo == nil {
		return copyAll
	}
	var resolver clusterIDResolver
	if h.registry != nil {
		resolver = h.registry
	}
	userID := middleware.GetUserIDFromContext(c)
	clusterQuota, err := lookupClusterQuota(c.Request.Context(), h.clusterQuotaRepo, resolver, source.ClusterID)
	if err != nil {
		return func(*models.InstanceQuotaOverride) (bool, string) {
			slog.Error("Clone skips the quota override: cluster quota lookup failed",
				"source_instance_id", source.ID, "user_id", userID, "error", err)
			return false, msgCloneQuotaLookupFailed
		}
	}
	return func(q *models.InstanceQuotaOverride) (bool, string) {
		if checkErr := models.CheckOverrideWithinClusterQuota(clusterQuota, q, nil); checkErr != nil {
			slog.Warn("Clone skips the quota override: it exceeds the cluster quota",
				"source_instance_id", source.ID, "user_id", userID, "reason", checkErr.Error())
			return false, fmt.Sprintf(msgCloneQuotaAboveCluster, checkErr.Error())
		}
		return true, ""
	}
}

// cloneInstanceTx creates clone and copies the value overrides and the branch
// overrides of the source instance, and the quota override when quotaPolicy is
// set and allows it. Repositories that are not in repos are skipped. It
// returns the warning of quotaPolicy when the policy skips the quota override.
func cloneInstanceTx(ctx context.Context, repos database.TxRepos, sourceID string, clone *models.StackInstance, now time.Time, quotaPolicy func(*models.InstanceQuotaOverride) (bool, string)) (string, error) {
	if err := repos.StackInstance.Create(clone); err != nil {
		return "", err
	}
	if repos.ValueOverride != nil {
		overrides, err := repos.ValueOverride.ListByInstance(sourceID)
		if err != nil {
			return "", err
		}
		for _, ov := range overrides {
			if err := repos.ValueOverride.Create(&models.ValueOverride{
				ID:              uuid.New().String(),
				StackInstanceID: clone.ID,
				ChartConfigID:   ov.ChartConfigID,
				Values:          ov.Values,
				UpdatedAt:       now,
			}); err != nil {
				return "", err
			}
		}
	}
	if repos.BranchOverride != nil {
		branches, err := repos.BranchOverride.List(sourceID)
		if err != nil {
			return "", err
		}
		for _, bo := range branches {
			if err := repos.BranchOverride.Set(&models.ChartBranchOverride{
				ID:              uuid.New().String(),
				StackInstanceID: clone.ID,
				ChartConfigID:   bo.ChartConfigID,
				Branch:          bo.Branch,
				UpdatedAt:       now,
			}); err != nil {
				return "", err
			}
		}
	}
	if quotaPolicy != nil && repos.InstanceQuotaOverride != nil {
		quota, err := repos.InstanceQuotaOverride.GetByInstanceID(ctx, sourceID)
		if err != nil && !errors.Is(err, dberrors.ErrNotFound) {
			return "", err
		}
		if err == nil && quota != nil {
			copyQuota, warning := quotaPolicy(quota)
			if !copyQuota {
				return warning, nil
			}
			copied := *quota
			copied.ID = uuid.New().String()
			copied.StackInstanceID = clone.ID
			copied.CreatedAt = now
			copied.UpdatedAt = now
			if quota.PodLimit != nil {
				pods := *quota.PodLimit
				copied.PodLimit = &pods
			}
			if err := repos.InstanceQuotaOverride.Upsert(ctx, &copied); err != nil {
				return "", err
			}
		}
	}
	return "", nil
}

// ExportChartValues godoc
// @Summary     Export chart values
// @Description Generate and export the merged values.yaml for a specific chart: cluster shared values (by priority), chart defaults, instance overrides, locked template values and the chart branch override, as used by deploy.
// @Tags        stack-instances
// @Produce     application/x-yaml
// @Param       id      path     string true "Instance ID"
// @Param       chartId path     string true "Chart config ID"
// @Success     200     {string} string "YAML content"
// @Header      200     {string} Content-Disposition "attachment with a quoted filename <instance>-<chart>-values.yaml and an RFC 5987 filename* (UTF-8) form"
// @Failure     401     {object} map[string]string
// @Failure     404     {object} map[string]string "Instance not found, or chart not found in this stack definition"
// @Failure     500     {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/values/{chartId} [get]
func (h *InstanceHandler) ExportChartValues(c *gin.Context) {
	instanceID := c.Param("id")
	chartID := c.Param("chartId")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	chart, ok := requireInstanceChart(c, h.chartConfigRepo, inst, chartID)
	if !ok {
		return
	}

	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	valuesMap, _, err := h.buildChartValuesAndBranches(c.Request.Context(), inst, def, []models.ChartConfig{*chart})
	if err != nil {
		slog.Error("export chart values: failed to build values", logKeyInstanceID, instanceID, "chart_id", chartID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	c.Header("Content-Disposition", attachmentDisposition(inst.Name+"-"+chart.ChartName+"-values.yaml"))
	c.Data(http.StatusOK, "application/x-yaml", []byte(valuesMap[chart.ChartName]))
}

// ExportAllValues godoc
// @Summary     Export all chart values
// @Description Generate and export merged values for all charts as a zip archive (same layers as deploy, including cluster shared values and chart branch overrides)
// @Tags        stack-instances
// @Produce     application/zip
// @Param       id  path     string true "Instance ID"
// @Success     200 {file}   file   "ZIP archive"
// @Header      200 {string} Content-Disposition "attachment with a quoted filename <instance>-values.zip and an RFC 5987 filename* (UTF-8) form"
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/values [get]
func (h *InstanceHandler) ExportAllValues(c *gin.Context) {
	instanceID := c.Param("id")

	inst, err := h.instanceRepo.FindByID(instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		status, message := mapError(err, entityChartConfigs)
		c.JSON(status, gin.H{"error": message})
		return
	}

	layers, templateVars, _, err := h.chartValueLayers(c.Request.Context(), inst, def, charts)
	if err != nil {
		slog.Error("export values: failed to collect values layers", logKeyInstanceID, instanceID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	params := helm.GenerateAllParams{
		Charts:       layers,
		TemplateVars: templateVars,
	}

	allValues, err := h.valuesGen.ExportAsZip(c.Request.Context(), params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	c.Header("Content-Disposition", attachmentDisposition(inst.Name+"-values.zip"))
	c.Data(http.StatusOK, "application/zip", allValues)
}

// DeployInstance godoc
// @Summary     Deploy a stack instance
// @Description Trigger Helm deployment for a stack instance. Allowed for draft, stopped, error, partial and running instances (running = redeploy, a Helm upgrade with the current values). With a TTL, the expiry becomes now + ttl_minutes, unless the current expiry is later (a redeploy never makes the expiry earlier).
// @Tags        stack-instances
// @Produce     json
// @Param       id path string true "Instance ID"
// @Success     202 {object} map[string]string "Deployment started"
// @Failure     400 {object} map[string]string
// @Failure     403 {object} map[string]string "Caller is not the owner, an admin or a devops user"
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string "Already deploying"
// @Router      /api/v1/stack-instances/{id}/deploy [post]
func (h *InstanceHandler) DeployInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	if h.deployManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": msgDeployerNotConfigured})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: only the owner, an admin or a devops user may deploy the instance.
	// Checked before any side effect (hooks, status change, deploy log, Helm).
	if !requireInstanceModify(c, inst) {
		return
	}

	// Allow deploy from draft, stopped, error, or running (upgrade).
	switch inst.Status {
	case models.StackStatusDraft, models.StackStatusStopped, models.StackStatusError, models.StackStatusRunning, models.StackStatusPartial:
		// OK — running triggers a helm upgrade with the latest values.
	case models.StackStatusDeploying, models.StackStatusQueued, models.StackStatusStopping, models.StackStatusStabilizing:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Cannot deploy: instance is currently %s", inst.Status)})
		return
	default:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Cannot deploy: instance is in state %s", inst.Status)})
		return
	}

	// Restart the TTL clock on deploy, but never make the expiry earlier: a
	// redeploy of an instance that was extended keeps the later expiry.
	if inst.TTLMinutes > 0 {
		inst.ExpiresAt = deployExpiry(inst.ExpiresAt, inst.TTLMinutes, time.Now().UTC())
		_ = h.instanceRepo.Update(inst)
	}

	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		status, message := mapError(err, entityChartConfigs)
		c.JSON(status, gin.H{"error": message})
		return
	}

	if len(charts) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No charts configured for this stack definition"})
		return
	}

	// Validate required deployment inputs before building chart values.
	if inst.Namespace == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance namespace is empty"})
		return
	}

	valuesMap, branchMap, err := h.buildChartValuesAndBranches(c.Request.Context(), inst, def, charts)
	if err != nil {
		slog.Error("Failed to build chart values",
			logKeyInstanceID, id,
			"error", err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	var chartInfos []deployer.ChartDeployInfo
	for _, ch := range charts {
		chartInfos = append(chartInfos, deployer.ChartDeployInfo{
			ChartConfig: ch,
			ValuesYAML:  []byte(valuesMap[ch.ChartName]),
			Branch:      branchMap[ch.ID],
		})
	}

	var lastDeployedValuesJSON string
	if encoded, err := json.Marshal(valuesMap); err == nil {
		lastDeployedValuesJSON = string(encoded)
	}

	req := deployer.DeployRequest{
		Instance:           inst,
		Definition:         def,
		Charts:             chartInfos,
		LastDeployedValues: lastDeployedValuesJSON,
		UserID:             middleware.GetUserIDFromContext(c),
	}

	logID, err := h.deployManager.Deploy(c.Request.Context(), req)
	if err != nil {
		slog.Error("Failed to start deployment",
			logKeyInstanceID, id,
			"error", err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"log_id": logID, "message": "Deployment started"})
}

// DeployPreview godoc
// @Summary     Preview deployment changes
// @Description Compare pending merged values against the running values per chart. Pending values use the deploy pipeline: cluster shared values (by priority), chart defaults, instance overrides, locked template values. Previous values are the values of the last deploy, or of the last successful rollback. has_changes compares the YAML content (key order and formatting do not matter). values_drift is true when the last operation was a successful rollback and at least one chart has changes: the next deploy undoes the rollback. A shared values load error returns 500 (fail closed).
// @Tags        stack-instances
// @Produce     json
// @Param       id path string true "Instance ID"
// @Success     200 {object} DeployPreviewResponse
// @Failure     400 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Security    BearerAuth
// @Router      /api/v1/stack-instances/{id}/deploy-preview [get]
func (h *InstanceHandler) DeployPreview(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: only the owner, an admin or a devops user may preview the instance.
	if !requireInstanceModify(c, inst) {
		return
	}

	if inst.Namespace == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance namespace is empty"})
		return
	}

	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		status, message := mapError(err, entityChartConfigs)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Build merged values for each chart.
	valuesMap, err := h.buildChartValues(c.Request.Context(), inst, def, charts)
	if err != nil {
		slog.Error("deploy-preview: failed to build chart values", logKeyInstanceID, id, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Parse last deployed values.
	previousMap := make(map[string]string)
	if inst.LastDeployedValues != "" {
		if err := json.Unmarshal([]byte(inst.LastDeployedValues), &previousMap); err != nil {
			slog.Warn("Failed to parse last deployed values", logKeyInstanceID, id, "error", err)
		}
	}

	// Build per-chart comparison.
	chartPreviews := make([]ChartDeployPreview, 0, len(charts))
	for _, ch := range charts {
		pending := valuesMap[ch.ChartName]
		previous := previousMap[ch.ChartName]

		chartPreviews = append(chartPreviews, ChartDeployPreview{
			ChartName:      ch.ChartName,
			PreviousValues: previous,
			PendingValues:  pending,
			HasChanges:     !valuesEqual(pending, previous),
		})
	}

	resp := DeployPreviewResponse{
		InstanceID:   inst.ID,
		InstanceName: inst.Name,
		Charts:       chartPreviews,
	}
	anyChanges := false
	for _, cp := range chartPreviews {
		anyChanges = anyChanges || cp.HasChanges
	}
	if anyChanges && h.lastOperationWasRollback(c.Request.Context(), inst.ID) {
		resp.ValuesDrift = true
		resp.Warning = msgValuesDrift
	}

	c.JSON(http.StatusOK, resp)
}

// StopInstance godoc
// @Summary     Stop a stack instance
// @Description Trigger Helm uninstall for a stack instance
// @Tags        stack-instances
// @Produce     json
// @Param       id path string true "Instance ID"
// @Success     202 {object} map[string]string "Stop initiated"
// @Failure     400 {object} map[string]string
// @Failure     403 {object} map[string]string "Caller is not the owner, an admin or a devops user"
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string "Not running"
// @Router      /api/v1/stack-instances/{id}/stop [post]
func (h *InstanceHandler) StopInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	if h.deployManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": msgDeployerNotConfigured})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: only the owner, an admin or a devops user may stop the instance.
	// Checked before any side effect (hooks, status change, deploy log, Helm).
	if !requireInstanceModify(c, inst) {
		return
	}

	// Only allow stop from running or deploying.
	switch inst.Status {
	case models.StackStatusRunning, models.StackStatusPartial, models.StackStatusDeploying, models.StackStatusStabilizing:
		// OK — partial/stabilizing means some Helm charts succeeded, safe to stop
	default:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Cannot stop: instance is currently %s", inst.Status)})
		return
	}

	// Fetch chart configs so StopWithCharts can run helm uninstall per chart.
	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		status, message := mapError(err, entityChartConfigs)
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
			logKeyInstanceID, id,
			"error", err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
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
// @Failure     403 {object} map[string]string "Caller is not the owner, an admin or a devops user"
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string "Invalid status for clean"
// @Failure     503 {object} map[string]string "Deployment service not configured"
// @Router      /api/v1/stack-instances/{id}/clean [post]
func (h *InstanceHandler) CleanInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	if h.deployManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": msgDeployerNotConfigured})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: only the owner, an admin or a devops user may clean the instance.
	// Checked before any side effect (hooks, status change, deploy log, Helm).
	if !requireInstanceModify(c, inst) {
		return
	}

	// Note: status check is not atomic with the update in Manager.Clean().
	// Concurrent API calls could race. The frontend mitigates this by
	// disabling buttons optimistically. A per-instance mutex would fix this
	// but is deferred as a known limitation shared with Deploy/Stop.
	switch inst.Status {
	case models.StackStatusRunning, models.StackStatusPartial, models.StackStatusStopped, models.StackStatusError:
		// OK
	default:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Cannot clean: instance is currently %s", inst.Status)})
		return
	}

	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		status, message := mapError(err, entityChartConfigs)
		c.JSON(status, gin.H{"error": message})
		return
	}

	logID, err := h.deployManager.Clean(c.Request.Context(), inst, charts)
	if err != nil {
		slog.Error("Failed to start clean operation",
			logKeyInstanceID, id,
			"error", err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"log_id": logID, "message": "Namespace cleanup initiated"})
}

// GetDeployLog godoc
// @Summary     Get deployment logs
// @Description Get deployment log history for a stack instance. Supports cursor-based pagination for efficient large dataset traversal.
// @Tags        stack-instances
// @Produce     json
// @Param       id     path  string true  "Instance ID"
// @Param       limit  query int    false "Page size (default 50)"
// @Param       offset query int    false "Offset for traditional pagination (default 0)"
// @Param       cursor query string false "Cursor from previous page for cursor-based pagination (overrides offset)"
// @Success     200 {object} models.DeploymentLogResult
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/deploy-log [get]
func (h *InstanceHandler) GetDeployLog(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	if h.deployLogRepo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Deployment log service not configured"})
		return
	}

	// Verify instance exists.
	if _, err := h.instanceRepo.FindByID(id); err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	filters := models.DeploymentLogFilters{
		InstanceID: id,
		Cursor:     c.Query("cursor"),
	}

	if limitStr := c.Query("limit"); limitStr != "" {
		l, err := strconv.Atoi(limitStr)
		if err != nil || l < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid limit parameter"})
			return
		}
		filters.Limit = l
	}

	if offsetStr := c.Query("offset"); offsetStr != "" {
		o, err := strconv.Atoi(offsetStr)
		if err != nil || o < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid offset parameter"})
			return
		}
		filters.Offset = o
	}

	result, err := h.deployLogRepo.ListByInstancePaginated(c.Request.Context(), filters)
	if err != nil {
		status, message := mapError(err, "Deployment log")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, result)
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
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
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
				logKeyInstanceID, id,
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
		nsStatus, err := client.GetNamespaceStatus(c.Request.Context(), inst.Namespace, k8s.StatusOptions{})
		if err != nil {
			slog.Error("Failed to get namespace status",
				logKeyInstanceID, id,
				"namespace", inst.Namespace,
				"error", err,
			)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}
		c.JSON(http.StatusOK, nsStatus)
		return
	}

	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "K8s monitoring not configured"})
}

// GetInstancePods godoc
// @Summary     Get instance pod status
// @Description Returns detailed pod health including container states, conditions, and recent events
// @Tags        stack-instances
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "Instance ID"
// @Success     200 {object} k8s.NamespaceStatus
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Failure     502 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Failure     504 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/pods [get]
func (h *InstanceHandler) GetInstancePods(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Always query directly with events — the watcher cache does not include events.
	if h.registry != nil {
		client, clientErr := h.registry.GetK8sClient(inst.ClusterID)
		if clientErr != nil {
			slog.Warn("Failed to get k8s client for instance pods",
				logKeyInstanceID, id,
				"cluster_id", inst.ClusterID,
				"error", clientErr,
			)
			var dbErr *dberrors.DatabaseError
			if errors.As(clientErr, &dbErr) && errors.Is(dbErr.Unwrap(), dberrors.ErrNotFound) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown cluster_id"})
			} else {
				c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to connect to cluster"})
			}
			return
		}
		statusCtx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()

		nsStatus, nsErr := client.GetNamespaceStatus(statusCtx, inst.Namespace, k8s.StatusOptions{IncludeEvents: true})
		if nsErr != nil {
			if statusCtx.Err() == context.DeadlineExceeded {
				slog.Error("Timed out getting namespace status for pods",
					logKeyInstanceID, id,
					"namespace", inst.Namespace,
					"error", nsErr,
				)
				c.JSON(http.StatusGatewayTimeout, gin.H{"error": "Timed out fetching pod status"})
				return
			}
			slog.Error("Failed to get namespace status for pods",
				logKeyInstanceID, id,
				"namespace", inst.Namespace,
				"error", nsErr,
			)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
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

// extendTTLRequest is the optional request body for the ExtendTTL endpoint.
type extendTTLRequest struct {
	// Minutes adds this many minutes to the current expiry (or to now when
	// the instance already expired or has no expiry time). Must be > 0.
	Minutes *int `json:"minutes,omitempty"`
	// TTLMinutes is deprecated. Sent alone, it keeps the old behaviour: set
	// the TTL to this value and the expiry to now + TTL. Use minutes instead.
	TTLMinutes *int `json:"ttl_minutes,omitempty"`
}

// msgExtendTTLDeprecated is logged and returned in a Warning header when a
// client sends the deprecated ttl_minutes body to the extend endpoint.
const msgExtendTTLDeprecated = `299 - "ttl_minutes on /extend is deprecated and resets the expiry to now + ttl_minutes; send {\"minutes\": N} to add N minutes"`

// ExtendTTL godoc
// @Summary     Extend instance TTL
// @Description Extend the expiry time of a stack instance. The extend never makes the expiry earlier and never changes ttl_minutes.
// @Description - {"minutes": N}: adds N minutes (N > 0) to the current expires_at, or to now when the instance already expired or has no expiry time. The new expiry is capped at now + 43200 minutes (30 days).
// @Description - Empty body: adds the instance's ttl_minutes in the same way (400 when ttl_minutes is 0).
// @Description - Deprecated {"ttl_minutes": N} (without minutes): the old behaviour for older clients (stackctl 0.4.0 and earlier). It sets ttl_minutes to N and expires_at to now + N, which can make the expiry earlier. The response has a Warning header; the server logs a deprecation warning.
// @Description An instance without TTL and without expiry time has nothing to extend (400).
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id  path     string          true  "Instance ID"
// @Param       body body    extendTTLRequest false "Minutes to add (or the deprecated ttl_minutes)"
// @Success     200 {object} models.StackInstance "The instance with the new expires_at"
// @Header      200 {string} Warning "Set when the deprecated ttl_minutes body is used"
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "Caller is not the owner, an admin or a devops user"
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/extend [post]
func (h *InstanceHandler) ExtendTTL(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: only the owner, an admin or a devops user may extend the TTL.
	if !requireInstanceModify(c, inst) {
		return
	}

	var req extendTTLRequest
	// Body is optional — only bind if the client sent content.
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
			return
		}
	}

	now := time.Now().UTC()

	switch {
	case req.Minutes != nil:
		if *req.Minutes <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "minutes must be greater than 0"})
			return
		}
		if inst.TTLMinutes <= 0 && inst.ExpiresAt == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Instance has no expiry; nothing to extend"})
			return
		}
		inst.ExpiresAt = extendExpiry(inst.ExpiresAt, *req.Minutes, now)

	case req.TTLMinutes != nil && *req.TTLMinutes != 0:
		// Deprecated: old clients send ttl_minutes. Keep the old semantics.
		ttl := *req.TTLMinutes
		if ttl < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ttl_minutes must be greater than 0"})
			return
		}
		if ttl > MaxTTLMinutes {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf(msgTTLExceedsMax, MaxTTLMinutes)})
			return
		}
		slog.Warn("deprecated ttl_minutes body on extend; the expiry is reset to now + ttl_minutes",
			logKeyInstanceID, id, "ttl_minutes", ttl, "user_id", middleware.GetUserIDFromContext(c))
		c.Header("Warning", msgExtendTTLDeprecated)
		inst.TTLMinutes = ttl
		exp := now.Add(time.Duration(ttl) * time.Minute)
		inst.ExpiresAt = &exp

	default:
		// Empty body: extend by one TTL period.
		if inst.TTLMinutes <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "No TTL configured for this instance"})
			return
		}
		inst.ExpiresAt = extendExpiry(inst.ExpiresAt, inst.TTLMinutes, now)
	}
	inst.UpdatedAt = now

	if err := h.instanceRepo.Update(inst); err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	h.setInstanceNames(inst)
	c.JSON(http.StatusOK, inst)
}

// deployExpiry returns the expiry after a deploy: now + ttl, or the current
// expiry when it is later (a redeploy never shortens an extended expiry).
func deployExpiry(current *time.Time, ttlMinutes int, now time.Time) *time.Time {
	exp := now.Add(time.Duration(ttlMinutes) * time.Minute)
	if current != nil && current.After(exp) {
		exp = *current
	}
	return &exp
}

// extendExpiry returns the expiry after adding minutes to current (or to now
// when current is nil or in the past). The result is capped at now +
// MaxTTLMinutes, but it is never earlier than current.
func extendExpiry(current *time.Time, minutes int, now time.Time) *time.Time {
	if minutes > MaxTTLMinutes {
		minutes = MaxTTLMinutes
	}
	base := now
	if current != nil && current.After(now) {
		base = *current
	}
	exp := base.Add(time.Duration(minutes) * time.Minute)
	if limit := now.Add(time.Duration(MaxTTLMinutes) * time.Minute); exp.After(limit) {
		exp = limit
	}
	if current != nil && current.After(exp) {
		exp = *current
	}
	return &exp
}

// CompareInstanceSummary is the summary info for one side of a comparison.
type CompareInstanceSummary struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DefinitionName string `json:"definition_name"`
	Branch         string `json:"branch"`
	Owner          string `json:"owner"`
}

// CompareChartDiff holds per-chart comparison data.
type CompareChartDiff struct {
	ChartName      string  `json:"chart_name"`
	LeftValues     *string `json:"left_values"`
	RightValues    *string `json:"right_values"`
	HasDifferences bool    `json:"has_differences"`
}

// CompareInstancesResponse is the response for the compare endpoint.
type CompareInstancesResponse struct {
	Left   CompareInstanceSummary `json:"left"`
	Right  CompareInstanceSummary `json:"right"`
	Charts []CompareChartDiff     `json:"charts"`
}

// CompareInstances godoc
// @Summary     Compare two stack instances
// @Description Compare the merged values of two stack instances side-by-side, per chart. The merged values use the deploy pipeline: cluster shared values, chart defaults, value overrides, locked template values and chart branch overrides. charts[].has_differences is true when the merged YAML differs or the chart exists on one side only.
// @Tags        stack-instances
// @Produce     json
// @Param       left  query    string true "Left instance ID"
// @Param       right query    string true "Right instance ID"
// @Success     200   {object} CompareInstancesResponse
// @Failure     400   {object} map[string]string
// @Failure     401   {object} map[string]string
// @Failure     404   {object} map[string]string
// @Failure     500   {object} map[string]string
// @Router      /api/v1/stack-instances/compare [get]
func (h *InstanceHandler) CompareInstances(c *gin.Context) {
	leftID := c.Query("left")
	rightID := c.Query("right")

	if leftID == "" || rightID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Both 'left' and 'right' query parameters are required"})
		return
	}

	// Fetch left instance.
	leftInst, err := h.instanceRepo.FindByID(leftID)
	if err != nil {
		status, message := mapError(err, "Left stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Fetch right instance.
	rightInst, err := h.instanceRepo.FindByID(rightID)
	if err != nil {
		status, message := mapError(err, "Right stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Fetch definitions.
	leftDef, err := h.definitionRepo.FindByID(leftInst.StackDefinitionID)
	if err != nil {
		slog.Error("compare: failed to fetch left definition", logKeyInstanceID, leftID, "error", err)
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	rightDef, err := h.definitionRepo.FindByID(rightInst.StackDefinitionID)
	if err != nil {
		slog.Error("compare: failed to fetch right definition", logKeyInstanceID, rightID, "error", err)
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Fetch chart configs for both definitions.
	leftCharts, err := h.chartConfigRepo.ListByDefinition(leftDef.ID)
	if err != nil {
		slog.Error("compare: failed to fetch left chart configs", "definition_id", leftDef.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	rightCharts, err := h.chartConfigRepo.ListByDefinition(rightDef.ID)
	if err != nil {
		slog.Error("compare: failed to fetch right chart configs", "definition_id", rightDef.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Generate merged values per chart with the deploy pipeline (shared
	// values, chart defaults, value overrides, locked values, branch
	// overrides), so compare shows what a deploy of each instance renders.
	leftValuesMap, err := h.buildChartValues(c.Request.Context(), leftInst, leftDef, leftCharts)
	if err != nil {
		slog.Error("compare: failed to build left values", logKeyInstanceID, leftID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	rightValuesMap, err := h.buildChartValues(c.Request.Context(), rightInst, rightDef, rightCharts)
	if err != nil {
		slog.Error("compare: failed to build right values", logKeyInstanceID, rightID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Resolve owner names for the summaries.
	leftOwner := resolveOwnerName(h.userRepo, leftInst.OwnerID)
	rightOwner := resolveOwnerName(h.userRepo, rightInst.OwnerID)

	// Collect all chart names from both sides.
	allChartNames := make(map[string]bool)
	for name := range leftValuesMap {
		allChartNames[name] = true
	}
	for name := range rightValuesMap {
		allChartNames[name] = true
	}

	// Build sorted chart names for deterministic output.
	sortedNames := make([]string, 0, len(allChartNames))
	for name := range allChartNames {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)

	// Build per-chart diffs.
	charts := make([]CompareChartDiff, 0, len(sortedNames))
	for _, name := range sortedNames {
		diff := CompareChartDiff{ChartName: name}

		leftVal, leftOK := leftValuesMap[name]
		rightVal, rightOK := rightValuesMap[name]

		if leftOK {
			diff.LeftValues = &leftVal
		}
		if rightOK {
			diff.RightValues = &rightVal
		}

		// Determine differences: charts missing on one side always differ.
		if leftOK && rightOK {
			diff.HasDifferences = leftVal != rightVal
		} else {
			diff.HasDifferences = true
		}

		charts = append(charts, diff)
	}

	resp := CompareInstancesResponse{
		Left: CompareInstanceSummary{
			ID:             leftInst.ID,
			Name:           leftInst.Name,
			DefinitionName: leftDef.Name,
			Branch:         leftInst.Branch,
			Owner:          leftOwner,
		},
		Right: CompareInstanceSummary{
			ID:             rightInst.ID,
			Name:           rightInst.Name,
			DefinitionName: rightDef.Name,
			Branch:         rightInst.Branch,
			Owner:          rightOwner,
		},
		Charts: charts,
	}

	c.JSON(http.StatusOK, resp)
}

// buildChartValues generates merged Helm values YAML for each chart in a stack
// instance. It returns a map of chartName → YAML string.
func (h *InstanceHandler) buildChartValues(ctx context.Context, inst *models.StackInstance, def *models.StackDefinition, charts []models.ChartConfig) (map[string]string, error) {
	values, _, err := h.buildChartValuesAndBranches(ctx, inst, def, charts)
	return values, err
}

// buildChartValuesAndBranches is buildChartValues that also returns the
// per-chart branch overrides (chart config ID -> branch) it used, so the
// deployer reports the same branch and image tag to hooks as the rendered
// values. See valuesBuilder.
func (h *InstanceHandler) buildChartValuesAndBranches(ctx context.Context, inst *models.StackInstance, def *models.StackDefinition, charts []models.ChartConfig) (map[string]string, map[string]string, error) {
	return h.values().build(ctx, inst, def, charts, "")
}

// chartValueLayers returns the values layers of each chart (see
// valuesBuilder.layers).
func (h *InstanceHandler) chartValueLayers(ctx context.Context, inst *models.StackInstance, def *models.StackDefinition, charts []models.ChartConfig) ([]helm.ChartValues, helm.TemplateVars, map[string]string, error) {
	return h.values().layers(ctx, inst, def, charts, "")
}

// buildLockedValuesMap returns chartName → lockedValues for a definition's source template.
func (h *InstanceHandler) buildLockedValuesMap(ctx context.Context, def *models.StackDefinition) (map[string]string, error) {
	return h.values().lockedValues(ctx, def)
}

// values returns the values pipeline over the handler's repositories.
func (h *InstanceHandler) values() *valuesBuilder {
	b := &valuesBuilder{
		overrideRepo:       h.overrideRepo,
		branchOverrideRepo: h.branchOverrideRepo,
		templateChartRepo:  h.templateChartRepo,
		versionRepo:        h.versionRepo,
		userRepo:           h.userRepo,
		valuesGen:          h.valuesGen,
		sharedValuesRepo:   h.sharedValuesRepo,
		clusterRepo:        h.clusterRepo,
	}
	if h.registry != nil {
		b.resolver = h.registry
	}
	return b
}

// rollbackRequest is the optional request body for RollbackInstance.
type rollbackRequest struct {
	// TargetLogID is the ID of a successful deploy log of this instance.
	TargetLogID string `json:"target_log_id"`
}

// RollbackInstance godoc
// @Summary     Rollback a stack instance
// @Description Without target_log_id: roll back every Helm release of the instance by one revision (helm rollback).
// @Description With target_log_id: restore the values of that deploy. The log must be a successful deploy of this instance with a values snapshot (404 when the log does not exist or belongs to another instance, 400 otherwise). Each chart of the target deploy is upgraded (helm upgrade --install) with the merged values of that deploy and the chart version that the deploy recorded; deploys from before the version recording use the current chart version. Charts that the target deploy did not include are left unchanged.
// @Description A rollback to a target restores the stored VALUES of that deploy (including the shared and locked values of that time), not the images. Image tags that are branch names can point to newer images now. A chart without a recorded version (the deploy used an empty chart version) installs the newest chart. Pre-deploy hooks do not run for a rollback; subscribe image gates to pre-rollback, which gets the same chart list (name, version, branch, image_tag) plus metadata rollback_mode, target_log_id and target_branch.
// @Description The pre-rollback hook runs in the background after the 202 answer, with progress streaming like pre-deploy. A rejection ends the rollback log with status error and the hook reason; the instance gets its previous status back. For a one-revision rollback the hook chart branches come from the previous successful deploy log when it recorded a branch.
// @Description In both modes a release stuck in pending-* is cleared before each chart, and after the rollback the instance waits for pod readiness (stabilizing) when readiness gating is configured, as for a deploy. A one-revision rollback that fails records the values of the charts that it already rolled back.
// @Description The rollback does not change the stored value or branch overrides. After a successful rollback the deploy preview compares against the running values; values_drift (in this response for a target, and in the deploy preview) shows that the next deploy applies the stored overrides again.
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       id   path     string          true  "Instance ID"
// @Param       body body     rollbackRequest false "Optional rollback target"
// @Success     202 {object} RollbackResponse
// @Failure     400 {object} map[string]string "Invalid body, no charts, or the target is not a successful deploy with a values snapshot"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "Caller is not the owner, an admin or a devops user"
// @Failure     404 {object} map[string]string "Instance or target deploy log not found"
// @Failure     409 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/rollback [post]
func (h *InstanceHandler) RollbackInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	if h.deployManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": msgDeployerNotConfigured})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: only the owner, an admin or a devops user may roll back the instance.
	// Checked before any side effect (hooks, status change, deploy log, Helm).
	if !requireInstanceModify(c, inst) {
		return
	}

	switch inst.Status {
	case models.StackStatusRunning, models.StackStatusPartial, models.StackStatusError:
		// OK
	default:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Cannot rollback: instance is in state %s", inst.Status)})
		return
	}

	var body rollbackRequest
	if c.Request.Body != nil && c.Request.Body != http.NoBody {
		if bindErr := c.ShouldBindJSON(&body); bindErr != nil && !errors.Is(bindErr, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
			return
		}
	}

	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(def.ID)
	if err != nil {
		status, message := mapError(err, entityChartConfigs)
		c.JSON(status, gin.H{"error": message})
		return
	}

	if len(charts) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No charts configured for this stack definition"})
		return
	}

	resp := RollbackResponse{Message: "Rollback started", TargetLogID: body.TargetLogID}
	target := &rollbackTargetData{}
	if body.TargetLogID != "" {
		target, err = h.loadRollbackTarget(c.Request.Context(), id, body.TargetLogID)
		if err != nil {
			var targetErr *rollbackTargetError
			if errors.As(err, &targetErr) {
				c.JSON(targetErr.status, gin.H{"error": targetErr.message})
				return
			}
			slog.Error("Failed to load rollback target", logKeyInstanceID, id, "target_log_id", body.TargetLogID, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}
		pending, buildErr := h.buildChartValues(c.Request.Context(), inst, def, charts)
		if buildErr != nil {
			logRollbackDriftError(id, buildErr)
		} else {
			drift := targetValuesDrift(pending, target.values)
			resp.ValuesDrift = &drift
			if drift {
				resp.Warning = msgValuesDrift
			}
		}
	}

	var chartInfos []deployer.ChartDeployInfo
	for _, ch := range charts {
		chartInfos = append(chartInfos, deployer.ChartDeployInfo{ChartConfig: ch})
	}

	logID, err := h.deployManager.Rollback(c.Request.Context(), deployer.RollbackRequest{
		Instance:            inst,
		Charts:              chartInfos,
		TargetLogID:         body.TargetLogID,
		TargetValues:        target.values,
		TargetChartVersions: target.versions,
		TargetBranch:        target.branch,
	})
	if err != nil {
		slog.Error("Failed to start rollback",
			logKeyInstanceID, id,
			"error", err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	resp.LogID = logID
	c.JSON(http.StatusAccepted, resp)
}

// GetDeployLogValues godoc
// @Summary     Get values snapshot for a deployment log entry
// @Description Returns the merged Helm values that were used for a specific deployment
// @Tags        stack-instances
// @Produce     json
// @Security    BearerAuth
// @Param       id    path     string true "Instance ID"
// @Param       logId path     string true "Deployment Log ID"
// @Success     200 {object} map[string]string
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/deploy-log/{logId}/values [get]
func (h *InstanceHandler) GetDeployLogValues(c *gin.Context) {
	instanceID := c.Param("id")
	logID := c.Param("logId")
	if instanceID == "" || logID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Instance ID and log ID are required"})
		return
	}

	if h.deployLogRepo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Deployment log service not configured"})
		return
	}

	if _, err := h.instanceRepo.FindByID(instanceID); err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	logEntry, err := h.deployLogRepo.FindByID(c.Request.Context(), logID)
	if err != nil {
		status, message := mapError(err, "Deployment log")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if logEntry.StackInstanceID != instanceID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment log not found for this instance"})
		return
	}

	if logEntry.ValuesSnapshot == "" {
		c.JSON(http.StatusOK, gin.H{
			"log_id": logID,
			"values": nil,
		})
		return
	}

	var values map[string]interface{}
	if err := json.Unmarshal([]byte(logEntry.ValuesSnapshot), &values); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"log_id": logID,
			"values": logEntry.ValuesSnapshot,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"log_id": logID,
		"values": values,
	})
}
