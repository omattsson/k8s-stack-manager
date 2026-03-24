// Package notifier provides a service for creating user notifications
// and optionally broadcasting them via WebSocket.
package notifier

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"backend/internal/models"
	"backend/internal/websocket"

	"github.com/google/uuid"
)

// Notifier creates notification records and broadcasts them in real time.
type Notifier struct {
	repo models.NotificationRepository
	hub  *websocket.Hub
}

// NewNotifier creates a new Notifier. hub may be nil if real-time broadcasting
// is not needed.
func NewNotifier(repo models.NotificationRepository, hub *websocket.Hub) *Notifier {
	return &Notifier{
		repo: repo,
		hub:  hub,
	}
}

// Notify creates a notification for the given user and optionally broadcasts it
// via WebSocket. It returns an error only if the database insert fails.
func (n *Notifier) Notify(ctx context.Context, userID, notifType, title, message, entityType, entityID string) error {
	notification := &models.Notification{
		ID:         uuid.New().String(),
		UserID:     userID,
		Type:       notifType,
		Title:      title,
		Message:    message,
		EntityType: entityType,
		EntityID:   entityID,
		IsRead:     false,
		CreatedAt:  time.Now().UTC(),
	}

	if err := n.repo.Create(ctx, notification); err != nil {
		return err
	}

	// Broadcast via WebSocket if hub is available.
	if n.hub != nil {
		msg, err := websocket.NewMessage(MessageTypeNotificationNew, notification)
		if err != nil {
			slog.Error("Failed to create WebSocket notification message", "error", err)
			return nil // DB write succeeded; WS failure is non-fatal
		}
		data, err := json.Marshal(msg)
		if err != nil {
			slog.Error("Failed to marshal WebSocket notification message", "error", err)
			return nil
		}
		n.hub.Broadcast(data)
	}

	return nil
}

// MessageTypeNotificationNew is the WebSocket message type for new notifications.
const MessageTypeNotificationNew = "notification.new"
