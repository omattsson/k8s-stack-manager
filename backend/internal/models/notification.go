package models

import (
	"context"
	"time"
)

// Notification represents an in-app notification for a user.
type Notification struct {
	CreatedAt  time.Time `json:"created_at" gorm:"index"`
	ID         string    `json:"id" gorm:"primaryKey;size:36"`
	UserID     string    `json:"user_id" gorm:"size:36;index;not null"`
	Type       string    `json:"type" gorm:"size:50;not null"`
	Title      string    `json:"title" gorm:"size:255;not null"`
	Message    string    `json:"message" gorm:"type:text"`
	EntityType string    `json:"entity_type,omitempty" gorm:"size:50"`
	EntityID   string    `json:"entity_id,omitempty" gorm:"size:36"`
	IsRead     bool      `json:"is_read" gorm:"default:false;index"`
}

// NotificationPreference controls whether a user receives a specific event type
// and which channel it is delivered to.
type NotificationPreference struct {
	ID        string `json:"id" gorm:"primaryKey;size:36"`
	UserID    string `json:"user_id" gorm:"size:36;uniqueIndex:idx_user_event;not null"`
	EventType string `json:"event_type" gorm:"size:50;uniqueIndex:idx_user_event;not null"`
	Enabled   bool   `json:"enabled" gorm:"default:true"`
	Channel   string `json:"channel" gorm:"size:20;default:in_app;not null"`
}

// PaginatedNotifications wraps a page of notification results with metadata.
type PaginatedNotifications struct {
	Notifications []Notification `json:"notifications"`
	Total         int64          `json:"total"`
	UnreadCount   int64          `json:"unread_count"`
}

// NotificationRepository defines data access operations for notifications.
type NotificationRepository interface {
	Create(ctx context.Context, notification *Notification) error
	ListByUser(ctx context.Context, userID string, unreadOnly bool, limit, offset int) ([]Notification, int64, error)
	CountUnread(ctx context.Context, userID string) (int64, error)
	MarkAsRead(ctx context.Context, id string, userID string) error
	MarkAllAsRead(ctx context.Context, userID string) error
	GetPreferences(ctx context.Context, userID string) ([]NotificationPreference, error)
	UpdatePreference(ctx context.Context, pref *NotificationPreference) error
	// DisabledUserIDs returns the users in userIDs that switched off the
	// event type in their preferences, with one query. A user without a
	// preference for the event type gets it (the default is on).
	DisabledUserIDs(ctx context.Context, eventType string, userIDs []string) (map[string]bool, error)
}

// NotificationTarget is the stack instance of a lifecycle event. The
// notifier uses it to find the receivers of the in-app notification (the
// owner and the followers) and to match the channel filters.
type NotificationTarget struct {
	InstanceID   string
	InstanceName string
	OwnerID      string
	DefinitionID string
	ClusterID    string
	// FollowerIDs are the followers of the instance when the caller read
	// them before the instance was deleted (a delete also deletes the
	// follower rows). When FollowerIDs is nil, the notifier reads them.
	FollowerIDs []string
}

// NewNotificationTarget returns the notification target of inst.
// FollowerIDs is nil: the notifier reads the followers.
func NewNotificationTarget(inst *StackInstance) NotificationTarget {
	if inst == nil {
		return NotificationTarget{}
	}
	return NotificationTarget{
		InstanceID:   inst.ID,
		InstanceName: inst.Name,
		OwnerID:      inst.OwnerID,
		DefinitionID: inst.StackDefinitionID,
		ClusterID:    inst.ClusterID,
	}
}
