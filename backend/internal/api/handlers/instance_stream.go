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
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gorilla "github.com/gorilla/websocket"
)

// Audit actions written by the stream handlers (the audit middleware ignores
// GET requests, so the handlers write their own entries).
const (
	auditActionPortForward    = middleware.AuditActionPortForward
	auditActionPortForwardEnd = middleware.AuditActionPortForwardEnd
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
	codeInternal              = "internal"
	codeUpgradeRequired       = "upgrade_required"
	codeOriginNotAllowed      = "origin_not_allowed"
	codeShuttingDown          = "shutting_down"
	streamResolveTimeout      = 20 * time.Second
	streamUpgraderBufferBytes = 4096
)

// PortForwarderProvider returns the cluster client for a cluster ID (empty:
// the default cluster). The registry satisfies it through a small adapter in
// bootstrap; tests inject a fake.
type PortForwarderProvider func(clusterID string) (k8s.PortForwarder, error)

// StreamDeps are the dependencies of the StreamHandler.
type StreamDeps struct {
	InstanceRepo models.StackInstanceRepository
	ClusterRepo  models.ClusterRepository
	// Provider resolves the cluster client of an instance.
	Provider PortForwarderProvider
	Manager  *podstream.Manager
	// Hub registers the sessions for revocation. May be nil in tests.
	Hub podstream.Registrar
	// Audit writes the start and end entries. May be nil (no entries).
	Audit middleware.AuditLogger
	// AllowedOrigins is CORS_ALLOWED_ORIGINS (the /ws origin rules).
	AllowedOrigins string
	Config         config.StreamConfig
}

// StreamHandler serves the stream routes of a stack instance: port-forward
// now, exec later. It holds the checks that both share (feature switch, auth
// method, instance access, cluster switch), the identity of a session, the
// upgrader with the origin check and the audit writes.
type StreamHandler struct {
	instanceRepo models.StackInstanceRepository
	clusterRepo  models.ClusterRepository
	provider     PortForwarderProvider
	manager      *podstream.Manager
	hub          podstream.Registrar
	audit        middleware.AuditLogger
	originPolicy middleware.OriginPolicy
	cfg          config.StreamConfig
}

// NewStreamHandler creates a StreamHandler.
func NewStreamHandler(d StreamDeps) *StreamHandler {
	return &StreamHandler{
		instanceRepo: d.InstanceRepo, clusterRepo: d.ClusterRepo, provider: d.Provider,
		manager: d.Manager, hub: d.Hub, audit: d.Audit,
		originPolicy: middleware.NewOriginPolicy(d.AllowedOrigins), cfg: d.Config,
	}
}

// streamError writes the error body of the stream routes.
func streamError(c *gin.Context, status int, code, message string) {
	if status == http.StatusInternalServerError {
		message = msgInternalServerError
	}
	c.JSON(status, gin.H{"error": message, "code": code})
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
// it, and its cluster allows the feature (clusterAllows). It writes the
// response and returns nil when a check fails.
func (h *StreamHandler) loadInstance(c *gin.Context, clusterAllows func(*models.Cluster) bool) *models.StackInstance {
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
	if cl == nil || !clusterAllows(cl) {
		streamError(c, http.StatusForbidden, codeClusterDisabled, "This cluster does not allow port-forward")
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

// identity builds the credential identity of the request for the hub. The
// access token of the caller may expire while the tunnel is open: the hub
// does not close a session for that.
func streamIdentity(c *gin.Context) websocket.ClientIdentity {
	id := websocket.ClientIdentity{UserID: middleware.GetUserIDFromContext(c)}
	if middleware.GetAuthMethodFromContext(c) == middleware.AuthMethodAPIKey {
		id.APIKeyID = middleware.GetAPIKeyIDFromContext(c)
		id.IssuedAt = time.Now()
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
	if h.audit == nil {
		return
	}
	raw, err := json.Marshal(details)
	if err != nil {
		slog.Error("Stream: audit details", "action", action, "error", err)
		return
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
	}
}
