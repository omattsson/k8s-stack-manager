package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/config"
	"backend/internal/ctxutil"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/internal/podstream"
	"backend/internal/sessionstore"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gorilla "github.com/gorilla/websocket"
	"golang.org/x/sync/singleflight"
)

// Audit actions written by the stream handlers (the audit middleware ignores
// GET requests, so the handlers write their own entries).
const (
	auditActionPortForward    = middleware.AuditActionPortForward
	auditActionPortForwardEnd = middleware.AuditActionPortForwardEnd
	auditActionExec           = middleware.AuditActionExec
	auditActionExecEnd        = middleware.AuditActionExecEnd
)

// Stable error codes of the stream routes (body {"error","code"}).
const (
	codeFeatureDisabled       = "feature_disabled"
	codeAPIKeyNotAllowed      = "api_key_not_allowed" // #nosec G101 -- an error code, not a credential
	codeInvalidService        = "invalid_service"
	codeInvalidPort           = "invalid_port"
	codeInstanceNotFound      = "instance_not_found"
	codeForbidden             = "forbidden"
	codeClusterDisabled       = "cluster_port_forward_disabled"
	codeInstanceNotDeployed   = "instance_not_deployed"
	codeNamespaceNotAllowed   = "namespace_not_allowed"
	codeTunnelLimit           = "tunnel_limit"
	codeServiceNotFound       = "service_not_found"
	codePortNotFound          = "port_not_found"
	codeServiceExternalName   = "service_external_name"
	codeServiceNoSelector     = "service_no_selector"
	codeNoReadyPod            = "no_ready_pod"
	codeDialFailed            = "dial_failed"
	codeKubeTimeout           = "kube_timeout"
	codeInternal              = "internal"
	codeUpgradeRequired       = "upgrade_required"
	codeOriginNotAllowed      = "origin_not_allowed"
	codeShuttingDown          = "shutting_down"
	codeInvalidChart          = "invalid_chart"
	codeInvalidContainer      = "invalid_container"
	codeClusterExecDisabled   = "cluster_exec_disabled"
	codeChartNotFound         = "chart_not_found"
	codeContainerNotAllowed   = "container_not_allowed"
	codeContainerNotFound     = "container_not_found"
	codeServiceAccountToken   = "service_account_token" // #nosec G101 -- an error code, not a credential
	codeExecLimit             = "exec_limit"
	codeTokenCheckForbidden   = "token_check_forbidden" // #nosec G101 -- an error code, not a credential
	streamResolveTimeout      = 20 * time.Second
	streamUpgraderBufferBytes = 4096
)

// PortForwarderProvider returns the cluster client for a cluster ID (empty:
// the default cluster). The registry satisfies it through a small adapter in
// bootstrap; tests inject a fake.
type PortForwarderProvider func(clusterID string) (k8s.PortForwarder, error)

// ExecClientProvider returns the exec client for a cluster ID (empty: the
// default cluster).
type ExecClientProvider func(clusterID string) (k8s.ExecClient, error)

// StreamDeps are the dependencies of the StreamHandler.
type StreamDeps struct {
	InstanceRepo models.StackInstanceRepository
	ClusterRepo  models.ClusterRepository
	// Provider resolves the cluster client of an instance.
	Provider PortForwarderProvider
	Manager  *podstream.Manager
	// ExecManager holds the exec sessions (kind exec). ExecProvider resolves
	// the exec client of a cluster and ChartConfigRepo the charts of the
	// definition of an instance. They are needed for the exec route only.
	ExecManager     *podstream.Manager
	ExecProvider    ExecClientProvider
	ChartConfigRepo models.ChartConfigRepository
	// Hub registers the sessions for revocation. May be nil in tests.
	Hub podstream.Registrar
	// Audit writes the start and end entries. May be nil (no entries).
	Audit middleware.AuditLogger
	// AllowedOrigins is CORS_ALLOWED_ORIGINS (the /ws origin rules).
	AllowedOrigins string
	// SessionStore, UserRepo and APIKeyRepo feed the revocation re-check that
	// runs right after the session is registered with the hub (the same
	// checks as /ws). Each may be nil: its check is skipped.
	SessionStore sessionstore.SessionStore
	UserRepo     models.UserRepository
	APIKeyRepo   models.APIKeyRepository
	Config       config.StreamConfig
}

