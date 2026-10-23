package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"backend/internal/notifier"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
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
	return cutLabel(normalizeRFC1123(s), maxNamespaceLen)
}

// normalizeRFC1123 does the same as sanitizeRFC1123Label, but it does not cut
// the length.
func normalizeRFC1123(s string) string {
	s = strings.ToLower(s)
	s = rfc1123InvalidChars.ReplaceAllString(s, "-")
	s = rfc1123ConsecutiveDashes.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "default"
	}
	return s
}

// cutLabel cuts s to at most n bytes and removes the trailing dashes. s must
// be a normalized label (ASCII only, starts with a letter or digit).
func cutLabel(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimRight(s[:n], "-")
}

// Namespace name limits.
const (
	namespacePrefix = "stack-"
	// maxNamespaceLen is the maximum length of an RFC 1123 label.
	maxNamespaceLen = 63
	// namespaceHashLen is the number of hex characters of the name hash in a
	// cut namespace.
	namespaceHashLen = 6
	// minNamespaceInstanceLen is the minimum length of the instance part in a
	// cut namespace. When the owner part leaves less room, the owner part is cut.
	minNamespaceInstanceLen = 10
)

// buildNamespace returns the namespace stack-<instance>-<owner>. It sanitizes
// both parts. A name that fits in 63 characters is not changed, so existing
// namespaces stay the same.
//
// A longer name becomes stack-<instance>-<hash>-<owner>. The instance part is
// cut to fit. The owner part stays whole when the instance part keeps at least
// minNamespaceInstanceLen characters. Otherwise the owner part is cut too. The
// hash is the first 6 hex characters of the SHA-256 of the full sanitized
// instance and owner parts. Thus two names that differ only after the cut get
// different namespaces (issue #631).
func buildNamespace(instancePart, ownerPart string) string {
	instance := normalizeRFC1123(instancePart)
	owner := normalizeRFC1123(ownerPart)
	if ns := namespacePrefix + instance + "-" + owner; len(ns) <= maxNamespaceLen {
		return ns
	}

	sum := sha256.Sum256([]byte(instance + "/" + owner))
	hash := hex.EncodeToString(sum[:])[:namespaceHashLen]

	// room is the space for the instance part and the owner part. The prefix,
	// the hash and two "-" separators use the rest.
	room := maxNamespaceLen - len(namespacePrefix) - namespaceHashLen - 2
	instanceLen := room - len(owner)
	if instanceLen < minNamespaceInstanceLen {
		instanceLen = min(minNamespaceInstanceLen, len(instance))
	}
	ownerLen := room - instanceLen
	return namespacePrefix + cutLabel(instance, instanceLen) + "-" + hash + "-" + cutLabel(owner, ownerLen)
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
	maxTTLMinutes      int
	txRunner           database.TxRunner
	hooks              *hooks.Dispatcher
	actions            *hooks.ActionRegistry
	notifier           deployer.LifecycleNotifier
	sharedValuesRepo   models.SharedValuesRepository
	clusterQuotaRepo   models.ResourceQuotaRepository
	followerRepo       models.InstanceFollowerRepository
	actionRuns         models.ActionRunRepository
	// statusFlight lets concurrent status reads of one namespace share one
	// set of Kubernetes calls (namespaceStatus).
	statusFlight singleflight.Group
	uniqueNames  bool
}

// WithUniqueInstanceNames turns on UNIQUE_INSTANCE_NAMES: create, rename and
// clone refuse a name that another instance uses on the same cluster (409),
// and generated clone names skip those names. Returns h for chaining.
func (h *InstanceHandler) WithUniqueInstanceNames(enabled bool) *InstanceHandler {
	h.uniqueNames = enabled
	return h
}

// WithMaxTTLDays sets MAX_INSTANCE_TTL_DAYS: the maximum TTL and the maximum
// distance of a stop time from now. A value below 1 keeps the default (30
// days). Returns h for chaining.
func (h *InstanceHandler) WithMaxTTLDays(days int) *InstanceHandler {
	h.maxTTLMinutes = maxTTLMinutesFromDays(days)
	return h
}

// maxTTL returns the TTL limit of h in minutes (default MaxTTLMinutes).
func (h *InstanceHandler) maxTTL() int {
	if h.maxTTLMinutes > 0 {
		return h.maxTTLMinutes
	}
	return MaxTTLMinutes
}

// nameGuard returns the UNIQUE_INSTANCE_NAMES check of h.
func (h *InstanceHandler) nameGuard() instanceNameGuard {
	return instanceNameGuard{instanceRepo: h.instanceRepo, userRepo: h.userRepo, registry: h.registry}
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

// WithActionRuns attaches the action run repository: invoke stores a run,
// the job log proxy stores the final job status, and
// GET /stack-instances/{id}/action-runs lists the runs (#640). Without it no
// run is stored and the list is empty. Returns h for chaining.
func (h *InstanceHandler) WithActionRuns(repo models.ActionRunRepository) *InstanceHandler {
	h.actionRuns = repo
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
	env := hooks.EventEnvelope{InstanceRef: instanceRefFor(instance)}
	if t, ok := hooks.TriggerFromContext(ctx); ok {
		env.Trigger = &t
	}
	return h.hooks.Fire(ctx, event, env)
}

// hookTriggerCtx returns the request context with a user trigger (ID and
// username of the caller). The deploy manager and fireInstanceHook add it to
// the hook envelopes, so subscribers can show who started the operation.
func hookTriggerCtx(c *gin.Context) context.Context {
	return hooks.WithTrigger(c.Request.Context(), hooks.Trigger{
		Type: hooks.TriggerUser,
		ID:   middleware.GetUserIDFromContext(c),
		Name: middleware.GetUsernameFromContext(c),
	})
}

// writeCtx is hookTriggerCtx without the request cancel. Use it from the
// first instance write on (#656).
func writeCtx(c *gin.Context) context.Context {
	return context.WithoutCancel(hookTriggerCtx(c))
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

	instances, total, err := h.instanceRepo.ListPaged(c.Request.Context(), filter, pageSize, offset)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}
	if instances == nil {
		instances = []models.StackInstance{}
	}
	h.setInstanceListNames(c.Request.Context(), instances)

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

	instances, err := h.instanceRepo.ListByOwner(c.Request.Context(), userID)
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
	h.setInstanceListNames(c.Request.Context(), instances)

	c.JSON(http.StatusOK, instances)
}

// createInstanceRequest is the JSON-binding DTO for CreateInstance.
// TTLMinutes is *int so we can distinguish "omitted" (nil → use default)
// from an explicit 0 (meaning "no expiry").
type createInstanceRequest struct {
	// ExpiresAt is a fixed stop time (RFC 3339 with a time zone). It sets
	// ttl_minutes to 0, and a deploy keeps it. Do not send it with ttl_minutes.
	ExpiresAt         *time.Time `json:"expires_at" example:"2026-10-17T18:00:00+02:00"`
	StackDefinitionID string     `json:"stack_definition_id"`
	Name              string     `json:"name"`
	Branch            string     `json:"branch"`
	ClusterID         string     `json:"cluster_id"`
	TTLMinutes        *int       `json:"ttl_minutes"`
}

