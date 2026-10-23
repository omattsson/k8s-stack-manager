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
	Trigger    Trigger   `json:"trigger,omitzero" gorm:"embedded"`
	Title      string    `json:"title" gorm:"size:255;not null"`
	Message    string    `json:"message" gorm:"type:text"`
	EntityType string    `json:"entity_type,omitempty" gorm:"size:50"`
	EntityID   string    `json:"entity_id,omitempty" gorm:"size:36"`
	IsRead     bool      `json:"is_read" gorm:"default:false"`
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

// Trigger is what started the action of a notification (#652); empty fields are NULL (default:null).
// The Go names differ from Notification.ID and Type: GORM also maps embedded fields by Go name.
type Trigger struct {
	TriggerType string `json:"type" gorm:"size:20;default:null" enums:"user,cleanup-policy,ttl"`
	TriggerID   string `json:"id,omitempty" gorm:"size:36;default:null"`
	TriggerName string `json:"name,omitempty" gorm:"size:255;default:null"`
}

// NotificationTotalCap is the count cap of the notification counts (#654),
// so a count never reads every notification of the user:
//   - The list total counts at most offset + NotificationTotalCap + 1 rows.
//     A larger total gives offset + NotificationTotalCap and
//     PaginatedNotifications.TotalCapped.
//   - The unread count counts at most NotificationTotalCap + 1 rows. A
//     larger count gives NotificationTotalCap and UnreadCountCapped.
const NotificationTotalCap = 1000

// PaginatedNotifications wraps a page of notification results with metadata.
type PaginatedNotifications struct {
	Notifications []Notification `json:"notifications"`
	// Total is the number of matching notifications. It is exact unless
	// total_capped is true; then it is offset + 1000, and at least
	// total + 1 notifications match.
	Total int64 `json:"total"`
	// UnreadCount is the number of unread notifications of the user. It is
	// exact unless unread_count_capped is true; then it is 1000, and the
	// user has at least 1001 unread notifications.
	UnreadCount int64 `json:"unread_count"`
	// TotalCapped (total_capped) is true when more than offset + 1000
	// notifications match. total is then offset + 1000, a lower bound: at
	// least total + 1 notifications exist.
	TotalCapped bool `json:"total_capped"`
	// UnreadCountCapped (unread_count_capped) is true when the user has more
	// than 1000 unread notifications. unread_count is then 1000.
	UnreadCountCapped bool `json:"unread_count_capped"`
}

// UnreadNotificationCount is the response of GET /notifications/count.
type UnreadNotificationCount struct {
	// UnreadCount is the number of unread notifications of the user. It is
	// exact unless unread_count_capped is true; then it is 1000, and the
	// user has at least 1001 unread notifications.
	UnreadCount int64 `json:"unread_count"`
	// UnreadCountCapped (unread_count_capped) is true when the user has more
	// than 1000 unread notifications.
	UnreadCountCapped bool `json:"unread_count_capped"`
}

// NotificationRepository defines data access operations for notifications.
type NotificationRepository interface {
	Create(ctx context.Context, notification *Notification) error
	// ListByUser returns one page of the notifications of the user (newest
	// first), the total and capped. The total counts at most
	// offset + NotificationTotalCap rows: when more rows match, total is
	// offset + NotificationTotalCap and capped is true.
	ListByUser(ctx context.Context, userID string, unreadOnly bool, limit, offset int) (page []Notification, total int64, capped bool, err error)
	// CountUnread returns the number of unread notifications of the user,
	// at most NotificationTotalCap: when more are unread, count is
	// NotificationTotalCap and capped is true.
	CountUnread(ctx context.Context, userID string) (count int64, capped bool, err error)
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
	// Namespace and Branch are display data for the channel payload (the
	// namespace and the default branch of the instance).
	Namespace string
	Branch    string
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
		Namespace:    inst.Namespace,
		Branch:       inst.Branch,
	}
}
