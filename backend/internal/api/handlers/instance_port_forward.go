package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/internal/podstream"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/util/validation"
)

// PortForward godoc
// @Summary     Open a port-forward tunnel to a service of a stack instance
// @Description Upgrades the connection to a WebSocket that carries raw TCP bytes between the client and a service port of the instance. Binary frames carry the bytes in both directions; a text frame closes the tunnel with 1003. There is no half-close. The server pings every 30 s; pings do not count as activity. Authentication: the Authorization header (JWT) or, when STACK_STREAM_ALLOW_API_KEYS is true, the X-API-Key header. The route never accepts a token in the query string. The caller must be the owner of the instance, a devops user or an admin, and the cluster must allow port-forward (allow_port_forward). All checks run before the upgrade and give an HTTP status with a stable code in the body ({"error","code"}). Close codes after the upgrade: 1000 normal, 1001 server shutdown, 1003 text frame, 1008 access revoked, 1009 frame larger than 64 KiB, 1011 internal error, 4001 maximum duration, 4002 idle timeout, 4502 pod stream error (the reason holds the error). An expired access token does not close an open tunnel. Delete, stop and clean of the instance close the tunnels of that replica at once and the tunnels of other replicas at the next revalidation (up to one minute).
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Security    ApiKeyAuth
// @Param       id      path  string true "Instance ID"
// @Param       service query string true "Service name (DNS-1035 label)"
// @Param       port    query int    true "Service port number (1-65535)"
// @Success     101 "Switching Protocols"
// @Failure     400 {object} map[string]string "invalid_service, invalid_port, upgrade_required"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "api_key_not_allowed, forbidden, cluster_port_forward_disabled, namespace_not_allowed, origin_not_allowed"
// @Failure     404 {object} map[string]string "feature_disabled, instance_not_found, service_not_found, port_not_found"
// @Failure     409 {object} map[string]string "instance_not_deployed, service_external_name, service_no_selector"
// @Failure     429 {object} map[string]string "tunnel_limit"
// @Failure     500 {object} map[string]string
// @Failure     502 {object} map[string]string "dial_failed"
// @Failure     503 {object} map[string]string "no_ready_pod, kube_timeout, shutting_down"
// @Router      /api/v1/stack-instances/{id}/port-forward [get]
func (h *StreamHandler) PortForward(c *gin.Context) {
	// The authentication time: before any check, so that a revoke which runs
	// during the setup is later than the issue time of an API-key identity.
	authTime := time.Now()
	setStreamKind(c, podstream.KindPortForward)

	// 1, 2: feature switch, API keys.
	if !h.checkFeature(c, h.cfg.PortForward.Enabled) {
		return
	}

	// 3: query parameters.
	service := c.Query("service")
	if service == "" || len(validation.IsDNS1035Label(service)) > 0 {
		streamError(c, http.StatusBadRequest, codeInvalidService, "service must be a DNS-1035 label")
		return
	}
	port64, err := strconv.ParseInt(c.Query("port"), 10, 32)
	if err != nil || port64 < 1 || port64 > 65535 {
		streamError(c, http.StatusBadRequest, codeInvalidPort, "port must be a number from 1 to 65535")
		return
	}

	port := int32(port64)

	// 4, 5, 6: instance, role, cluster switch.
	inst := h.loadInstance(c)
	if inst == nil {
		return
	}

	// 7: a live namespace exists.
	switch inst.Status {
	case models.StackStatusDraft, models.StackStatusStopped, models.StackStatusStopping, models.StackStatusCleaning:
		streamError(c, http.StatusConflict, codeInstanceNotDeployed, "The instance is not deployed")
		return
	}
	if inst.Namespace == "" {
		streamError(c, http.StatusConflict, codeInstanceNotDeployed, "The instance is not deployed")
		return
	}

	// 8: the namespace is a stack namespace of this instance only.
	if !h.checkNamespaceOwner(c, inst) {
		return
	}
	if h.provider == nil {
		streamError(c, http.StatusInternalServerError, codeInternal, "")
		return
	}
	pf, err := h.provider(inst.ClusterID)
	if err != nil || pf == nil {
		slog.Error("Port-forward: cluster client unavailable", "instance_id", inst.ID, "cluster_id", inst.ClusterID, "error", err)
		streamError(c, http.StatusInternalServerError, codeInternal, "")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), streamResolveTimeout)
	defer cancel()
	if err := pf.CheckStreamNamespace(ctx, inst.Namespace, h.cfg.BackendNamespace); err != nil {
		h.respondK8sError(c, err, "check namespace", false)
		return
	}

	// Not a WebSocket request, or a foreign origin: refuse before a slot and
	// a pod connection are used.
	if !gorilla.IsWebSocketUpgrade(c.Request) {
		streamError(c, http.StatusBadRequest, codeUpgradeRequired, "A WebSocket upgrade is required")
		return
	}
	if allowed, _ := h.originPolicy.Allows(c.Request, c.Request.Header.Get("Origin")); !allowed {
		streamError(c, http.StatusForbidden, codeOriginNotAllowed, "Origin not allowed")
		return
	}

	// 9: tunnel limit.
	userID := middleware.GetUserIDFromContext(c)
	slot, err := h.manager.Acquire(userID)
	if err != nil {
		if errors.Is(err, podstream.ErrLimitReached) {
			streamError(c, http.StatusTooManyRequests, codeTunnelLimit, "Too many open tunnels")
			return
		}
		streamError(c, http.StatusServiceUnavailable, codeShuttingDown, "The server is shutting down")
		return
	}
	handedOver := false
	defer func() {
		if !handedOver {
			slot.Release()
		}
	}()

	// 10: service, port and pod.
	target, err := pf.ResolveServiceTarget(ctx, inst.Namespace, service, port)
	if err != nil {
		h.respondK8sError(c, err, "resolve service", false)
		return
	}

	// 11: dial.
	stream, err := pf.DialPodPort(ctx, inst.Namespace, target.Pod, target.TargetPort)
	if err != nil {
		h.respondK8sError(c, err, "dial pod", true)
		return
	}

	// Upgrade. On failure gorilla has answered; free the pod stream.
	up := h.upgrader()
	conn, err := up.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		_ = stream.Close()
		slog.Debug("Port-forward: upgrade failed", "instance_id", inst.ID, "error", err)
		return
	}

	username := middleware.GetUsernameFromContext(c)
	auth := middleware.GetAuthMethodFromContext(c)
	h.writeAudit(userID, username, auditActionPortForward, inst.ID, map[string]any{
		"service": service, "port": port, "pod": target.Pod, "target_port": target.TargetPort, "auth": auth,
	})

	handedOver = true
	identity := streamIdentity(c, authTime)
	h.manager.StartPortForward(podstream.PortForwardOptions{
		Conn: conn, Stream: stream, Slot: slot, Hub: h.hub,
		Identity: identity, InstanceID: inst.ID,
		PostRegister: h.postRegisterCheck(identity, inst.ID, podstream.KindPortForward),
		OnEnd: func(s podstream.Summary) {
			h.writeAudit(userID, username, auditActionPortForwardEnd, inst.ID, map[string]any{
				"service": service, "port": port, "pod": target.Pod,
				"bytes_in": s.BytesIn, "bytes_out": s.BytesOut, "duration_ms": s.Duration.Milliseconds(),
				"close_code": s.CloseCode, "close_reason": s.CloseReason,
			})
		},
	})
}

