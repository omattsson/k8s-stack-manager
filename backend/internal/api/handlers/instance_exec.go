package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"backend/internal/api/middleware"
	"backend/internal/k8s"
	"backend/internal/podstream"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// maxExecChartNameLength is the longest Helm release name.
	maxExecChartNameLength = 53
	// maxAuditCommandLength cuts the command of the exec audit entry.
	maxAuditCommandLength = 1000
	// auditTruncatedMarker ends a cut command.
	auditTruncatedMarker = " …[truncated]"
)

// Exec godoc
// @Summary     Run a command in a container of a stack instance
// @Description Upgrades the connection to a WebSocket that runs one command in a container of the chart (Helm release) of the instance and streams its output. The command runs without stdin and without a TTY. Authentication: the Authorization header (JWT) or, when STACK_STREAM_ALLOW_API_KEYS is true, the X-API-Key header; the route never accepts a token in the query string. The caller must be the owner of the instance, a devops user or an admin, exec must be on (STACK_EXEC_ENABLED) and the cluster must allow it (allow_exec). Only containers in STACK_EXEC_ALLOWED_CONTAINERS can be used, and a container that holds a service-account token is refused unless STACK_EXEC_ALLOW_SERVICE_ACCOUNT_TOKEN is true. The pod is always chosen by the server: the first Running and Ready pod of the release, by name. All checks run before the upgrade and give an HTTP status with a stable code in the body ({"error","code"}). Protocol after the upgrade: the client sends one text frame within 10 s, {"type":"start","command":["bin/console","cache:clear"]} (1 to 256 arguments, the first not empty, no NUL byte, at most STACK_EXEC_MAX_COMMAND_LENGTH bytes in total; the server never uses a shell). The server answers with the text frame {"type":"started","pod":"...","container":"..."}, then sends the output as binary frames: the first byte is the stream (1 stdout, 2 stderr), the rest is raw output, at most 32 KiB per frame. The command ends with the text frame {"type":"exit","exit_code":N} and the close code 1000. A Kubernetes stream error ends with {"type":"error","message":"..."} and 4502; an internal error with {"type":"error","message":"Internal server error"} and 1011. Later client frames are ignored. The server pings every 30 s. Close codes: 1000 the command ended, 1001 server shutdown, 1008 access revoked, 1009 client frame larger than 64 KiB, 1011 internal error (also when the audit entry cannot be written; the command does not run then), 4001 maximum duration (no exit frame), 4400 invalid start message, 4408 no start message in 10 s, 4502 Kubernetes stream error. The remote process can keep running after the stream closes. Exec shows the container environment and its secrets: turn it on only where the stack owners may see them. The start and the end of each command are audited (exec, exec_end).
// @Tags        stack-instances
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Security    ApiKeyAuth
// @Param       id        path  string true  "Instance ID"
// @Param       chart     query string true  "Chart name (the Helm release name, DNS-1123 subdomain, at most 53 characters)"
// @Param       container query string false "Container name (DNS-1123 label). Omitted: the first allowed container of the first pod that has one"
// @Success     101 "Switching Protocols"
// @Failure     400 {object} map[string]string "invalid_chart, invalid_container, upgrade_required"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "api_key_not_allowed, forbidden, cluster_exec_disabled, namespace_not_allowed, container_not_allowed, service_account_token, origin_not_allowed"
// @Failure     404 {object} map[string]string "feature_disabled, instance_not_found, chart_not_found, container_not_found"
// @Failure     409 {object} map[string]string "instance_not_deployed"
// @Failure     429 {object} map[string]string "exec_limit"
// @Failure     500 {object} map[string]string
// @Failure     503 {object} map[string]string "no_ready_pod, kube_timeout, shutting_down"
// @Router      /api/v1/stack-instances/{id}/exec [get]
func (h *StreamHandler) Exec(c *gin.Context) {
	// The authentication time: before any check (see PortForward).
	authTime := time.Now()
	setStreamKind(c, podstream.KindExec)

	// 1, 2: feature switch, API keys.
	if !h.checkFeature(c, h.cfg.Exec.Enabled) {
		return
	}

	// 3: query parameters.
	chart := c.Query("chart")
	if chart == "" || len(chart) > maxExecChartNameLength || len(validation.IsDNS1123Subdomain(chart)) > 0 {
		streamError(c, http.StatusBadRequest, codeInvalidChart, "chart must be a Helm release name (a DNS-1123 name of at most 53 characters)")
		return
	}
	container, hasContainer := c.GetQuery("container")
	if hasContainer && (container == "" || len(validation.IsDNS1123Label(container)) > 0) {
		streamError(c, http.StatusBadRequest, codeInvalidContainer, "container must be a DNS-1123 label")
		return
	}

	// 4, 5, 6: instance, role, cluster switch.
	inst := h.loadInstance(c, podstream.KindExec)
	if inst == nil {
		return
	}

	// 7: a live namespace exists.
	if !streamInstanceLive(inst) {
		streamError(c, http.StatusConflict, codeInstanceNotDeployed, "The instance is not deployed")
		return
	}

	// 8: the namespace is a stack namespace of this instance only.
	if !h.checkNamespaceOwner(c, podstream.KindExec, inst) {
		return
	}
	if h.execProvider == nil || h.execManager == nil || h.chartRepo == nil {
		streamError(c, http.StatusInternalServerError, codeInternal, "")
		return
	}
	ec, err := h.execProvider(inst.ClusterID)
	if err != nil || ec == nil {
		slog.Error("Exec: cluster client unavailable", "instance_id", inst.ID, "cluster_id", inst.ClusterID, "error", err)
		streamError(c, http.StatusInternalServerError, codeInternal, "")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), streamResolveTimeout)
	defer cancel()
	if err := ec.CheckStreamNamespace(ctx, inst.Namespace, h.cfg.BackendNamespace); err != nil {
		h.respondK8sError(c, podstream.KindExec, err, "check namespace", false)
		return
	}

	// 9: the chart is a chart of the definition of the instance.
	charts, err := h.chartRepo.ListByDefinition(c.Request.Context(), inst.StackDefinitionID)
	if err != nil {
		if respondCancelled(c, err) {
			return
		}
		slog.Error("Exec: chart lookup failed", "instance_id", inst.ID, "error", err)
		streamError(c, http.StatusInternalServerError, codeInternal, "")
		return
	}
	found := false
	for i := range charts {
		if charts[i].ChartName == chart {
			found = true
			break
		}
	}
	if !found {
		streamError(c, http.StatusNotFound, codeChartNotFound, "The instance has no such chart")
		return
	}

	// 10: the container is allowed. An empty list allows nothing.
	if len(h.cfg.Exec.AllowedContainers) == 0 || (hasContainer && !h.cfg.Exec.ContainerAllowed(container)) {
		slog.Warn("Exec denied: container not allowed",
			"user_id", middleware.GetUserIDFromContext(c), "instance_id", inst.ID, "container", container)
		streamError(c, http.StatusForbidden, codeContainerNotAllowed, "The container is not allowed for exec")
		return
	}

	// 11: not a WebSocket request, or a foreign origin.
	if !gorilla.IsWebSocketUpgrade(c.Request) {
		streamError(c, http.StatusBadRequest, codeUpgradeRequired, "A WebSocket upgrade is required")
		return
	}
	if allowed, _ := h.originPolicy.Allows(c.Request, c.Request.Header.Get("Origin")); !allowed {
		streamError(c, http.StatusForbidden, codeOriginNotAllowed, "Origin not allowed")
		return
	}

	// 12: session limit.
	userID := middleware.GetUserIDFromContext(c)
	slot, err := h.execManager.Acquire(userID)
	if err != nil {
		if errors.Is(err, podstream.ErrLimitReached) {
			streamError(c, http.StatusTooManyRequests, codeExecLimit, "Too many open exec sessions")
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

	// 13: pod and container, from the pods of the release in the namespace.
	target, err := ec.ResolveExecTarget(ctx, inst.Namespace, k8s.ExecTargetRequest{
		Release: chart, Container: container,
		AllowedContainer:         h.cfg.Exec.ContainerAllowed,
		AllowServiceAccountToken: h.cfg.Exec.AllowServiceAccountToken,
	})
	if err != nil {
		h.respondK8sError(c, podstream.KindExec, err, "resolve exec target", false)
		return
	}

	// Upgrade. Gorilla has answered on failure.
	up := h.upgrader()
	conn, err := up.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Debug("Exec: upgrade failed", "instance_id", inst.ID, "error", err)
		return
	}

	username := middleware.GetUsernameFromContext(c)
	auth := middleware.GetAuthMethodFromContext(c)
	namespace := inst.Namespace
	handedOver = true
	identity := streamIdentity(c, authTime)
	h.execManager.StartExec(podstream.ExecOptions{
		Conn: conn, Slot: slot, Hub: h.hub,
		Identity: identity, InstanceID: inst.ID,
		Pod: target.Pod, Container: target.Container,
		MaxCommandLength: h.cfg.Exec.MaxCommandLength,
		PostRegister:     h.postRegisterCheck(identity, inst.ID, podstream.KindExec),
		// The start entry is written before the command and fails closed.
		BeforeStart: func(_ context.Context, command []string) error {
			if h.audit == nil {
				// Fail closed: no audit logger, no command.
				slog.Error("Exec: no audit logger configured; the command is refused", "instance_id", inst.ID)
				return podstream.ErrExecAuditUnavailable
			}
			err := h.writeAuditErr(userID, username, auditActionExec, inst.ID, map[string]any{
				"chart": chart, "container": target.Container, "pod": target.Pod,
				"command": auditCommand(command), "auth": auth,
			})
			if err != nil {
				return podstream.ErrExecAuditUnavailable
			}
			return nil
		},
		Run: func(ctx context.Context, command []string, stdout, stderr io.Writer) (int, error) {
			code, err := ec.ExecCommand(ctx, namespace, *target, command, stdout, stderr)
			if errors.Is(err, k8s.ErrRESTConfigUnavailable) {
				err = fmt.Errorf("%w: %v", podstream.ErrExecInternal, err)
			}
			if err != nil {
				logExecFailure(ctx, inst.ID, target.Pod, err)
			}
			return code, err
		},
		OnEnd: func(s podstream.ExecSummary) {
			if !s.Audited {
				// No start entry was written (bad start message, timeout,
				// revoked before the audit): log only.
				slog.Info("Exec session ended without a command",
					"instance_id", inst.ID, "close_code", s.CloseCode, "close_reason", s.CloseReason)
				return
			}
			var exit any // null when the process did not report an exit code
			if s.ExitCode != nil {
				exit = *s.ExitCode
			}
			h.writeAudit(userID, username, auditActionExecEnd, inst.ID, map[string]any{
				"chart": chart, "container": target.Container, "pod": target.Pod,
				"exit_code": exit, "result": s.Result, "duration_ms": s.Duration.Milliseconds(),
				"bytes_stdout": s.BytesStdout, "bytes_stderr": s.BytesStderr,
				"close_code": s.CloseCode, "close_reason": s.CloseReason,
			})
		},
	})
}