// StreamHandler serves the stream routes of a stack instance: port-forward
// and exec. It holds the checks that both share (feature switch, auth
// method, instance access, cluster switch), the identity of a session, the
// upgrader with the origin check and the audit writes.
type StreamHandler struct {
	instanceRepo models.StackInstanceRepository
	clusterRepo  models.ClusterRepository
	provider     PortForwarderProvider
	manager      *podstream.Manager
	execManager  *podstream.Manager
	execProvider ExecClientProvider
	chartRepo    models.ChartConfigRepository
	hub          podstream.Registrar
	audit        middleware.AuditLogger
	originPolicy middleware.OriginPolicy
	sessionStore sessionstore.SessionStore
	userRepo     models.UserRepository
	apiKeyRepo   models.APIKeyRepository
	// accessCheck is the access re-check of the hub (instance, current role,
	// cluster switch), also run once after the registration.
	accessCheck websocket.RevocableAccessChecker
	cfg         config.StreamConfig
	// hintFlight lets concurrent exec-hint reads of one instance share one
	// computation (sharedExecHints).
	hintFlight singleflight.Group
	// hintWait is how long a caller waits for the flight (0: execHintWait).
	// hintJoined, when set, is called by each caller once it has joined the
	// flight (tests).
	hintWait   time.Duration
	hintJoined func()
}

// NewStreamHandler creates a StreamHandler.
func NewStreamHandler(d StreamDeps) *StreamHandler {
	return &StreamHandler{
		instanceRepo: d.InstanceRepo, clusterRepo: d.ClusterRepo, provider: d.Provider,
		manager: d.Manager, hub: d.Hub, audit: d.Audit,
		execManager: d.ExecManager, execProvider: d.ExecProvider, chartRepo: d.ChartConfigRepo,
		originPolicy: middleware.NewOriginPolicy(d.AllowedOrigins), cfg: d.Config,
		sessionStore: d.SessionStore, userRepo: d.UserRepo, apiKeyRepo: d.APIKeyRepo,
		accessCheck: NewStreamAccessChecker(d.InstanceRepo, d.UserRepo, d.ClusterRepo, d.Config),
	}
}

// streamError writes the error body of the stream routes. It counts every
// refusal before the upgrade (status 400 to 499 and 501 to 599 except 500,
// which is a fault, not a refusal) in stack_stream.rejected_total with the
// code as the reason. The tunnel limit is the exception: the manager counts it
// with the finer reasons user_limit and process_cap.
func streamError(c *gin.Context, status int, code, message string) {
	if status == http.StatusInternalServerError {
		message = msgInternalServerError
	} else if status >= http.StatusBadRequest && code != codeTunnelLimit && code != codeExecLimit {
		podstream.RecordRejected(streamKind(c), code)
	}
	c.JSON(status, gin.H{"error": message, "code": code})
}

// streamKindKey is the gin context key that names the stream kind of the
// request (podstream.KindPortForward or podstream.KindExec). Each stream
// handler sets it first; streamError reads it for the metrics label only. A
// security decision never reads it: the kind is passed explicitly.
const streamKindKey = "stream_kind"

// setStreamKind records the kind of the request.
func setStreamKind(c *gin.Context, kind string) { c.Set(streamKindKey, kind) }

// streamKind returns the kind of the request; port-forward when none is set.
func streamKind(c *gin.Context) string {
	if k, ok := c.Get(streamKindKey); ok {
		if s, ok := k.(string); ok && s != "" {
			return s
		}
	}
	return podstream.KindPortForward
}

// streamLabel is the name of the feature in log lines.
func streamLabel(kind string) string {
	if kind == podstream.KindExec {
		return "Exec"
	}
	return "Port-forward"
}

// clusterAllowsStream reports whether the cluster switch of the kind is on. An
// unknown kind is never allowed.
func clusterAllowsStream(cl *models.Cluster, kind string) bool {
	switch kind {
	case podstream.KindExec:
		return cl.AllowExec
	case podstream.KindPortForward:
		return cl.AllowPortForward
	default:
		return false
	}
}