// checkNamespaceOwner refuses a namespace that another instance also names.
// Rows from before the namespace became immutable can name the namespace of
// another stack, and the label check of CheckStreamNamespace cannot tell them
// apart. It writes the response and returns false when the check fails.
func (h *StreamHandler) checkNamespaceOwner(c *gin.Context, inst *models.StackInstance) bool {
	shared, err := h.instanceRepo.NamespaceShared(c.Request.Context(), inst.Namespace, inst.ID)
	if err != nil {
		if respondCancelled(c, err) {
			return false
		}
		slog.Error(streamLabel(c)+": namespace owner lookup failed", "instance_id", inst.ID, "error", err)
		streamError(c, http.StatusInternalServerError, codeInternal, "")
		return false
	}
	if shared {
		slog.Warn(streamLabel(c)+" denied: the namespace is shared with another instance",
			"instance_id", inst.ID, "namespace", inst.Namespace, "user_id", middleware.GetUserIDFromContext(c))
		streamError(c, http.StatusForbidden, codeNamespaceNotAllowed, "The namespace of the instance is not allowed")
		return false
	}
	return true
}

// respondK8sError maps an error of the stream layer to the response of the
// contract. dial is true for the dial to the pod: there a timeout is
// dial_failed (502); in the namespace, service and pod lookups it is
// kube_timeout (503). Anything unknown is a 500 with the generic message.
func (h *StreamHandler) respondK8sError(c *gin.Context, err error, op string, dial bool) {
	status, code, msg := mapStreamError(err, dial)
	if code == codeNoReadyPod && streamKind(c) == podstream.KindExec {
		msg = "No ready pod for the chart"
	}
	if code == codeInternal {
		if errors.Is(err, context.Canceled) {
			// The client left; nothing to answer.
			c.AbortWithStatus(499)
			return
		}
		slog.Error(streamLabel(c)+" failed", "op", op, "instance_id", c.Param("id"), "error", err)
	}
	streamError(c, status, code, msg)
}