// logExecFailure logs a failure of the exec stream. The error comes from the
// k8s layer already stripped of the exec URL (it carries every argument of the
// command); only its Detail is logged. A session that ended on purpose (revoke,
// maximum duration, shutdown) is no failure: debug level.
func logExecFailure(ctx context.Context, instanceID, pod string, err error) {
	// A raw error would hold the exec URL: sanitize it again (idempotent).
	detail := err.Error()
	var se *k8s.ExecStreamError
	if !errors.As(err, &se) {
		_ = errors.As(k8s.SanitizeExecError(err), &se)
	}
	if se != nil {
		detail = se.Detail
	}
	level := slog.LevelWarn
	if ctx.Err() != nil {
		level = slog.LevelDebug
	}
	slog.Log(ctx, level, "Exec: stream failed", "instance_id", instanceID, "pod", pod, "error", detail)
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellQuote quotes one argument the way a POSIX shell reads it back.
func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	if shellSafe.MatchString(arg) {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// auditCommand renders the command for the audit entry: shell-quoted, cut at
// maxAuditCommandLength characters with a marker. The arguments are not
// redacted.
func auditCommand(command []string) string {
	quoted := make([]string, len(command))
	for i, a := range command {
		quoted[i] = shellQuote(strings.ToValidUTF8(a, "?"))
	}
	s := strings.Join(quoted, " ")
	if utf8.RuneCountInString(s) <= maxAuditCommandLength {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxAuditCommandLength]) + auditTruncatedMarker
}