// checkFeature runs checks 1 and 2: the feature switch and the API-key
// switch. It writes the response and returns false when a check fails.
func (h *StreamHandler) checkFeature(c *gin.Context, enabled bool) bool {
	if !enabled {
		streamError(c, http.StatusNotFound, codeFeatureDisabled, "This feature is disabled")
		return false
	}
	if middleware.GetAuthMethodFromContext(c) == middleware.AuthMethodAPIKey && !h.cfg.AllowAPIKeys {
		streamError(c, http.StatusForbidden, codeAPIKeyNotAllowed, "API keys cannot open this stream")
		return false
	}
	return true
}

// loadInstance runs checks 4 to 6: the instance exists, the caller may modify
// it, and its cluster allows the feature of the request kind (allow_port_forward
// or allow_exec). It writes the response and returns nil when a check fails.
//
// kind is passed explicitly, never read from the context: the cluster switch is
// a security check. An unknown kind refuses the request.
func (h *StreamHandler) loadInstance(c *gin.Context, kind string) *models.StackInstance {
	if kind != podstream.KindPortForward && kind != podstream.KindExec {
		slog.Error("Stream: unknown kind", "kind", kind)
		streamError(c, http.StatusInternalServerError, codeInternal, "")
		return nil
	}
	ctx := c.Request.Context()
	inst, err := h.instanceRepo.FindByID(ctx, c.Param("id"))
	if err != nil {
		if respondCancelled(c, err) {
			return nil
		}
		status, message := mapError(err, entityStackInstance)
		if status == http.StatusNotFound {
			streamError(c, status, codeInstanceNotFound, message)
			return nil
		}
		slog.Error("Stream: instance lookup failed", "instance_id", c.Param("id"), "error", err)
		streamError(c, http.StatusInternalServerError, codeInternal, "")
		return nil
	}
	if !canModifyInstance(c, inst) {
		slog.Warn("Stream denied",
			"user_id", middleware.GetUserIDFromContext(c), "role", middleware.GetRoleFromContext(c), "instance_id", inst.ID)
		streamError(c, http.StatusForbidden, codeForbidden, msgInstanceModifyForbidden)
		return nil
	}
	cl, err := h.findCluster(ctx, inst.ClusterID)
	if err != nil {
		if respondCancelled(c, err) {
			return nil
		}
		if status, _ := mapError(err, entityCluster); status != http.StatusNotFound {
			slog.Error("Stream: cluster lookup failed", "cluster_id", inst.ClusterID, "error", err)
			streamError(c, http.StatusInternalServerError, codeInternal, "")
			return nil
		}
		// No such cluster: nothing allows the stream.
		cl = nil
	}
	if cl == nil || !clusterAllowsStream(cl, kind) {
		if kind == podstream.KindExec {
			streamError(c, http.StatusForbidden, codeClusterExecDisabled, "This cluster does not allow exec")
		} else {
			streamError(c, http.StatusForbidden, codeClusterDisabled, "This cluster does not allow port-forward")
		}
		return nil
	}
	return inst
}

func (h *StreamHandler) findCluster(ctx context.Context, clusterID string) (*models.Cluster, error) {
	if clusterID == "" {
		return h.clusterRepo.FindDefault(ctx)
	}
	return h.clusterRepo.FindByID(ctx, clusterID)
}

// upgrader builds the gorilla upgrader with the origin rules of /ws. A client
// without an Origin header (stackctl, curl) passes.
func (h *StreamHandler) upgrader() gorilla.Upgrader {
	return gorilla.Upgrader{
		ReadBufferSize:  streamUpgraderBufferBytes,
		WriteBufferSize: streamUpgraderBufferBytes,
		CheckOrigin: func(r *http.Request) bool {
			allowed, _ := h.originPolicy.Allows(r, r.Header.Get("Origin"))
			return allowed
		},
	}
}