// CreateInstance godoc
// @Summary     Create a stack instance
// @Description Create a new stack instance from a definition.
// @Description The owner and the subscribed notification channels get the notification instance.created, with the caller as the trigger.
// @Description Expiry: ttl_minutes counts from the deploy (omitted: DEFAULT_INSTANCE_TTL_MINUTES; 0: no expiry). expires_at (RFC 3339 with a time zone) is a fixed stop time instead: ttl_minutes becomes 0, and a deploy keeps the stop time. expires_at must be in the future and not later than now + MAX_INSTANCE_TTL_DAYS (default 30). A TTL above that limit, or expires_at together with ttl_minutes, gives 400.
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       instance body     createInstanceRequest true "Instance object"
// @Success     201      {object} models.StackInstance
// @Failure     400      {object} map[string]string "Invalid body, or name is not a DNS label (lowercase a-z, 0-9, '-', start and end alphanumeric, at most 50 characters)"
// @Failure     401      {object} map[string]string
// @Failure     403      {object} map[string]string "A pre-instance-create hook rejected the request"
// @Failure     404      {object} map[string]string "Stack definition not found"
// @Failure     409      {object} NamespaceConflictResponse "Namespace already exists, the name is taken on the cluster (UNIQUE_INSTANCE_NAMES), or the per-user instance limit of the cluster is reached ({error})"
// @Failure     500      {object} map[string]string
// @Router      /api/v1/stack-instances [post]
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

	// The default TTL applies only when ttl_minutes and expires_at are both
	// omitted. An explicit 0 means "no expiry"; expires_at is a fixed stop time.
	ttl, expiresAt, ttlMsg := requestedExpiry(req.TTLMinutes, req.ExpiresAt, h.defaultTTLMinutes, h.maxTTL(), now)
	if ttlMsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": ttlMsg})
		return
	}
	inst.TTLMinutes = ttl
	inst.ExpiresAt = expiresAt

	// Resolve or validate ClusterID using the registry when available.
	// - If empty, resolve to the current default cluster so the persisted
	//   value is explicit and won't shift if the default changes.
	// - If non-empty, validate that it refers to a known cluster to avoid
	//   persisting invalid references that will only fail at deploy time.
	if h.registry != nil {
		if inst.ClusterID == "" {
			resolved, resolveErr := h.registry.ResolveClusterID("")
			if resolveErr != nil {
				respondDefaultClusterError(c, resolveErr)
				return
			}
			inst.ClusterID = resolved
		} else if !h.registry.ClusterExists(inst.ClusterID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown cluster_id"})
			return
		}
	}

	limit, ok := instanceLimitFor(c, h.clusterRepo, inst.ClusterID, unknownClusterID)
	if !ok {
		return
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
	if h.checkNamespaceUniqueness(c, inst.Namespace, inst.Name, inst.ClusterID, owner) {
		return
	}
	if h.uniqueNames && h.nameGuard().check(c, inst.Name, inst.ClusterID, "", owner) {
		return
	}

	// Verify definition exists.
	if _, err := h.definitionRepo.FindByID(c.Request.Context(), inst.StackDefinitionID); err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Pre-instance-create hook: a subscriber with failure_policy=fail can abort
	// before any DB write. Fired after field validation, namespace uniqueness,
	// and definition existence checks so subscribers only see creates that are
	// otherwise eligible to succeed.
	if err := h.fireInstanceHook(hookTriggerCtx(c), hooks.EventPreInstanceCreate, &inst); err != nil {
		slog.Error("pre-instance-create hook failed", "instance_name", inst.Name, "error", err)
		c.JSON(http.StatusForbidden, gin.H{"error": "pre-instance-create hook rejected the request"})
		return
	}

	wctx := writeCtx(c)
	create := func(repo models.StackInstanceRepository) error {
		if err := checkInstanceLimit(wctx, repo, limit, inst.OwnerID); err != nil {
			return err
		}
		return repo.Create(wctx, &inst)
	}
	var createErr error
	if h.txRunner != nil && limit.max > 0 {
		// The count locks the user row first, so concurrent creates of one
		// owner wait for each other. Without a txRunner the lock ends with
		// the count: two concurrent creates can then both pass.
		createErr = h.txRunner.RunInTx(wctx, func(repos database.TxRepos) error {
			return create(repos.StackInstance)
		})
	} else {
		createErr = create(h.instanceRepo)
	}
	if createErr != nil {
		if respondInstanceLimit(c, createErr, limit) {
			return
		}
		status, message := mapError(createErr, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Post-instance-create hook: ignore-by-default. Cannot undo the create at
	// this point; subscribers exist for downstream notification (CMDB sync,
	// audit logs, etc).
	_ = h.fireInstanceHook(wctx, hooks.EventPostInstanceCreate, &inst)

	notifyInstanceCreated(wctx, h.notifier, &inst)

	h.deployManager.BroadcastInstanceCreated(inst.ID)

	h.setInstanceNames(c.Request.Context(), &inst)
	c.JSON(http.StatusCreated, inst)
}

// GetInstance godoc
// @Summary     Get a stack instance
// @Description Get a stack instance by ID. values_drift is true when the running values come from a successful rollback and the stored overrides produce different values: the next deploy undoes the rollback. Only this endpoint computes values_drift, following (the caller follows the instance) and follower_count; list responses omit them. owner_username, definition_name and cluster_name are omitted when the owner, definition or cluster no longer exists; cluster_name is also omitted when cluster_id is empty (older instances).
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	h.enrichInstanceDetail(c, inst)

	c.JSON(http.StatusOK, inst)
}

// UpdateInstance godoc
// @Summary     Update a stack instance
// @Description Update a stack instance (branch, name, etc.). A changed name must be a DNS label (lowercase a-z, 0-9, '-', start and end alphanumeric, at most 50 characters); an unchanged name is not checked, so older instances with other names stay editable. A rename does not change the namespace.
// @Description ttl_minutes sets a new TTL from now (0 clears the expiry). expires_at (RFC 3339 with a time zone) sets a fixed stop time and ttl_minutes 0. Do not send both (400). The limits are the same as for create (MAX_INSTANCE_TTL_DAYS).
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id       path     string               true "Instance ID"
// @Param       instance body     models.StackInstance   true "Instance object"
// @Success     200      {object} models.StackInstance "The updated instance with the same computed fields as GET (values_drift, names, follow state)"
// @Failure     400      {object} map[string]string
// @Failure     401      {object} map[string]string
// @Failure     403      {object} map[string]string "Caller is not the owner, an admin or a devops user"
// @Failure     404      {object} map[string]string
// @Failure     409      {object} NamespaceConflictResponse "The new name is taken on the cluster (UNIQUE_INSTANCE_NAMES)"
// @Failure     500      {object} map[string]string
// @Router      /api/v1/stack-instances/{id} [put]
func (h *InstanceHandler) UpdateInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	existing, err := h.instanceRepo.FindByID(c.Request.Context(), id)
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
		ExpiresAt  *time.Time `json:"expires_at"`
		Name       *string    `json:"name"`
		Branch     *string    `json:"branch"`
		Namespace  *string    `json:"namespace"`
		TTLMinutes *int       `json:"ttl_minutes"`
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
		// A rename keeps the namespace, so only the name on the cluster counts.
		if h.uniqueNames && h.nameGuard().check(c, *update.Name, existing.ClusterID, existing.ID, "") {
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

	// Update the TTL or the stop time only when one was sent. ttl_minutes 0
	// clears the expiry; expires_at sets a fixed stop time (ttl_minutes 0).
	// A client that sends the whole instance back (GET, change, PUT) echoes
	// the stored expires_at, and often the stored ttl_minutes. An echoed
	// expires_at is no change; with an unchanged ttl_minutes, the expiry
	// stays as it is (a rename keeps a TTL expiry and a fixed stop time).
	reqTTL, reqStop := update.TTLMinutes, update.ExpiresAt
	if reqStop != nil && existing.ExpiresAt != nil && reqStop.Equal(*existing.ExpiresAt) {
		reqStop = nil
		if reqTTL != nil && *reqTTL == existing.TTLMinutes {
			reqTTL = nil
		}
	}
	if reqTTL != nil || reqStop != nil {
		ttl, expiresAt, ttlMsg := requestedExpiry(reqTTL, reqStop, 0, h.maxTTL(), existing.UpdatedAt)
		if ttlMsg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": ttlMsg})
			return
		}
		existing.TTLMinutes = ttl
		existing.ExpiresAt = expiresAt
	}

	if err := existing.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.instanceRepo.Update(writeCtx(c), existing); err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	h.enrichInstanceDetail(c, existing)
	c.JSON(http.StatusOK, existing)
}

