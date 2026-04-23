package deployer

import (
	"log/slog"

	"backend/internal/websocket"
)

// deploymentStatusPayload is the WebSocket payload for deployment status updates.
type deploymentStatusPayload struct {
	InstanceID   string `json:"instance_id"`
	Status       string `json:"status"`
	LogID        string `json:"log_id"`
	ErrorMessage string `json:"error_message,omitempty"`
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

// notifyUser sends an in-app notification to a specific user.
// Silently returns if no notifier is configured.
func (m *Manager) notifyUser(ownerID, instanceID, notifType, title, message string) {
	if m.notifier == nil {
		return
	}

	if err := m.notifier.Notify(m.shutdownCtx, ownerID, notifType, title, message, "stack_instance", instanceID); err != nil {
		slog.Error("failed to create lifecycle notification",
			"instance_id", instanceID, "type", notifType, "error", err)
	}
}

