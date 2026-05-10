package models

import (
	"context"
	"time"
)

// NotificationChannel represents an outgoing webhook channel for system notifications.
type NotificationChannel struct {
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	ID         string    `json:"id" gorm:"primaryKey;size:36"`
	Name       string    `json:"name" gorm:"size:255;uniqueIndex;not null"`
	WebhookURL string    `json:"webhook_url" gorm:"size:2048;not null"`
	Secret     string    `json:"-" gorm:"type:text"`
	Enabled    bool      `json:"enabled" gorm:"default:true"`
}

// NotificationChannelSubscription links a channel to a specific event type.
type NotificationChannelSubscription struct {
	ID        string `json:"id" gorm:"primaryKey;size:36"`
	ChannelID string `json:"channel_id" gorm:"size:36;uniqueIndex:idx_channel_event;not null"`
	EventType string `json:"event_type" gorm:"size:50;uniqueIndex:idx_channel_event;not null"`
}

// NotificationDeliveryLog records each webhook delivery attempt.
type NotificationDeliveryLog struct {
	CreatedAt    time.Time `json:"created_at" gorm:"index"`
	ID           string    `json:"id" gorm:"primaryKey;size:36"`
	ChannelID    string    `json:"channel_id" gorm:"size:36;index;not null"`
	ChannelName  string    `json:"channel_name" gorm:"size:255"`
	EventType    string    `json:"event_type" gorm:"size:50"`
	Status       string    `json:"status" gorm:"size:20;not null"`
	StatusCode   int       `json:"status_code"`
	ErrorMessage string    `json:"error_message,omitempty" gorm:"type:text"`
}

// NotificationChannelRepository defines persistence operations for notification channels.
type NotificationChannelRepository interface {
	CreateChannel(ctx context.Context, channel *NotificationChannel) error
	GetChannel(ctx context.Context, id string) (*NotificationChannel, error)
	UpdateChannel(ctx context.Context, channel *NotificationChannel) error
	DeleteChannel(ctx context.Context, id string) error
	ListChannels(ctx context.Context) ([]NotificationChannel, error)
	ListEnabledChannels(ctx context.Context) ([]NotificationChannel, error)
	SetSubscriptions(ctx context.Context, channelID string, eventTypes []string) error
	GetSubscriptions(ctx context.Context, channelID string) ([]NotificationChannelSubscription, error)
	CountSubscriptionsByChannel(ctx context.Context) (map[string]int, error)
	FindChannelsByEvent(ctx context.Context, eventType string) ([]NotificationChannel, error)
	CreateDeliveryLog(ctx context.Context, log *NotificationDeliveryLog) error
	ListDeliveryLogs(ctx context.Context, channelID string, limit, offset int) ([]NotificationDeliveryLog, int64, error)
}