// DeleteInstance godoc
// @Summary     Delete a stack instance
// @Description Deletes a stack instance. If the instance has running resources (status running/stopped/error), a cleanup is initiated first — helm releases are uninstalled and the namespace is deleted before the database record is removed. Returns 204 for immediate deletion (draft instances) or 202 when async cleanup is required: the clean started, and the row is removed when the clean finishes (WebSocket message instance.deleted). A failed clean keeps the instance with status error ("Clean failed; the stack was not deleted."). An operation in progress gives 409 before pre-instance-delete fires; of two concurrent deletes one gets 409. When quick deploy created the stack definition of the instance (owner_instance_id) and no other instance uses it, the definition and its charts are deleted with the instance.
// @Tags        stack-instances
// @Produce     json
// @Param       id  path     string true "Instance ID"
// @Success     202 {object} map[string]string "Cleanup initiated, instance will be deleted after resources are removed"
// @Success     204 "No Content — instance deleted immediately (no resources to clean)"
// @Failure     403 {object} map[string]string "Caller is not the owner, an admin or a devops user, or a pre-instance-delete hook rejected the request"
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string "Instance is in a transient state (deploying/stopping/cleaning), or a concurrent delete started first"
// @Failure     503 {object} map[string]string "Deploy manager not configured"
// @Router      /api/v1/stack-instances/{id} [delete]
func (h *InstanceHandler) DeleteInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInstanceIDRequired})
		return
	}

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
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

	logID, delErr := h.deleteInstance(c, inst)
	if delErr != nil {
		c.JSON(delErr.status, gin.H{"error": delErr.message})
		return
	}
	if logID != "" {
		c.JSON(http.StatusAccepted, gin.H{
			"log_id":  logID,
			"message": "Cleanup initiated; instance will be deleted after resources are removed",
		})
		return
	}
	c.Status(http.StatusNoContent)
}

// instanceDeleteError is a delete failure with its HTTP status and a safe,
// user-facing message.
type instanceDeleteError struct {
	message string
	status  int
}

func (e *instanceDeleteError) Error() string { return e.message }

// deleteInstance deletes one instance after the caller checked the
// authorization. The single delete and the bulk delete use it, so both apply
// the same rules:
//   - an operation in progress (deploying, stopping, cleaning, queued,
//     stabilizing) gives 409, before any hook fires;
//   - pre-instance-delete can reject the delete (failure_policy=fail);
//   - an instance with cluster resources (running, partial, stopped, error)
//     is cleaned first (Helm uninstall and namespace delete). The start of
//     the clean is a conditional update that also sets
//     stack_instances.delete_after_clean, so of two concurrent deletes one
//     wins and the other gets 409. The deploy manager deletes the row when
//     the clean completes; deleteInstance returns the log ID of the clean;
//   - any other instance (draft) is deleted at once, and post-instance-delete,
//     delete-completed and instance.deleted follow. deleteInstance then
//     returns an empty log ID.
func (h *InstanceHandler) deleteInstance(c *gin.Context, inst *models.StackInstance) (string, *instanceDeleteError) {
	ctx := hookTriggerCtx(c)

	needsClean := false
	switch inst.Status {
	case models.StackStatusDeploying, models.StackStatusStopping, models.StackStatusCleaning, models.StackStatusQueued, models.StackStatusStabilizing:
		// Checked before pre-instance-delete: no "pre" event without a delete.
		return "", &instanceDeleteError{status: http.StatusConflict, message: fmt.Sprintf("Cannot delete: instance is currently %s", inst.Status)}
	case models.StackStatusRunning, models.StackStatusPartial, models.StackStatusStopped, models.StackStatusError:
		needsClean = true
	default:
		// draft or unknown — no resources to clean, delete immediately
	}

	// Pre-instance-delete hook: a subscriber with failure_policy=fail can block
	// the delete (e.g. enforce dependency checks).
	if err := h.fireInstanceHook(ctx, hooks.EventPreInstanceDelete, inst); err != nil {
		slog.Error("pre-instance-delete hook failed", logKeyInstanceID, inst.ID, "error", err)
		return "", &instanceDeleteError{status: http.StatusForbidden, message: "pre-instance-delete hook rejected the request"}
	}

	// A client that leaves must not stop the delete or its side effects (#656).
	wctx := context.WithoutCancel(ctx)

	if needsClean {
		if h.deployManager == nil {
			return "", &instanceDeleteError{status: http.StatusServiceUnavailable, message: msgDeployerNotConfigured}
		}
		def, err := h.definitionRepo.FindByID(wctx, inst.StackDefinitionID)
		if err != nil {
			status, message := mapError(err, entityStackDefinition)
			return "", &instanceDeleteError{status: status, message: message}
		}
		charts, err := h.chartConfigRepo.ListByDefinition(wctx, def.ID)
		if err != nil {
			status, message := mapError(err, entityChartConfigs)
			return "", &instanceDeleteError{status: status, message: message}
		}

		logID, err := h.deployManager.CleanForDelete(wctx, inst, charts)
		if errors.Is(err, models.ErrDeleteConflict) {
			return "", &instanceDeleteError{status: http.StatusConflict, message: "Cannot delete: another operation started on the instance"}
		}
		if err != nil {
			slog.Error("Failed to start clean for delete",
				logKeyInstanceID, inst.ID, "error", err)
			return "", &instanceDeleteError{status: http.StatusInternalServerError, message: msgInternalServerError}
		}
		return logID, nil
	}

	if h.txRunner == nil {
		slog.Error("txRunner not configured for instance delete", logKeyInstanceID, inst.ID)
		return "", &instanceDeleteError{status: http.StatusInternalServerError, message: msgInternalServerError}
	}

	// The delete removes the follower rows: read the followers first, so
	// that they get the "instance.deleted" notification.
	followerIDs := h.followerIDsBeforeDelete(wctx, inst.ID)

	if txErr := database.DeleteInstanceWithOwnedDefinition(wctx, h.txRunner, inst); txErr != nil {
		status, message := mapError(txErr, entityStackInstance)
		if status == http.StatusInternalServerError {
			slog.Error("failed to delete instance", logKeyInstanceID, inst.ID, "error", txErr)
		}
		return "", &instanceDeleteError{status: status, message: message}
	}

	h.afterInstanceDeleted(wctx, inst, followerIDs)
	return "", nil
}

