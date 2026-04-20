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
// @Description List stack instances with server-side pagination. Supports page/pageSize or legacy limit/offset params. Use owner=me to filter by the authenticated user.
// @Tags        stack-instances
// @Produce     json
// @Param       owner    query    string false "Filter by owner (use 'me' for current user)"
// @Param       page     query    int    false "Page number (1-based, default: 1)"
// @Param       pageSize query    int    false "Results per page (default: 25, max: 100)"
// @Param       limit    query    int    false "Legacy: maximum number of results"
// @Param       offset   query    int    false "Legacy: number of results to skip"
// @Success     200   {object} map[string]interface{} "data: []StackInstance, total: int, page: int, pageSize: int"
// @Failure     500   {object} map[string]string
// @Router      /api/v1/stack-instances [get]
func (h *InstanceHandler) ListInstances(c *gin.Context) {
	owner := c.Query("owner")

	// Owner-filtered list — small result set, no server-side pagination needed.
	if owner == "me" {
		userID := middleware.GetUserIDFromContext(c)
		instances, err := h.instanceRepo.ListByOwner(userID)
		if err != nil {
			status, message := mapError(err, entityStackInstance)
			c.JSON(status, gin.H{"error": message})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"data":     instances,
			"total":    len(instances),
			"page":     1,
			"pageSize": len(instances),
		})
		return
	}

	// Determine limit and offset from query params.
	// page/pageSize take precedence; fall back to legacy limit/offset.
	pageSize := listPageSizeDefault
	offset := 0
	page := 1

	if ps := c.Query("pageSize"); ps != "" {
		if v, err := strconv.Atoi(ps); err == nil && v > 0 {
			pageSize = v
		}
		if pageSize > listPageSizeMax {
			pageSize = listPageSizeMax
		}
	}

	if p := c.Query("page"); p != "" {
		// Page-based pagination
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
		offset = (page - 1) * pageSize
	} else if l := c.Query("limit"); l != "" {
		// Legacy limit/offset pagination (no page param)
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			pageSize = v
			if pageSize > listPageSizeMax {
				pageSize = listPageSizeMax
			}
		}
		if o := c.Query("offset"); o != "" {
			if v, err := strconv.Atoi(o); err == nil && v >= 0 {
				offset = v
			}
		}
	}

	instances, total, err := h.instanceRepo.ListPaged(pageSize, offset)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

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
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	inst, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
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
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	existing, err := h.instanceRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Authorization: only the owner or an admin may update the instance.
	userID := middleware.GetUserIDFromContext(c)
	role := middleware.GetRoleFromContext(c)
	if existing.OwnerID != userID && role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not allowed to modify this stack instance"})
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

	if update.Name != nil {
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
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	// Look up the instance for the hook envelope and the delete itself.
	// A failed lookup is a real error — don't silently proceed with a nil
	// snapshot that would make the pre-delete hook a no-op.
	var snapshot *models.StackInstance
	if h.hooks != nil {
		inst, lookupErr := h.instanceRepo.FindByID(id)
		if lookupErr != nil {
			status, message := mapError(lookupErr, entityStackInstance)
			c.JSON(status, gin.H{"error": message})
			return
		}
		snapshot = inst
	}

	// Pre-instance-delete hook: a subscriber with failure_policy=fail can block
	// the delete (e.g. enforce dependency checks).
	if err := h.fireInstanceHook(c.Request.Context(), hooks.EventPreInstanceDelete, snapshot); err != nil {
		slog.Error("pre-instance-delete hook failed", logKeyInstanceID, id, "error", err)
		c.JSON(http.StatusForbidden, gin.H{"error": "pre-instance-delete hook rejected the request"})
		return
	}

	if h.txRunner != nil {
		// Transactional path — branch override cleanup + instance delete are atomic.
		txErr := h.txRunner.RunInTx(func(repos database.TxRepos) error {
			if err := repos.BranchOverride.DeleteByInstance(id); err != nil {
				return err
			}
			return repos.StackInstance.Delete(id)
		})
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

	// Post-instance-delete hook: notify only; the delete is committed.
	_ = h.fireInstanceHook(c.Request.Context(), hooks.EventPostInstanceDelete, snapshot)

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
		status, message := mapError(err, entityStackInstance)
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

	// Transactional path — instance create + override copies are atomic.
	overrides, listErr := h.overrideRepo.ListByInstance(source.ID)
	if listErr != nil {
		overrides = nil // proceed without overrides
	}

	txErr := h.txRunner.RunInTx(func(repos database.TxRepos) error {
		if err := repos.StackInstance.Create(clone); err != nil {
			return err
		}
		for _, ov := range overrides {
			clonedOV := &models.ValueOverride{
				ID:              uuid.New().String(),
				StackInstanceID: clone.ID,
				ChartConfigID:   ov.ChartConfigID,
				Values:          ov.Values,
				UpdatedAt:       now,
			}
			if err := repos.ValueOverride.Create(clonedOV); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		status, message := mapError(txErr, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
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
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	chart, err := h.chartConfigRepo.FindByID(chartID)
	if err != nil {
		status, message := mapError(err, entityChartConfig)
		c.JSON(status, gin.H{"error": message})
		return
	}

	def, err := h.definitionRepo.FindByID(inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
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

	// Resolve per-chart branch override.
	var chartBranch string
	if h.branchOverrideRepo != nil {
		bo, boErr := h.branchOverrideRepo.Get(instanceID, chartID)
		if boErr == nil && bo != nil {
			chartBranch = bo.Branch
		}
	}

	params := helm.GenerateParams{
		ChartName:      chart.ChartName,
		DefaultValues:  chart.DefaultValues,
		LockedValues:   lockedValues,
		OverrideValues: overrideValues,
		ChartBranch:    chartBranch,
		TemplateVars: helm.TemplateVars{
			Branch:       inst.Branch,
			ImageTag:     helm.SanitizeImageTag(inst.Branch),
			Namespace:    inst.Namespace,
			InstanceName: inst.Name,
			StackName:    def.Name,
			Owner:        ownerName,
		},
	}

	yamlData, err := h.valuesGen.GenerateValues(c.Request.Context(), params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
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
			ImageTag:     helm.SanitizeImageTag(inst.Branch),
			Namespace:    inst.Namespace,
			InstanceName: inst.Name,
			StackName:    def.Name,
			Owner:        ownerName,
		},
	}

	allValues, err := h.valuesGen.ExportAsZip(c.Request.Context(), params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
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

	// Reset TTL expiry clock on deploy.
	if inst.TTLMinutes > 0 {
		exp := time.Now().UTC().Add(time.Duration(inst.TTLMinutes) * time.Minute)
		inst.ExpiresAt = &exp
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

	valuesMap, err := h.buildChartValues(c.Request.Context(), inst, def, charts)
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
// @Description Compare pending merged values against last-deployed values per chart
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

	// Authorization: only the owner or an admin/devops may preview the instance.
	userID := middleware.GetUserIDFromContext(c)
	role := middleware.GetRoleFromContext(c)
	if inst.OwnerID != userID && role != "admin" && role != "devops" {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not allowed to preview this stack instance"})
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
			HasChanges:     pending != previous,
		})
	}

	c.JSON(http.StatusOK, DeployPreviewResponse{
		InstanceID:   inst.ID,
		InstanceName: inst.Name,
		Charts:       chartPreviews,
	})
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
	TTLMinutes int `json:"ttl_minutes"`
}

// ExtendTTL godoc
// @Summary     Extend instance TTL
// @Description Extend the expiry time of a stack instance. Uses provided ttl_minutes or the instance's existing TTLMinutes.
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id  path     string          true  "Instance ID"
// @Param       body body    extendTTLRequest false "Optional TTL override"
// @Success     200 {object} models.StackInstance
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
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

	// Authorization: only the owner or an admin may extend the TTL.
	userID := middleware.GetUserIDFromContext(c)
	role := middleware.GetRoleFromContext(c)
	if inst.OwnerID != userID && role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not allowed to modify this stack instance"})
		return
	}

	var req extendTTLRequest
	// Body is optional — only bind if the client sent content.
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
			return
		}
	}

	ttl := req.TTLMinutes
	if ttl == 0 {
		ttl = inst.TTLMinutes
	}
	if ttl <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No TTL configured for this instance"})
		return
	}
	if ttl > MaxTTLMinutes {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf(msgTTLExceedsMax, MaxTTLMinutes)})
		return
	}

	inst.TTLMinutes = ttl
	exp := time.Now().UTC().Add(time.Duration(ttl) * time.Minute)
	inst.ExpiresAt = &exp
	inst.UpdatedAt = time.Now().UTC()

	if err := h.instanceRepo.Update(inst); err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, inst)
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
// @Description Compare the merged values of two stack instances side-by-side, per chart
// @Tags        stack-instances
// @Produce     json
// @Param       left  query    string true "Left instance ID"
// @Param       right query    string true "Right instance ID"
// @Success     200   {object} CompareInstancesResponse
// @Failure     400   {object} map[string]string
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

	// Fetch overrides for both instances.
	leftOverrides, err := h.overrideRepo.ListByInstance(leftID)
	if err != nil {
		slog.Error("compare: failed to fetch left overrides", logKeyInstanceID, leftID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	rightOverrides, err := h.overrideRepo.ListByInstance(rightID)
	if err != nil {
		slog.Error("compare: failed to fetch right overrides", logKeyInstanceID, rightID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	leftOverrideMap := make(map[string]string) // chartConfigID → values
	for _, ov := range leftOverrides {
		leftOverrideMap[ov.ChartConfigID] = ov.Values
	}
	rightOverrideMap := make(map[string]string)
	for _, ov := range rightOverrides {
		rightOverrideMap[ov.ChartConfigID] = ov.Values
	}

	// Build locked values maps from templates.
	leftLockedMap, err := h.buildLockedValuesMap(leftDef)
	if err != nil {
		slog.Error("compare: failed to build left locked values", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	rightLockedMap, err := h.buildLockedValuesMap(rightDef)
	if err != nil {
		slog.Error("compare: failed to build right locked values", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Resolve owner names.
	leftOwner := resolveOwnerName(h.userRepo, leftInst.OwnerID)
	rightOwner := resolveOwnerName(h.userRepo, rightInst.OwnerID)

	// Generate merged values for left charts.
	leftValuesMap := make(map[string]string) // chartName → merged YAML
	for _, ch := range leftCharts {
		yamlBytes, err := h.valuesGen.GenerateValues(c.Request.Context(), helm.GenerateParams{
			ChartName:      ch.ChartName,
			DefaultValues:  ch.DefaultValues,
			LockedValues:   leftLockedMap[ch.ChartName],
			OverrideValues: leftOverrideMap[ch.ID],
			TemplateVars: helm.TemplateVars{
				Branch:       leftInst.Branch,
				Namespace:    leftInst.Namespace,
				InstanceName: leftInst.Name,
				StackName:    leftDef.Name,
				Owner:        leftOwner,
			},
		})
		if err != nil {
			slog.Error("compare: failed to generate left values", "chart", ch.ChartName, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}
		leftValuesMap[ch.ChartName] = string(yamlBytes)
	}

	// Generate merged values for right charts.
	rightValuesMap := make(map[string]string)
	for _, ch := range rightCharts {
		yamlBytes, err := h.valuesGen.GenerateValues(c.Request.Context(), helm.GenerateParams{
			ChartName:      ch.ChartName,
			DefaultValues:  ch.DefaultValues,
			LockedValues:   rightLockedMap[ch.ChartName],
			OverrideValues: rightOverrideMap[ch.ID],
			TemplateVars: helm.TemplateVars{
				Branch:       rightInst.Branch,
				Namespace:    rightInst.Namespace,
				InstanceName: rightInst.Name,
				StackName:    rightDef.Name,
				Owner:        rightOwner,
			},
		})
		if err != nil {
			slog.Error("compare: failed to generate right values", "chart", ch.ChartName, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}
		rightValuesMap[ch.ChartName] = string(yamlBytes)
	}

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
	lockedMap, err := h.buildLockedValuesMap(def)
	if err != nil {
		return nil, fmt.Errorf("build locked values: %w", err)
	}

	overridesMap := make(map[string]string)
	overrides, err := h.overrideRepo.ListByInstance(inst.ID)
	if err != nil {
		return nil, fmt.Errorf("list value overrides: %w", err)
	}
	for _, ov := range overrides {
		overridesMap[ov.ChartConfigID] = ov.Values
	}

	branchMap := make(map[string]string)
	if h.branchOverrideRepo != nil {
		branchOverrides, err := h.branchOverrideRepo.List(inst.ID)
		if err != nil {
			return nil, fmt.Errorf("list branch overrides: %w", err)
		}
		for _, bo := range branchOverrides {
			branchMap[bo.ChartConfigID] = bo.Branch
		}
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

	result := make(map[string]string, len(charts))
	for _, ch := range charts {
		yamlData, err := h.valuesGen.GenerateValues(ctx, helm.GenerateParams{
			ChartName:      ch.ChartName,
			DefaultValues:  ch.DefaultValues,
			LockedValues:   lockedMap[ch.ChartName],
			OverrideValues: overridesMap[ch.ID],
			ChartBranch:    branchMap[ch.ID],
			TemplateVars:   templateVars,
		})
		if err != nil {
			return nil, fmt.Errorf("generate values for chart %s: %w", ch.ChartName, err)
		}
		result[ch.ChartName] = string(yamlData)
	}

	return result, nil
}

// buildLockedValuesMap returns chartName → lockedValues for a definition's source template.
func (h *InstanceHandler) buildLockedValuesMap(def *models.StackDefinition) (map[string]string, error) {
	lockedMap := make(map[string]string)
	if def.SourceTemplateID != "" && h.templateChartRepo != nil {
		templateCharts, err := h.templateChartRepo.ListByTemplate(def.SourceTemplateID)
		if err != nil {
			return nil, fmt.Errorf("list template chart configs: %w", err)
		}
		for _, tc := range templateCharts {
			lockedMap[tc.ChartName] = tc.LockedValues
		}
	}
	return lockedMap, nil
}

// RollbackInstance godoc
// @Summary     Rollback a stack instance
// @Description Rollback all Helm releases in a stack instance to their previous revision
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id   path     string true "Instance ID"
// @Param       body body     object false "Optional: {\"target_log_id\": \"...\"}"
// @Success     202 {object} map[string]string
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string
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

	switch inst.Status {
	case models.StackStatusRunning, models.StackStatusError:
		// OK
	default:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Cannot rollback: instance is in state %s", inst.Status)})
		return
	}

	var body struct {
		TargetLogID string `json:"target_log_id"`
	}
	if c.Request.Body != nil && c.Request.Body != http.NoBody {
		_ = c.ShouldBindJSON(&body)
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

	var chartInfos []deployer.ChartDeployInfo
	for _, ch := range charts {
		chartInfos = append(chartInfos, deployer.ChartDeployInfo{ChartConfig: ch})
	}

	logID, err := h.deployManager.Rollback(c.Request.Context(), deployer.RollbackRequest{
		Instance:    inst,
		Charts:      chartInfos,
		TargetLogID: body.TargetLogID,
	})
	if err != nil {
		slog.Error("Failed to start rollback",
			logKeyInstanceID, id,
			"error", err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"log_id": logID, "message": "Rollback started"})
}

// GetDeployLogValues godoc
// @Summary     Get values snapshot for a deployment log entry
// @Description Returns the merged Helm values that were used for a specific deployment
// @Tags        stack-instances
// @Produce     json
// @Param       id    path     string true "Instance ID"
// @Param       logId path     string true "Deployment Log ID"
// @Success     200 {object} map[string]string
// @Failure     404 {object} map[string]string
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