// streamIdentity builds the credential identity of the request for the hub.
// The access token of the caller may expire while the tunnel is open: the hub
// does not close a session for that. authTime is the time at the start of the
// handler, before any check: an API-key identity uses it as the issue time
// (like /ws), so a user revoke that happens during the tunnel setup counts.
func streamIdentity(c *gin.Context, authTime time.Time) websocket.ClientIdentity {
	id := websocket.ClientIdentity{UserID: middleware.GetUserIDFromContext(c)}
	if middleware.GetAuthMethodFromContext(c) == middleware.AuthMethodAPIKey {
		id.APIKeyID = middleware.GetAPIKeyIDFromContext(c)
		id.IssuedAt = authTime
		if exp, ok := middleware.GetAPIKeyExpiryFromContext(c); ok {
			id.ExpiresAt = exp
		}
		return id
	}
	id.TokenID = middleware.GetJTIFromContext(c)
	id.SessionID = middleware.GetSessionIDFromContext(c)
	if iat, ok := middleware.GetTokenIssuedAtFromContext(c); ok {
		id.IssuedAt = iat
	}
	if exp, ok := middleware.GetTokenExpiryFromContext(c); ok {
		id.ExpiresAt = exp
	}
	return id
}

// writeAudit writes an audit entry with a context that is not tied to the
// request. A failure is logged; it does not stop the tunnel.
func (h *StreamHandler) writeAudit(userID, username, action, instanceID string, details map[string]any) {
	_ = h.writeAuditErr(userID, username, action, instanceID, details)
}

// writeAuditErr is writeAudit that returns the error. Exec fails closed on it.
func (h *StreamHandler) writeAuditErr(userID, username, action, instanceID string, details map[string]any) error {
	if h.audit == nil {
		return nil
	}
	raw, err := json.Marshal(details)
	if err != nil {
		slog.Error("Stream: audit details", "action", action, "error", err)
		return err
	}
	ctx, cancel := ctxutil.Detach(context.Background(), ctxutil.WriteTimeout)
	defer cancel()
	entry := &models.AuditLog{
		ID: uuid.New().String(), UserID: userID, Username: username,
		Action: action, EntityType: "stack_instance", EntityID: instanceID,
		Details: string(raw), Timestamp: time.Now(),
	}
	if err := h.audit.Create(ctx, entry); err != nil {
		slog.Error("Stream: failed to write audit entry", "action", action, "instance_id", instanceID, "error", err)
		return err
	}
	return nil
}

// streamProbe is the Revocable view of a session that is not registered yet:
// the access checker only reads its identity, instance and kind.
type streamProbe struct {
	identity websocket.ClientIdentity
	instance string
	kind     string
}

func (p *streamProbe) Identity() websocket.ClientIdentity { return p.identity }
func (p *streamProbe) InstanceID() string                 { return p.instance }
func (p *streamProbe) Kind() string {
	if p.kind == "" {
		return podstream.KindPortForward
	}
	return p.kind
}
func (p *streamProbe) Revoke(int, string) {}

// streamRecheckTimeout bounds the re-check after the registration.
const streamRecheckTimeout = 5 * time.Second

// postRegisterCheck returns the check that runs once right after the session
// is registered with the hub (podstream.PortForwardOptions.PostRegister, podstream.ExecOptions.PostRegister). A
// logout, a user revoke, a role change or a delete of the instance can run
// between the authentication and the registration; its disconnect then
// missed the session. The check applies what /ws applies after its
// registration (the token, session and user blocklists, or for an API key the
// key and user checks) and the access check of the hub revalidation. It
// returns true when the session must close. Lookup errors fail open (logged by
// the helpers); the revalidation checks again.
func (h *StreamHandler) postRegisterCheck(id websocket.ClientIdentity, instanceID, kind string) func(context.Context) bool {
	return func(ctx context.Context) bool {
		ctx, cancel := context.WithTimeout(ctx, streamRecheckTimeout)
		defer cancel()
		if id.APIKeyID != "" {
			if apiKeyIdentityRevoked(ctx, h.sessionStore, h.userRepo, h.apiKeyRepo, id) {
				return true
			}
		} else {
			if revoked, lookupErr := tokenRevocationStatus(ctx, h.sessionStore, id); revoked != nil {
				return true
			} else if lookupErr != nil {
				logRecheckError(ctx, "Stream token re-check failed", id.UserID, lookupErr)
			}
		}
		if h.accessCheck == nil {
			return false
		}
		lost := h.accessCheck(ctx, []websocket.Revocable{&streamProbe{identity: id, instance: instanceID, kind: kind}})
		return len(lost) == 1 && lost[0]
	}
}