// mapStreamError maps the sentinel errors of internal/k8s to status, code and
// message. A deadline maps to kube_timeout, or to dial_failed when dial is
// true (the timeout was in the dial to the pod).
func mapStreamError(err error, dial bool) (int, string, string) {
	switch {
	case errors.Is(err, k8s.ErrNamespaceNotFound):
		return http.StatusConflict, codeInstanceNotDeployed, "The instance is not deployed"
	case errors.Is(err, k8s.ErrNamespaceNotAllowed):
		return http.StatusForbidden, codeNamespaceNotAllowed, "The namespace of the instance is not allowed"
	case errors.Is(err, k8s.ErrServiceNotFound):
		return http.StatusNotFound, codeServiceNotFound, "Service not found"
	case errors.Is(err, k8s.ErrPortNotFound):
		return http.StatusNotFound, codePortNotFound, "The service has no such port"
	case errors.Is(err, k8s.ErrServiceExternalName):
		return http.StatusConflict, codeServiceExternalName, "The service is of type ExternalName"
	case errors.Is(err, k8s.ErrServiceNoSelector):
		return http.StatusConflict, codeServiceNoSelector, "The service has no selector"
	case errors.Is(err, k8s.ErrNoReadyPod):
		return http.StatusServiceUnavailable, codeNoReadyPod, "No ready pod for the service"
	case errors.Is(err, k8s.ErrContainerNotFound):
		return http.StatusNotFound, codeContainerNotFound, "No ready pod runs the container"
	case errors.Is(err, k8s.ErrContainerNotAllowed):
		return http.StatusForbidden, codeContainerNotAllowed, "The container is not allowed for exec"
	case errors.Is(err, k8s.ErrServiceAccountToken):
		return http.StatusForbidden, codeServiceAccountToken, "The container holds a service-account token"
	case errors.Is(err, k8s.ErrDialFailed):
		return http.StatusBadGateway, codeDialFailed, "The connection to the pod failed"
	case errors.Is(err, context.DeadlineExceeded):
		if dial {
			return http.StatusBadGateway, codeDialFailed, "The connection to the pod failed"
		}
		return http.StatusServiceUnavailable, codeKubeTimeout, "The cluster did not answer in time"
	default:
		return http.StatusInternalServerError, codeInternal, msgInternalServerError
	}
}