// followerIDsBeforeDelete returns the followers of the instance, read
// before the delete removes the follower rows. Without a notifier it
// returns nil.
func (h *InstanceHandler) followerIDsBeforeDelete(ctx context.Context, instanceID string) []string {
	if h.notifier == nil {
		return nil
	}
	return h.notifier.FollowerIDs(ctx, instanceID)
}

// afterInstanceDeleted fires post-instance-delete and delete-completed and
// sends instance.deleted to the owner and to followerIDs (read before the
// delete), after an instance row was deleted without a clean (single and
// bulk delete). delete-completed fires after every delete (also after a
// clean and for a cleanup policy delete), so a subscriber needs only this
// event.
func (h *InstanceHandler) afterInstanceDeleted(ctx context.Context, inst *models.StackInstance, followerIDs []string) {
	// Open stack lists remove the row (the delete after a clean sends the
	// same message from the deploy manager).
	h.deployManager.BroadcastInstanceDeleted(inst.ID)

	_ = h.fireInstanceHook(ctx, hooks.EventPostInstanceDelete, inst)
	_ = h.fireInstanceHook(ctx, hooks.EventDeleteCompleted, inst)

	if h.notifier != nil {
		target := models.NewNotificationTarget(inst)
		target.FollowerIDs = followerIDs
		if target.FollowerIDs == nil {
			// The rows are gone: no lookup after the delete.
			target.FollowerIDs = []string{}
		}
		_ = h.notifier.NotifyInstance(ctx, target, "instance.deleted", "Stack deleted",
			fmt.Sprintf("Stack %s has been deleted", inst.Name), notifyTriggerOpt(ctx))
	}
}

// notifyInstanceCreated sends instance.created to the owner and the channels
// with the trigger of ctx (hookTriggerCtx). Create, clone and quick deploy
// use it (#650). A nil notifier sends nothing.
func notifyInstanceCreated(ctx context.Context, n deployer.LifecycleNotifier, inst *models.StackInstance) {
	if n == nil || inst == nil {
		return
	}
	// A new instance has no followers yet.
	target := models.NewNotificationTarget(inst)
	target.FollowerIDs = []string{}
	// The instance is committed: a client that disconnects must not cancel
	// the notification insert. WithoutCancel keeps the trigger value.
	_ = n.NotifyInstance(context.WithoutCancel(ctx), target, "instance.created", "Stack created",
		fmt.Sprintf("Stack %s has been created", inst.Name), notifyTriggerOpt(ctx))
}

// notifyTriggerOpt is the notifier option with the trigger of ctx
// (hooks.WithTrigger, see hookTriggerCtx); none when ctx has no trigger.
func notifyTriggerOpt(ctx context.Context) notifier.InstanceOption {
	t, ok := hooks.TriggerFromContext(ctx)
	if !ok || t.Type == "" {
		return notifier.WithTrigger(nil)
	}
	return notifier.WithTrigger(&t)
}

type invokeActionRequest struct {
	Parameters map[string]any `json:"parameters,omitempty"`
}

