package deployer

import (
	"encoding/json"
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

	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("failed to marshal deployment status message", "error", err)
		return
	}

	m.hub.Broadcast(data)
}

// broadcastLog sends a deployment log line via WebSocket for real-time log streaming.
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

	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("failed to marshal deployment log message", "error", err)
		return
	}

	m.hub.Broadcast(data)
}
