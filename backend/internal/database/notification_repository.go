package database

import (
	"context"
	"fmt"
	"math"

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
	// totalCap is the count cap of ListByUser (models.NotificationTotalCap).
	// Same-package tests lower it.
	totalCap int
}

// NewGORMNotificationRepository creates a new GORM-backed notification repository.
func NewGORMNotificationRepository(db *gorm.DB) *GORMNotificationRepository {
	return &GORMNotificationRepository{db: db, totalCap: models.NotificationTotalCap}
}

// Create inserts a new notification record. A deadlock with MarkAllAsRead
// (or MarkAsRead) of the same user runs the insert again (#648). The retry
// cannot insert a duplicate: the insert is one autocommit statement
// (SkipDefaultTransaction), and InnoDB rolls the statement back before it
// returns error 1213, so the row of the failed attempt was never stored.
// The ID stays the same across attempts, so even a stored row would give a
// duplicate-key error (not retried), never a second notification.
func (r *GORMNotificationRepository) Create(ctx context.Context, notification *models.Notification) error {
	return retryOnDeadlock(ctx, "notification_create", func() error {
		if err := r.db.WithContext(ctx).Create(notification).Error; err != nil {
			return dberrors.NewDatabaseError("create", err)
		}
		return nil
	})
}

// ListByUser returns notifications for a user with optional unread-only filter and pagination.
// Results are ordered newest-first. total counts the matching rows before
// pagination, but at most offset + totalCap of them (#654): a user can have
// many thousands of notifications, and an exact count reads all of them on
// every list call. When more rows match, total is offset + totalCap and
// capped is true.
func (r *GORMNotificationRepository) ListByUser(ctx context.Context, userID string, unreadOnly bool, limit, offset int) ([]models.Notification, int64, bool, error) {
	if offset < 0 {
		offset = 0
	}
	total, capped, err := r.countWindow(ctx, userID, unreadOnly, offset)
	if err != nil {
		return nil, 0, false, err
	}

	query := r.db.WithContext(ctx).Where("user_id = ?", userID)
	if unreadOnly {
		query = query.Where("is_read = ?", false)
	}
	var notifications []models.Notification
	if err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&notifications).Error; err != nil {
		return nil, 0, false, dberrors.NewDatabaseError("list", err)
	}

	return notifications, total, capped, nil
}

// The windowed count queries of ListByUser. All values are bound parameters.
const (
	notificationCountAll    = "SELECT COUNT(*) FROM (SELECT 1 FROM notifications WHERE user_id = ? LIMIT ?) AS capped"
	notificationCountUnread = "SELECT COUNT(*) FROM (SELECT 1 FROM notifications WHERE user_id = ? AND is_read = ? LIMIT ?) AS capped"
)

// countWindow counts the matching notifications of the user, at most
// offset + totalCap + 1 of them, with a LIMIT in a derived table, so MySQL
// stops reading the index after that many rows. n is the true count when it
// is at most offset + totalCap (also for an offset past the end). A count of
// offset + totalCap + 1 means more rows exist: total is then
// offset + totalCap and capped is true. For an offset so large that the
// window overflows int64, the window is math.MaxInt64 (no table has that
// many rows), so the count is exact.
func (r *GORMNotificationRepository) countWindow(ctx context.Context, userID string, unreadOnly bool, offset int) (int64, bool, error) {
	capN := int64(r.totalCap)
	if capN < 0 {
		capN = 0
	}
	off := int64(offset)
	window := int64(math.MaxInt64)
	bounded := off <= math.MaxInt64-capN-1
	if bounded {
		window = off + capN + 1
	}

	stmt, args := notificationCountAll, []any{userID, window}
	if unreadOnly {
		stmt, args = notificationCountUnread, []any{userID, false, window}
	}

	var n int64
	if err := r.db.WithContext(ctx).Raw(stmt, args...).Scan(&n).Error; err != nil {
		return 0, false, dberrors.NewDatabaseError("count", err)
	}
	if bounded && n >= window {
		return off + capN, true, nil
	}
	return n, false, nil
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
// It changes the is_read index entries of the user, as MarkAllAsRead does,
// so it runs again after a deadlock too (#648). The update is idempotent.
func (r *GORMNotificationRepository) MarkAsRead(ctx context.Context, id string, userID string) error {
	return retryOnDeadlock(ctx, "notification_mark_read", func() error {
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
	})
}

// MarkAllAsRead sets is_read=true for all unread notifications of a user.
// The update locks the index ranges of the user, so a concurrent Create for
// the user can deadlock with it; it runs again after a deadlock (#648). The
// update is idempotent.
func (r *GORMNotificationRepository) MarkAllAsRead(ctx context.Context, userID string) error {
	return retryOnDeadlock(ctx, "notification_mark_all_read", func() error {
		if err := r.db.WithContext(ctx).
			Model(&models.Notification{}).
			Where("user_id = ? AND is_read = ?", userID, false).
			Update("is_read", true).Error; err != nil {
			return dberrors.NewDatabaseError("mark_all_read", err)
		}
		return nil
	})
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
// The insert uses a column map: with the struct, GORM replaces enabled=false
// (the zero value) with the field default (true), so a preference could not
// be switched off.
func (r *GORMNotificationRepository) UpdatePreference(ctx context.Context, pref *models.NotificationPreference) error {
	if pref.Channel == "" {
		pref.Channel = "in_app"
	}
	if err := r.db.WithContext(ctx).
		Model(&models.NotificationPreference{}).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "event_type"}},
			DoUpdates: clause.AssignmentColumns([]string{"enabled", "channel"}),
		}).
		Create(map[string]interface{}{
			"id":         pref.ID,
			"user_id":    pref.UserID,
			"event_type": pref.EventType,
			"enabled":    pref.Enabled,
			"channel":    pref.Channel,
		}).Error; err != nil {
		return dberrors.NewDatabaseError("update_preference", err)
	}
	return nil
}

// DisabledUserIDs returns the users in userIDs with a preference that
// switches off eventType. Users without a preference row get the event.
func (r *GORMNotificationRepository) DisabledUserIDs(ctx context.Context, eventType string, userIDs []string) (map[string]bool, error) {
	disabled := make(map[string]bool)
	if len(userIDs) == 0 {
		return disabled, nil
	}
	var ids []string
	if err := r.db.WithContext(ctx).
		Model(&models.NotificationPreference{}).
		Where("event_type = ? AND enabled = ? AND user_id IN ?", eventType, false, userIDs).
		Pluck("user_id", &ids).Error; err != nil {
		return nil, dberrors.NewDatabaseError("disabled_user_ids", err)
	}
	for _, id := range ids {
		disabled[id] = true
	}
	return disabled, nil
}