// InvokeAction godoc
// @Summary     Invoke a registered action against a stack instance
// @Description Dispatches to the action subscriber webhook and wraps its response in an envelope containing action, instance_id, status_code, and result fields. The subscriber's JSON body is nested under the result key. Returns 200 even for non-2xx subscriber responses — check status_code to distinguish. For an answer with status 400 or higher the envelope also has message: the subscriber's reason from a JSON string body or from the message, error, detail or reason field (one line, at most 500 characters, URLs replaced by [url]). A text/plain body of such an answer becomes a JSON string result; another non-JSON body gives a null result. A non-JSON body with a status below 400 gives 502. When the action declares parameters, they are checked (type, required, enum options) before the call; undeclared parameters pass through. When the action has a log_path, the subscriber answered 2xx and the result has a valid job_id, the envelope also has job_id: poll GET /stack-instances/{id}/actions/{name}/jobs/{job_id}/log for the job log. Each call that reaches the subscriber (also an unreachable subscriber or a non-JSON body) stores one action run (GET /stack-instances/{id}/action-runs) with the caller, the declared parameters only (a sensitive parameter as [redacted]), the status code, the message and the job_id. The envelope has run_id when the run was stored; a failed write never fails the call.
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id      path     string                true "Instance ID"
// @Param       name    path     string                true "Action name"
// @Param       request body     invokeActionRequest   false "Optional parameters passed through to the subscriber"
// @Success     200 {object} map[string]any
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "Caller is not the owner, an admin or a devops user"
// @Failure     404 {object} map[string]string "Instance not found or action not registered"
// @Failure     500 {object} map[string]string
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
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

	// Check the declared parameter schema (when the action has one) before
	// the subscriber is called. An unknown action falls through to Invoke,
	// which answers 404.
	sub, known := h.actions.Lookup(name)
	if known {
		if paramErr := sub.ValidateParameters(req.Parameters); paramErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": paramErr.Error()})
			return
		}
	}

	startedAt := time.Now().UTC()
	res, invokeErr := h.actions.Invoke(c.Request.Context(), name, instanceRefFor(inst), req.Parameters)
	answeredAt := time.Now().UTC()
	run := newActionRun(c, inst.ID, name, sub, req.Parameters, startedAt)
	run.RequestID = res.RequestID
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
		// An answer that cannot be forwarded (for example a 302 with an HTML
		// body) keeps its status code on the run; a transport error has 0.
		run.StatusCode = res.StatusCode
		run.Message = actionRunFailureMessage(invokeErr)
		if res.StatusCode == 0 && c.Request.Context().Err() != nil {
			// The caller went away: the subscriber was not unreachable.
			run.Message = actionRunMsgClientClosed
		}
		run.FinishedAt = &answeredAt
		h.recordActionRun(c, run)
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
		run.StatusCode = res.StatusCode
		run.Message = actionRunMsgNonJSON
		run.FinishedAt = &answeredAt
		h.recordActionRun(c, run)
		c.JSON(http.StatusBadGateway, gin.H{
			"error":       "action subscriber returned a non-JSON body",
			"action":      name,
			"status_code": res.StatusCode,
		})
		return
	}

	envelope := gin.H{
		"action":      name,
		"instance_id": id,
		"status_code": res.StatusCode,
		"result":      json.RawMessage(result),
	}
	// For an asynchronous action with a job log, lift a valid result.job_id
	// of a 2xx answer to the envelope so clients can poll the job log route.
	jobID := asyncJobID(sub, res.StatusCode, result)
	if jobID != "" {
		envelope["job_id"] = jobID
	}
	// For a refusal (status >= 400, for example 409 "already in flight"), lift the
	// subscriber's message to the envelope so clients can show it.
	msg := hooks.ActionResultMessage(res.StatusCode, result)
	if msg != "" {
		envelope["message"] = msg
	}

	run.StatusCode = res.StatusCode
	// Every viewer of the instance reads the stored message.
	run.Message = sub.RedactSensitive(msg, req.Parameters)
	run.JobID = jobID
	if jobID == "" {
		// A job gets its finished_at from the job log proxy.
		run.FinishedAt = &answeredAt
	}
	if runID := h.recordActionRun(c, run); runID != "" {
		envelope["run_id"] = runID
	}
	c.JSON(http.StatusOK, envelope)
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
	// ExpiresAt is a fixed stop time of the clone (RFC 3339 with a time
	// zone). Do not send it with ttl_minutes. Default: a future fixed stop
	// time of the source.
	ExpiresAt *time.Time `json:"expires_at"`
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
// @Description It copies the TTL (unless ttl_minutes or expires_at is given; a future fixed stop time of the source is copied too), the value overrides and the branch overrides. It copies the instance quota override only when the caller owns the source or is admin or devops; for an owner without the admin or devops role only when the override stays within the cluster quota. When it is not copied for that reason (or the cluster quota lookup fails), the response has a warning and the clone uses the cluster quota.
// @Description The body is optional. Without a name, the server picks the first free name of <source>-copy, <source>-copy-2, ... (the namespace stack-<name>-<owner> must be free). A given name must be a DNS label (lowercase a-z, 0-9, '-', start and end alphanumeric, at most 50 characters).
// @Description The owner and the subscribed notification channels get the notification instance.created, with the caller as the trigger.
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
// @Failure     409  {object} NamespaceConflictResponse "Namespace already exists (given name), the name is taken on the cluster (UNIQUE_INSTANCE_NAMES), no free generated name, the per-user instance limit of the cluster is reached, or the cluster of the source instance no longer exists ({error})"
// @Failure     500  {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/clone [post]
func (h *InstanceHandler) CloneInstance(c *gin.Context) {
	id := c.Param("id")
	source, err := h.instanceRepo.FindByID(c.Request.Context(), id)
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

	// Without ttl_minutes and expires_at, the clone gets the TTL of the
	// source, or its fixed stop time when that is still in the future.
	reqTTL, reqStop := req.TTLMinutes, req.ExpiresAt
	if reqTTL == nil && reqStop == nil {
		if hasFixedStopTime(source.TTLMinutes, source.ExpiresAt) && source.ExpiresAt.After(now) {
			reqStop = source.ExpiresAt
		} else {
			srcTTL := source.TTLMinutes
			reqTTL = &srcTTL
		}
	}
	ttl, cloneExpiry, ttlMsg := requestedExpiry(reqTTL, reqStop, 0, h.maxTTL(), now)
	if ttlMsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": ttlMsg})
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
		if h.checkNamespaceUniqueness(c, buildNamespace(cloneName, ownerName), cloneName, source.ClusterID, ownerName) {
			return
		}
		if h.uniqueNames && h.nameGuard().check(c, cloneName, source.ClusterID, "", ownerName) {
			return
		}
	} else {
		// The source name can be older than the name rule; sanitize it first.
		base := sanitizeRFC1123Label(source.Name)
		for n := 1; n <= maxCloneNameAttempts; n++ {
			candidate := cloneNameCandidate(base, n)
			_, findErr := h.instanceRepo.FindByNamespace(c.Request.Context(), buildNamespace(candidate, ownerName))
			if findErr != nil && !errors.Is(findErr, dberrors.ErrNotFound) {
				if respondCancelled(c, findErr) {
					return
				}
				slog.Error("Failed to check namespace uniqueness for clone",
					logKeyInstanceID, id, "error", findErr)
				c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
				return
			}
			if findErr == nil {
				continue // the namespace is taken
			}
			if h.uniqueNames {
				holder, nameErr := h.nameGuard().nameHolder(c.Request.Context(), candidate, source.ClusterID, "")
				if nameErr != nil {
					if respondCancelled(c, nameErr) {
						return
					}
					slog.Error("Failed to check instance name uniqueness for clone",
						logKeyInstanceID, id, "error", nameErr)
					c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
					return
				}
				if holder != nil {
					continue // the name is taken on the cluster
				}
			}
			cloneName = candidate
			break
		}
		if cloneName == "" {
			code := "namespace already exists"
			if h.uniqueNames {
				// The cause can also be a name that is taken on the cluster.
				code = "no free clone name"
			}
			c.JSON(http.StatusConflict, NamespaceConflictResponse{
				Error:       code,
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
		ExpiresAt:         cloneExpiry,
		CreatedAt:         now,
		UpdatedAt:         now,
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

	limit, ok := instanceLimitFor(c, h.clusterRepo, clone.ClusterID, sourceClusterGone)
	if !ok {
		return
	}

	quotaPolicy := h.cloneQuotaPolicy(c, source)

	// Instance create + override copies are atomic.
	wctx := writeCtx(c)
	var warning string
	txErr := h.txRunner.RunInTx(wctx, func(repos database.TxRepos) error {
		if err := checkInstanceLimit(wctx, repos.StackInstance, limit, clone.OwnerID); err != nil {
			return err
		}
		var err error
		warning, err = cloneInstanceTx(wctx, repos, source.ID, clone, now, quotaPolicy)
		return err
	})
	if txErr != nil {
		if respondInstanceLimit(c, txErr, limit) {
			return
		}
		status, message := mapError(txErr, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	notifyInstanceCreated(wctx, h.notifier, clone)

	h.deployManager.BroadcastInstanceCreated(clone.ID)

	h.setInstanceNames(c.Request.Context(), clone)
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
	if err := repos.StackInstance.Create(ctx, clone); err != nil {
		return "", err
	}
	if repos.ValueOverride != nil {
		overrides, err := repos.ValueOverride.ListByInstance(ctx, sourceID)
		if err != nil {
			return "", err
		}
		for _, ov := range overrides {
			if err := repos.ValueOverride.Create(ctx, &models.ValueOverride{
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
		branches, err := repos.BranchOverride.List(ctx, sourceID)
		if err != nil {
			return "", err
		}
		for _, bo := range branches {
			if err := repos.BranchOverride.Set(ctx, &models.ChartBranchOverride{
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	chart, ok := requireInstanceChart(c, h.chartConfigRepo, inst, chartID)
	if !ok {
		return
	}

	def, err := h.definitionRepo.FindByID(c.Request.Context(), inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	valuesMap, _, err := h.buildChartValuesAndBranches(c.Request.Context(), inst, def, []models.ChartConfig{*chart})
	if err != nil {
		if respondCancelled(c, err) {
			return
		}
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), instanceID)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	def, err := h.definitionRepo.FindByID(c.Request.Context(), inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(c.Request.Context(), def.ID)
	if err != nil {
		status, message := mapError(err, entityChartConfigs)
		c.JSON(status, gin.H{"error": message})
		return
	}

	layers, templateVars, _, err := h.chartValueLayers(c.Request.Context(), inst, def, charts)
	if err != nil {
		if respondCancelled(c, err) {
			return
		}
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
// @Description Trigger Helm deployment for a stack instance. Allowed for draft, stopped, error, partial and running instances (running = redeploy, a Helm upgrade with the current values). With a TTL, the expiry becomes now + ttl_minutes, unless the current expiry is later (a redeploy never makes the expiry earlier). A fixed stop time (ttl_minutes 0 with expires_at) stays; when it has passed, the deploy gives 409 "The stop time has passed. Set a new stop time or a TTL."
// @Tags        stack-instances
// @Produce     json
// @Param       id path string true "Instance ID"
// @Success     202 {object} map[string]string "Deployment started"
// @Failure     400 {object} map[string]string
// @Failure     403 {object} map[string]string "Caller is not the owner, an admin or a devops user"
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string "Already deploying, or the fixed stop time has passed"
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
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

	// A fixed stop time stays on deploy. When it has passed, the TTL reaper
	// would stop the stack again at once, so refuse the deploy.
	if stopTimePassed(inst.TTLMinutes, inst.ExpiresAt, time.Now().UTC()) {
		c.JSON(http.StatusConflict, gin.H{"error": msgStopTimePassed})
		return
	}

	def, err := h.definitionRepo.FindByID(c.Request.Context(), inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(c.Request.Context(), def.ID)
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

	// The build runs before the first write, so a client that left gets 499
	// and nothing changes (#656).
	valuesMap, branchMap, err := h.buildChartValuesAndBranches(c.Request.Context(), inst, def, charts)
	if err != nil {
		if respondCancelled(c, err) {
			return
		}
		slog.Error("Failed to build chart values",
			logKeyInstanceID, id,
			"error", err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Restart the TTL clock on deploy, but never make the expiry earlier: a
	// redeploy of an instance that was extended keeps the later expiry.
	wctx := writeCtx(c)
	if inst.TTLMinutes > 0 {
		inst.ExpiresAt = deployExpiry(inst.ExpiresAt, inst.TTLMinutes, h.maxTTL(), time.Now().UTC())
		_ = h.instanceRepo.Update(wctx, inst)
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

	logID, err := h.deployManager.Deploy(wctx, req)
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
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

	def, err := h.definitionRepo.FindByID(c.Request.Context(), inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(c.Request.Context(), def.ID)
	if err != nil {
		status, message := mapError(err, entityChartConfigs)
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Build merged values for each chart.
	valuesMap, err := h.buildChartValues(c.Request.Context(), inst, def, charts)
	if err != nil {
		if respondCancelled(c, err) {
			return
		}
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
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
	def, err := h.definitionRepo.FindByID(c.Request.Context(), inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(c.Request.Context(), def.ID)
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

	logID, err := h.deployManager.StopWithCharts(writeCtx(c), inst, chartInfos)
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
// @Failure     409 {object} map[string]string "Invalid status for clean, or a concurrent clean or delete started first"
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
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

	// Manager.Clean starts with a conditional update (status still allows a
	// clean, no delete started): a concurrent clean or delete gives 409.
	switch inst.Status {
	case models.StackStatusRunning, models.StackStatusPartial, models.StackStatusStopped, models.StackStatusError:
		// OK
	default:
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Cannot clean: instance is currently %s", inst.Status)})
		return
	}

	def, err := h.definitionRepo.FindByID(c.Request.Context(), inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(c.Request.Context(), def.ID)
	if err != nil {
		status, message := mapError(err, entityChartConfigs)
		c.JSON(status, gin.H{"error": message})
		return
	}

	logID, err := h.deployManager.Clean(writeCtx(c), inst, charts)
	if errors.Is(err, models.ErrCleanConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": msgCleanConflict})
		return
	}
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
// @Description Get deployment log history for a stack instance, newest first. Supports cursor-based pagination for efficient large dataset traversal.
// @Description The default view (full) returns all fields of each log. The summary view returns the row columns only (no output, values_snapshot or chart_versions) and, without a cursor, highlights (running log, current deploy, latest failure; computed over all logs of the instance, the filters do not apply).
// @Description The offset path returns total and, when more rows follow, next_cursor. The cursor path returns total: -1.
// @Description When the highlights cannot be read, the response has no highlights (the list is still returned).
// @Tags        stack-instances
// @Produce     json
// @Security    BearerAuth
// @Param       id     path  string true  "Instance ID"
// @Param       limit  query int    false "Page size (default 50, maximum 100; a larger value gives 100)"
// @Param       offset query int    false "Offset for traditional pagination (default 0)"
// @Param       cursor query string false "Cursor from previous page for cursor-based pagination (overrides offset)"
// @Param       view   query string false "Response view (default full)" Enums(full, summary)
// @Param       action query string false "Comma list of actions: deploy, rollback, stop, clean"
// @Param       status query string false "Comma list of statuses: running, success, error, partial"
// @Success     200 {object} models.DeploymentLogResult
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Failure     503 {object} map[string]string
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

	// Verify instance exists. The queries use the stored ID (MySQL
	// compares IDs without case).
	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
	if err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}
	id = inst.ID

	filters := models.DeploymentLogFilters{
		InstanceID: id,
		Cursor:     c.Query("cursor"),
	}

	switch c.Query("view") {
	case "", deployLogViewFull:
	case deployLogViewSummary:
		filters.Summary = true
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidDeployLogView})
		return
	}

	actions, ok := parseDeployLogList(c.Query("action"), models.IsDeployLogAction)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidDeployLogAction})
		return
	}
	filters.Actions = actions

	statuses, ok := parseDeployLogList(c.Query("status"), models.IsDeployLogStatus)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidDeployLogStatus})
		return
	}
	filters.Statuses = statuses

	if limitStr := c.Query("limit"); limitStr != "" {
		l, err := strconv.Atoi(limitStr)
		if err != nil || l < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid limit parameter"})
			return
		}
		// A larger limit gives the maximum (no 400: older clients send more).
		filters.Limit = min(l, models.MaxDeployLogPageSize)
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

	// Highlights are for the first page of the summary view only. Without
	// them the client finds the logs in the list, so an error omits them.
	if reader, isReader := h.deployLogRepo.(models.DeploymentLogHighlightReader); isReader && filters.Summary && filters.Cursor == "" {
		highlights, hlErr := reader.HistoryHighlights(c.Request.Context(), id)
		if hlErr != nil {
			slog.Warn("Failed to read deploy history highlights", logKeyInstanceID, id, "error", hlErr)
		} else {
			result.Highlights = highlights
		}
	}

	rows := deployLogResultRows(result)
	if filters.Summary {
		for _, row := range rows {
			if row != nil {
				summaryDeployLog(row)
			}
		}
	}
	setDeployLogStartedBy(c.Request.Context(), h.userRepo, rows)

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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
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
		nsStatus, err := h.namespaceStatus(c.Request.Context(), client, inst.ClusterID, inst.Namespace, k8s.StatusOptions{})
		if err != nil {
			if respondCancelled(c, err) {
				return
			}
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
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

		nsStatus, nsErr := h.namespaceStatus(statusCtx, client, inst.ClusterID, inst.Namespace, k8s.StatusOptions{IncludeEvents: true})
		if nsErr != nil {
			if respondCancelled(c, nsErr) {
				return
			}
			if errors.Is(nsErr, context.DeadlineExceeded) || statusCtx.Err() == context.DeadlineExceeded {
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

// namespaceStatusTimeout bounds one shared namespace status read.
const namespaceStatusTimeout = 10 * time.Second

// namespaceStatus reads the status of a namespace. Concurrent reads of the same
// namespace share one read: each read costs 5 or 6 requests of the client-side
// limit of the cluster. Each caller still stops at its own deadline.
func (h *InstanceHandler) namespaceStatus(ctx context.Context, client *k8s.Client, clusterID, namespace string, opts k8s.StatusOptions) (*k8s.NamespaceStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ch := h.statusFlight.DoChan(namespaceStatusKey(clusterID, namespace, opts), func() (_ interface{}, err error) {
		// DoChan re-panics in a new goroutine when callers wait, which Gin
		// Recovery cannot catch: turn a panic into an error.
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("namespace status panic: %v", r)
			}
		}()
		// The first caller leaving must not fail the others.
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), namespaceStatusTimeout)
		defer cancel()
		return client.GetNamespaceStatus(readCtx, namespace, opts)
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.(*k8s.NamespaceStatus), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// namespaceStatusKey is the singleflight key of one namespace status read.
func namespaceStatusKey(clusterID, namespace string, opts k8s.StatusOptions) string {
	return fmt.Sprintf("%s/%s/%t", clusterID, namespace, opts.IncludeEvents)
}

// checkNamespaceUniqueness checks whether the given namespace is already in use.
// If it is, it returns true and writes a 409 response with suggestions.
// The caller should return immediately when this returns true. With
// UNIQUE_INSTANCE_NAMES the suggestions are only names that are free on
// clusterID and give a free namespace for owner.
func (h *InstanceHandler) checkNamespaceUniqueness(c *gin.Context, namespace, instanceName, clusterID, owner string) bool {
	existing, err := h.instanceRepo.FindByNamespace(c.Request.Context(), namespace)
	if err != nil {
		// Not found is the happy path — namespace is available.
		if errors.Is(err, dberrors.ErrNotFound) {
			return false
		}
		if respondCancelled(c, err) {
			return true
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
	if h.uniqueNames {
		suggestions = h.nameGuard().freeSuggestions(c.Request.Context(), instanceName, clusterID, owner)
	}
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

func resolveOwnerName(ctx context.Context, userRepo models.UserRepository, ownerID string) string {
	if userRepo == nil {
		return ownerID
	}
	user, err := userRepo.FindByID(ctx, ownerID)
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
	// Until sets the expiry to this time (RFC 3339 with a time zone). It must
	// be after the current expiry and not later than now plus the limit
	// (MAX_INSTANCE_TTL_DAYS). Do not send it with minutes or ttl_minutes.
	Until *time.Time `json:"until,omitempty" example:"2026-10-17T18:00:00+02:00"`
}

// msgExtendTTLDeprecated is logged and returned in a Warning header when a
// client sends the deprecated ttl_minutes body to the extend endpoint.
const msgExtendTTLDeprecated = `299 - "ttl_minutes on /extend is deprecated and resets the expiry to now + ttl_minutes; send {\"minutes\": N} to add N minutes"`

// ExtendTTL godoc
// @Summary     Extend instance TTL
// @Description Extend the expiry time of a stack instance. The extend never makes the expiry earlier and never changes ttl_minutes.
// @Description - {"minutes": N}: adds N minutes (N > 0) to the current expires_at, or to now when the instance already expired or has no expiry time. The new expiry is capped at now + MAX_INSTANCE_TTL_DAYS (default 30 days).
// @Description - {"until": "<RFC 3339>"}: sets expires_at to this time. It must be after the current expiry (or after now when the expiry has passed) and not later than now + MAX_INSTANCE_TTL_DAYS (400). It works for a TTL and for a fixed stop time. Do not send it with minutes or ttl_minutes (400).
// @Description - Empty body: adds the instance's ttl_minutes in the same way (400 when ttl_minutes is 0).
// @Description - Deprecated {"ttl_minutes": N} (without minutes): the old behaviour for older clients (stackctl 0.4.0 and earlier). It sets ttl_minutes to N and expires_at to now + N, which can make the expiry earlier. The response has a Warning header; the server logs a deprecation warning.
// @Description An instance without TTL and without expiry time has nothing to extend (400).
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Param       id  path     string          true  "Instance ID"
// @Param       body body    extendTTLRequest false "Minutes to add, the new expiry (until), or the deprecated ttl_minutes"
// @Success     200 {object} models.StackInstance "The instance with the new expires_at and the same computed fields as GET (values_drift, names, follow state)"
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
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

	if req.Until != nil && (req.Minutes != nil || req.TTLMinutes != nil) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Send either until, minutes or ttl_minutes"})
		return
	}

	switch {
	case req.Until != nil:
		if inst.TTLMinutes <= 0 && inst.ExpiresAt == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Instance has no expiry; nothing to extend"})
			return
		}
		until := req.Until.UTC()
		// An extend never makes the expiry earlier.
		earliest := now
		if inst.ExpiresAt != nil && inst.ExpiresAt.After(now) {
			earliest = *inst.ExpiresAt
		}
		if !until.After(earliest) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "until must be after the current expiry"})
			return
		}
		if limit := now.Add(time.Duration(h.maxTTL()) * time.Minute); until.After(limit) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("until must not be later than %s from now", daysText(h.maxTTL()))})
			return
		}
		inst.ExpiresAt = &until

	case req.Minutes != nil:
		if *req.Minutes <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "minutes must be greater than 0"})
			return
		}
		if inst.TTLMinutes <= 0 && inst.ExpiresAt == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Instance has no expiry; nothing to extend"})
			return
		}
		inst.ExpiresAt = extendExpiry(inst.ExpiresAt, *req.Minutes, h.maxTTL(), now)

	case req.TTLMinutes != nil && *req.TTLMinutes != 0:
		// Deprecated: old clients send ttl_minutes. Keep the old semantics.
		ttl := *req.TTLMinutes
		if ttl < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ttl_minutes must be greater than 0"})
			return
		}
		if ttl > h.maxTTL() {
			c.JSON(http.StatusBadRequest, gin.H{"error": ttlExceedsMessage(h.maxTTL())})
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
		inst.ExpiresAt = extendExpiry(inst.ExpiresAt, inst.TTLMinutes, h.maxTTL(), now)
	}
	inst.UpdatedAt = now

	if err := h.instanceRepo.Update(writeCtx(c), inst); err != nil {
		status, message := mapError(err, entityStackInstance)
		c.JSON(status, gin.H{"error": message})
		return
	}

	h.enrichInstanceDetail(c, inst)
	c.JSON(http.StatusOK, inst)
}

// deployExpiry returns the expiry after a deploy: now + ttl, capped at now +
// maxMinutes (MAX_INSTANCE_TTL_DAYS, for a TTL set before the limit was
// lowered), or the current expiry when it is later (a redeploy never shortens
// an extended expiry).
func deployExpiry(current *time.Time, ttlMinutes, maxMinutes int, now time.Time) *time.Time {
	if ttlMinutes > maxMinutes {
		ttlMinutes = maxMinutes
	}
	exp := now.Add(time.Duration(ttlMinutes) * time.Minute)
	if current != nil && current.After(exp) {
		exp = *current
	}
	return &exp
}

// extendExpiry returns the expiry after adding minutes to current (or to now
// when current is nil or in the past). The result is capped at now +
// maxMinutes (MAX_INSTANCE_TTL_DAYS), but it is never earlier than current.
func extendExpiry(current *time.Time, minutes, maxMinutes int, now time.Time) *time.Time {
	if minutes > maxMinutes {
		minutes = maxMinutes
	}
	base := now
	if current != nil && current.After(now) {
		base = *current
	}
	exp := base.Add(time.Duration(minutes) * time.Minute)
	if limit := now.Add(time.Duration(maxMinutes) * time.Minute); exp.After(limit) {
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
	leftInst, err := h.instanceRepo.FindByID(c.Request.Context(), leftID)
	if err != nil {
		status, message := mapError(err, "Left stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Fetch right instance.
	rightInst, err := h.instanceRepo.FindByID(c.Request.Context(), rightID)
	if err != nil {
		status, message := mapError(err, "Right stack instance")
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Fetch definitions.
	leftDef, err := h.definitionRepo.FindByID(c.Request.Context(), leftInst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		if status == http.StatusInternalServerError {
			slog.Error("compare: failed to fetch left definition", logKeyInstanceID, leftID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	rightDef, err := h.definitionRepo.FindByID(c.Request.Context(), rightInst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		if status == http.StatusInternalServerError {
			slog.Error("compare: failed to fetch right definition", logKeyInstanceID, rightID, "error", err)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	// Fetch chart configs for both definitions.
	leftCharts, err := h.chartConfigRepo.ListByDefinition(c.Request.Context(), leftDef.ID)
	if err != nil {
		if respondCancelled(c, err) {
			return
		}
		slog.Error("compare: failed to fetch left chart configs", "definition_id", leftDef.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	rightCharts, err := h.chartConfigRepo.ListByDefinition(c.Request.Context(), rightDef.ID)
	if err != nil {
		if respondCancelled(c, err) {
			return
		}
		slog.Error("compare: failed to fetch right chart configs", "definition_id", rightDef.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Generate merged values per chart with the deploy pipeline (shared
	// values, chart defaults, value overrides, locked values, branch
	// overrides), so compare shows what a deploy of each instance renders.
	leftValuesMap, err := h.buildChartValues(c.Request.Context(), leftInst, leftDef, leftCharts)
	if err != nil {
		if respondCancelled(c, err) {
			return
		}
		slog.Error("compare: failed to build left values", logKeyInstanceID, leftID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	rightValuesMap, err := h.buildChartValues(c.Request.Context(), rightInst, rightDef, rightCharts)
	if err != nil {
		if respondCancelled(c, err) {
			return
		}
		slog.Error("compare: failed to build right values", logKeyInstanceID, rightID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Resolve owner names for the summaries.
	leftOwner := resolveOwnerName(c.Request.Context(), h.userRepo, leftInst.OwnerID)
	rightOwner := resolveOwnerName(c.Request.Context(), h.userRepo, rightInst.OwnerID)

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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), id)
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

	def, err := h.definitionRepo.FindByID(c.Request.Context(), inst.StackDefinitionID)
	if err != nil {
		status, message := mapError(err, entityStackDefinition)
		c.JSON(status, gin.H{"error": message})
		return
	}

	charts, err := h.chartConfigRepo.ListByDefinition(c.Request.Context(), def.ID)
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
		// A client that left before the first write gets 499 and no rollback (#656).
		if buildErr != nil && respondCancelled(c, buildErr) {
			return
		}
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

	logID, err := h.deployManager.Rollback(writeCtx(c), deployer.RollbackRequest{
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

// GetDeployLogEntry godoc
// @Summary     Get one deployment log entry
// @Description Returns one deployment log of the instance with its output, error message and started_by. The values snapshot and the chart versions are not in the response (see the values endpoint).
// @Tags        stack-instances
// @Produce     json
// @Security    BearerAuth
// @Param       id    path     string true "Instance ID"
// @Param       logId path     string true "Deployment Log ID"
// @Success     200 {object} models.DeploymentLog
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Router      /api/v1/stack-instances/{id}/deploy-log/{logId} [get]
func (h *InstanceHandler) GetDeployLogEntry(c *gin.Context) {
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), instanceID)
	if err != nil {
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

	// Compare with the stored ID: MySQL finds the instance without case.
	if logEntry.StackInstanceID != inst.ID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment log not found for this instance"})
		return
	}

	// The values endpoint serves the values snapshot.
	logEntry.ValuesSnapshot = ""
	logEntry.ChartVersions = ""
	setDeployLogStartedBy(c.Request.Context(), h.userRepo, []*models.DeploymentLog{logEntry})

	c.JSON(http.StatusOK, logEntry)
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

	inst, err := h.instanceRepo.FindByID(c.Request.Context(), instanceID)
	if err != nil {
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

	// Compare with the stored ID: MySQL finds the instance without case.
	if logEntry.StackInstanceID != inst.ID {
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
