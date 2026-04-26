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
	repo     models.NotificationRepository
	hub      *websocket.Hub
	userRepo models.UserRepository
}

// NewNotifier creates a new Notifier. hub and userRepo may be nil if real-time
// broadcasting or system-wide notifications are not needed.
func NewNotifier(repo models.NotificationRepository, hub *websocket.Hub, userRepo models.UserRepository) *Notifier {
	return &Notifier{
		repo:     repo,
		hub:      hub,
		userRepo: userRepo,
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

// NotifySystem creates a notification for all admin and devops users.
// Requires userRepo to have been provided at construction time.
func (n *Notifier) NotifySystem(ctx context.Context, notifType, title, message, entityType, entityID string) error {
	if n.userRepo == nil {
		slog.Warn("NotifySystem called without userRepo configured")
		return nil
	}

	admins, err := n.userRepo.ListByRoles([]string{"admin", "devops"})
	if err != nil {
		return err
	}

	for _, u := range admins {
		if err := n.Notify(ctx, u.ID, notifType, title, message, entityType, entityID); err != nil {
			slog.Error("failed to create system notification for user", "user_id", u.ID, "type", notifType, "error", err)
		}
	}
	return nil
}

// MessageTypeNotificationNew is the WebSocket message type for new notifications.
const MessageTypeNotificationNew = "notification.new"
