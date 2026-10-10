package deployer

import (
	"log/slog"

	"backend/internal/models"
	"backend/internal/websocket"
)

// deploymentStatusPayload is the WebSocket payload for deployment status updates.
type deploymentStatusPayload struct {
	InstanceID   string `json:"instance_id"`
	Status       string `json:"status"`
	LogID        string `json:"log_id"`
	ErrorMessage string `json:"error_message,omitempty"`
	// Action is the operation of the log: deploy, stop, clean or rollback.
	// Omitted when unknown (older clients ignore the field).
	Action string `json:"action,omitempty"`
}

// deploymentLogPayload is the WebSocket payload for real-time log streaming.
type deploymentLogPayload struct {
	InstanceID string `json:"instance_id"`
	LogID      string `json:"log_id"`
	Line       string `json:"line"`
}

// broadcastStatus sends a deployment status update via WebSocket.
func (m *Manager) broadcastStatus(instanceID, status, logID string) {
	m.broadcastStatusWithError(instanceID, status, logID, "")
}

// broadcastStatusWithError sends a deployment status update with an optional error message.
func (m *Manager) broadcastStatusWithError(instanceID, status, logID, errorMessage string) {
	if m.hub == nil {
		return
	}

	msg, err := websocket.NewMessage("deployment.status", deploymentStatusPayload{
		InstanceID:   instanceID,
		Status:       status,
		LogID:        logID,
		ErrorMessage: errorMessage,
		Action:       m.logAction(logID, status),
	})
	if err != nil {
		slog.Error("failed to create deployment status message", "error", err)
		return
	}

	data, err := msg.Bytes()
	if err != nil {
		slog.Error("failed to serialize deployment status message", "error", err)
		return
	}

	m.hub.Broadcast(data)
}

// broadcastLog sends a deployment log line via WebSocket for real-time log streaming.
// Uses targeted broadcast when the hub supports it, so only clients subscribed to
// the instance receive the high-volume log output.
func (m *Manager) broadcastLog(instanceID, logID, line string) {
	if m.hub == nil {
		return
	}
	// A panic in the hub must not stop the deploy goroutine or the process.
	// The log line is lost; the deploy log in the database still has it.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in deployment log broadcast", "instance_id", instanceID, "recover", r)
		}
	}()

	msg, err := websocket.NewMessage("deployment.log", deploymentLogPayload{
		InstanceID: instanceID,
		LogID:      logID,
		Line:       line,
	})
	if err != nil {
		slog.Error("failed to create deployment log message", "error", err)
		return
	}

	data, err := msg.Bytes()
	if err != nil {
		slog.Error("failed to serialize deployment log message", "error", err)
		return
	}

	if targeted, ok := m.hub.(websocket.TargetedSender); ok {
		targeted.BroadcastToInstance(instanceID, data)
	} else {
		m.hub.Broadcast(data)
	}
}

// notifyInstance sends the in-app notification of an instance event to the
// owner and the followers of the instance. Silently returns if no notifier
// is configured.
func (m *Manager) notifyInstance(target models.NotificationTarget, notifType, title, message string) {
	if m.notifier == nil {
		return
	}

	if err := m.notifier.NotifyInstance(m.shutdownCtx, target, notifType, title, message); err != nil {
		slog.Error("failed to create lifecycle notification",
			"instance_id", target.InstanceID, "type", notifType, "error", err)
	}
}

// followerIDs returns the followers of the instance, or nil without a
// notifier. Call it before the instance is deleted.
func (m *Manager) followerIDs(instanceID string) []string {
	if m.notifier == nil {
		return nil
	}
	return m.notifier.FollowerIDs(m.shutdownCtx, instanceID)
}

// logAction returns the action of logID for the status payload. A final
// status (not one of the in-progress statuses) removes the entry.
func (m *Manager) logAction(logID, status string) string {
	var v any
	var ok bool
	switch status {
	case models.StackStatusQueued, models.StackStatusDeploying, models.StackStatusStabilizing,
		models.StackStatusStopping, models.StackStatusCleaning:
		v, ok = m.logActions.Load(logID)
	default:
		v, ok = m.logActions.LoadAndDelete(logID)
	}
	if !ok {
		return ""
	}
	action, _ := v.(string)
	return action
}
