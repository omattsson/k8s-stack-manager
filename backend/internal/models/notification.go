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
}
