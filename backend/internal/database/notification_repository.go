package database

import (
	"context"
	"fmt"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Compile-time interface check.
var _ models.NotificationRepository = (*GORMNotificationRepository)(nil)

// GORMNotificationRepository implements NotificationRepository using GORM.
type GORMNotificationRepository struct {
	db *gorm.DB
}

// NewGORMNotificationRepository creates a new GORM-backed notification repository.
func NewGORMNotificationRepository(db *gorm.DB) *GORMNotificationRepository {
	return &GORMNotificationRepository{db: db}
}

// Create inserts a new notification record.
func (r *GORMNotificationRepository) Create(ctx context.Context, notification *models.Notification) error {
	if err := r.db.WithContext(ctx).Create(notification).Error; err != nil {
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// ListByUser returns notifications for a user with optional unread-only filter and pagination.
// Results are ordered newest-first. total is the count before pagination.
func (r *GORMNotificationRepository) ListByUser(ctx context.Context, userID string, unreadOnly bool, limit, offset int) ([]models.Notification, int64, error) {
	query := r.db.WithContext(ctx).Where("user_id = ?", userID)
	if unreadOnly {
		query = query.Where("is_read = ?", false)
	}

	var total int64
	if err := query.Model(&models.Notification{}).Count(&total).Error; err != nil {
		return nil, 0, dberrors.NewDatabaseError("count", err)
	}

	var notifications []models.Notification
	if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&notifications).Error; err != nil {
		return nil, 0, dberrors.NewDatabaseError("list", err)
	}

	return notifications, total, nil
}

// CountUnread returns the number of unread notifications for a user.
func (r *GORMNotificationRepository) CountUnread(ctx context.Context, userID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&models.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false).
		Count(&count).Error; err != nil {
		return 0, dberrors.NewDatabaseError("count_unread", err)
	}
	return count, nil
}

// MarkAsRead sets is_read=true for a specific notification owned by the user.
func (r *GORMNotificationRepository) MarkAsRead(ctx context.Context, id string, userID string) error {
	result := r.db.WithContext(ctx).
		Model(&models.Notification{}).
		Where("id = ? AND user_id = ?", id, userID).
		Update("is_read", true)
	if result.Error != nil {
		return dberrors.NewDatabaseError("mark_read", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("mark_read", fmt.Errorf("%w", dberrors.ErrNotFound))
	}
	return nil
}

// MarkAllAsRead sets is_read=true for all unread notifications of a user.
func (r *GORMNotificationRepository) MarkAllAsRead(ctx context.Context, userID string) error {
	if err := r.db.WithContext(ctx).
		Model(&models.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false).
		Update("is_read", true).Error; err != nil {
		return dberrors.NewDatabaseError("mark_all_read", err)
	}
	return nil
}

// GetPreferences returns all notification preferences for a user.
func (r *GORMNotificationRepository) GetPreferences(ctx context.Context, userID string) ([]models.NotificationPreference, error) {
	var prefs []models.NotificationPreference
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Find(&prefs).Error; err != nil {
		return nil, dberrors.NewDatabaseError("get_preferences", err)
	}
	return prefs, nil
}

// UpdatePreference upserts a notification preference for a user+event_type.
// Uses ON CONFLICT on the (user_id, event_type) unique index to atomically
// insert or update, preventing races and duplicate-key violations.
func (r *GORMNotificationRepository) UpdatePreference(ctx context.Context, pref *models.NotificationPreference) error {
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "event_type"}},
			DoUpdates: clause.AssignmentColumns([]string{"enabled", "channel"}),
		}).
		Create(pref).Error; err != nil {
		return dberrors.NewDatabaseError("update_preference", err)
	}
	return nil
}
