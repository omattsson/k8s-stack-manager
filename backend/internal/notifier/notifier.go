// Package notifier provides a service for creating user notifications
// and optionally broadcasting them via WebSocket and external channels.
package notifier

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"backend/internal/models"
	"backend/internal/notifier/channel"
	"backend/internal/websocket"

	"github.com/google/uuid"
)

const dispatchQueueSize = 100

// Notifier creates notification records and broadcasts them in real time.
type Notifier struct {
	repo              models.NotificationRepository
	hub               *websocket.Hub
	userRepo          models.UserRepository
	channelDispatcher *channel.Dispatcher
	dispatchQueue     chan dispatchWork
	dispatchOnce      sync.Once
}

type dispatchWork struct {
	payload channel.EventPayload
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

// WithChannelDispatcher sets the external channel dispatcher for webhook delivery
// and starts a bounded worker pool for processing dispatches. Safe to call once;
// subsequent calls are no-ops.
func (n *Notifier) WithChannelDispatcher(d *channel.Dispatcher) *Notifier {
	n.dispatchOnce.Do(func() {
		n.channelDispatcher = d
		n.dispatchQueue = make(chan dispatchWork, dispatchQueueSize)
		go n.dispatchWorker()
	})
	return n
}

func (n *Notifier) dispatchWorker() {
	for w := range n.dispatchQueue {
		n.channelDispatcher.Dispatch(context.Background(), w.payload)
	}
}

func (n *Notifier) enqueueDispatch(payload channel.EventPayload) {
	select {
	case n.dispatchQueue <- dispatchWork{payload: payload}:
	default:
		slog.Warn("notification channel dispatch queue full, dropping event", "event", payload.EventType)
	}
}

// Notify creates a notification for the given user and optionally broadcasts it
// via WebSocket. It returns an error only if the database insert fails.
func (n *Notifier) Notify(ctx context.Context, userID, notifType, title, message, entityType, entityID string) error {
	return n.notify(ctx, userID, notifType, title, message, entityType, entityID, true)
}

func (n *Notifier) notify(ctx context.Context, userID, notifType, title, message, entityType, entityID string, dispatchExternal bool) error {
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

	// Dispatch to external channels (Teams, Slack, etc.) in background.
	if dispatchExternal && n.channelDispatcher != nil {
		displayName := ""
		if n.userRepo != nil {
			if user, err := n.userRepo.FindByID(userID); err == nil && user != nil {
				displayName = user.DisplayName
			}
		}
		n.enqueueDispatch(channel.EventPayload{
			EventType:       notifType,
			Timestamp:       notification.CreatedAt,
			Title:           title,
			Message:         message,
			UserDisplayName: displayName,
			EntityType:      entityType,
			EntityID:        entityID,
		})
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
		if err := n.notify(ctx, u.ID, notifType, title, message, entityType, entityID, false); err != nil {
			slog.Error("failed to create system notification for user", "user_id", u.ID, "type", notifType, "error", err)
		}
	}

	// Dispatch once to external channels for system events.
	if n.channelDispatcher != nil {
		n.enqueueDispatch(channel.EventPayload{
			EventType:       notifType,
			Timestamp:       time.Now().UTC(),
			Title:           title,
			Message:         message,
			UserDisplayName: "System",
			EntityType:      entityType,
			EntityID:        entityID,
		})
	}

	return nil
}

// MessageTypeNotificationNew is the WebSocket message type for new notifications.
const MessageTypeNotificationNew = "notification.new"
