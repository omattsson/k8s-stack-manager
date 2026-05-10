package database

import (
	"context"
	"fmt"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Compile-time interface check.
var _ models.NotificationChannelRepository = (*GORMNotificationChannelRepository)(nil)

// GORMNotificationChannelRepository implements NotificationChannelRepository using GORM.
type GORMNotificationChannelRepository struct {
	db *gorm.DB
}

// NewGORMNotificationChannelRepository creates a new GORM-backed notification channel repository.
func NewGORMNotificationChannelRepository(db *gorm.DB) *GORMNotificationChannelRepository {
	return &GORMNotificationChannelRepository{db: db}
}

// CreateChannel inserts a new notification channel.
func (r *GORMNotificationChannelRepository) CreateChannel(ctx context.Context, channel *models.NotificationChannel) error {
	if err := r.db.WithContext(ctx).Create(channel).Error; err != nil {
		return dberrors.NewDatabaseError("create_channel", err)
	}
	return nil
}

// GetChannel returns a single notification channel by ID.
func (r *GORMNotificationChannelRepository) GetChannel(ctx context.Context, id string) (*models.NotificationChannel, error) {
	var channel models.NotificationChannel
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&channel).Error; err != nil {
		return nil, dberrors.NewDatabaseError("get_channel", err)
	}
	return &channel, nil
}

// UpdateChannel updates an existing notification channel.
func (r *GORMNotificationChannelRepository) UpdateChannel(ctx context.Context, channel *models.NotificationChannel) error {
	result := r.db.WithContext(ctx).Save(channel)
	if result.Error != nil {
		return dberrors.NewDatabaseError("update_channel", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("update_channel", fmt.Errorf("%w", dberrors.ErrNotFound))
	}
	return nil
}

// DeleteChannel removes a notification channel and its subscriptions by ID.
func (r *GORMNotificationChannelRepository) DeleteChannel(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Delete subscriptions first.
		if err := tx.Where("channel_id = ?", id).Delete(&models.NotificationChannelSubscription{}).Error; err != nil {
			return dberrors.NewDatabaseError("delete_channel_subscriptions", err)
		}
		result := tx.Where("id = ?", id).Delete(&models.NotificationChannel{})
		if result.Error != nil {
			return dberrors.NewDatabaseError("delete_channel", result.Error)
		}
		if result.RowsAffected == 0 {
			return dberrors.NewDatabaseError("delete_channel", fmt.Errorf("%w", dberrors.ErrNotFound))
		}
		return nil
	})
}

// ListChannels returns all notification channels.
func (r *GORMNotificationChannelRepository) ListChannels(ctx context.Context) ([]models.NotificationChannel, error) {
	var channels []models.NotificationChannel
	if err := r.db.WithContext(ctx).Order("name ASC").Find(&channels).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_channels", err)
	}
	return channels, nil
}

// ListEnabledChannels returns all enabled notification channels.
func (r *GORMNotificationChannelRepository) ListEnabledChannels(ctx context.Context) ([]models.NotificationChannel, error) {
	var channels []models.NotificationChannel
	if err := r.db.WithContext(ctx).Where("enabled = ?", true).Order("name ASC").Find(&channels).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_enabled_channels", err)
	}
	return channels, nil
}

// SetSubscriptions replaces all subscriptions for a channel with the given event types.
func (r *GORMNotificationChannelRepository) SetSubscriptions(ctx context.Context, channelID string, eventTypes []string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Delete existing subscriptions.
		if err := tx.Where("channel_id = ?", channelID).Delete(&models.NotificationChannelSubscription{}).Error; err != nil {
			return dberrors.NewDatabaseError("delete_subscriptions", err)
		}
		// Bulk insert new subscriptions.
		if len(eventTypes) > 0 {
			subs := make([]models.NotificationChannelSubscription, len(eventTypes))
			for i, et := range eventTypes {
				subs[i] = models.NotificationChannelSubscription{
					ID:        uuid.New().String(),
					ChannelID: channelID,
					EventType: et,
				}
			}
			if err := tx.Create(&subs).Error; err != nil {
				return dberrors.NewDatabaseError("create_subscriptions", err)
			}
		}
		return nil
	})
}

// GetSubscriptions returns all subscriptions for a channel.
func (r *GORMNotificationChannelRepository) GetSubscriptions(ctx context.Context, channelID string) ([]models.NotificationChannelSubscription, error) {
	var subs []models.NotificationChannelSubscription
	if err := r.db.WithContext(ctx).Where("channel_id = ?", channelID).Find(&subs).Error; err != nil {
		return nil, dberrors.NewDatabaseError("get_subscriptions", err)
	}
	return subs, nil
}

// FindChannelsByEvent returns all enabled channels subscribed to the given event type.
func (r *GORMNotificationChannelRepository) FindChannelsByEvent(ctx context.Context, eventType string) ([]models.NotificationChannel, error) {
	var channels []models.NotificationChannel
	err := r.db.WithContext(ctx).
		Joins("JOIN notification_channel_subscriptions ON notification_channel_subscriptions.channel_id = notification_channels.id").
		Where("notification_channel_subscriptions.event_type = ? AND notification_channels.enabled = ?", eventType, true).
		Find(&channels).Error
	if err != nil {
		return nil, dberrors.NewDatabaseError("find_channels_by_event", err)
	}
	return channels, nil
}

// CreateDeliveryLog inserts a new delivery log record.
func (r *GORMNotificationChannelRepository) CreateDeliveryLog(ctx context.Context, log *models.NotificationDeliveryLog) error {
	if err := r.db.WithContext(ctx).Create(log).Error; err != nil {
		return dberrors.NewDatabaseError("create_delivery_log", err)
	}
	return nil
}

// ListDeliveryLogs returns paginated delivery logs for a channel, ordered by created_at DESC.
func (r *GORMNotificationChannelRepository) ListDeliveryLogs(ctx context.Context, channelID string, limit, offset int) ([]models.NotificationDeliveryLog, int64, error) {
	query := r.db.WithContext(ctx).Where("channel_id = ?", channelID)

	var total int64
	if err := query.Model(&models.NotificationDeliveryLog{}).Count(&total).Error; err != nil {
		return nil, 0, dberrors.NewDatabaseError("count_delivery_logs", err)
	}

	var logs []models.NotificationDeliveryLog
	if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&logs).Error; err != nil {
		return nil, 0, dberrors.NewDatabaseError("list_delivery_logs", err)
	}

	return logs, total, nil
}
