package database

import (
	"context"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Compile-time interface check.
var _ models.InstanceFollowerRepository = (*GORMInstanceFollowerRepository)(nil)

// GORMInstanceFollowerRepository implements models.InstanceFollowerRepository
// using GORM.
type GORMInstanceFollowerRepository struct {
	db *gorm.DB
}

// NewGORMInstanceFollowerRepository creates a GORM-backed instance follower
// repository.
func NewGORMInstanceFollowerRepository(db *gorm.DB) *GORMInstanceFollowerRepository {
	return &GORMInstanceFollowerRepository{db: db}
}

// Follow adds the pair. An existing pair is kept (ON CONFLICT DO NOTHING on
// the primary key), so a repeated follow is not an error. A missing instance
// gives ErrNotFound.
//
// The transaction reads the instance row with a shared lock (SELECT ... FOR
// SHARE) before the insert. An instance delete (DeleteInstanceRecord)
// deletes the instance row first and the followers after it, in one
// transaction. So either the delete waits for the follow and then removes
// the new row, or the follow waits for the delete and finds no instance:
// no follower row stays without its instance.
func (r *GORMInstanceFollowerRepository) Follow(ctx context.Context, userID, instanceID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []string
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
			Model(&models.StackInstance{}).
			Where("id = ?", instanceID).
			Pluck("id", &ids).Error; err != nil {
			return dberrors.NewDatabaseError("follow", err)
		}
		if len(ids) == 0 {
			return dberrors.NewDatabaseError("follow", dberrors.ErrNotFound)
		}
		row := &models.InstanceFollower{
			UserID:     userID,
			InstanceID: instanceID,
			CreatedAt:  time.Now().UTC(),
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
			return dberrors.NewDatabaseError("follow", err)
		}
		return nil
	})
}

// Unfollow removes the pair. A missing pair is not an error.
func (r *GORMInstanceFollowerRepository) Unfollow(ctx context.Context, userID, instanceID string) error {
	if err := r.db.WithContext(ctx).
		Where("user_id = ? AND instance_id = ?", userID, instanceID).
		Delete(&models.InstanceFollower{}).Error; err != nil {
		return dberrors.NewDatabaseError("unfollow", err)
	}
	return nil
}

// FollowState returns whether userID follows the instance and the number of
// followers, with one query on the instance_id index.
func (r *GORMInstanceFollowerRepository) FollowState(ctx context.Context, userID, instanceID string) (bool, int64, error) {
	var row struct {
		Total int64
		Mine  int64
	}
	if err := r.db.WithContext(ctx).
		Model(&models.InstanceFollower{}).
		Select("COUNT(*) AS total, COALESCE(SUM(CASE WHEN user_id = ? THEN 1 ELSE 0 END), 0) AS mine", userID).
		Where("instance_id = ?", instanceID).
		Scan(&row).Error; err != nil {
		return false, 0, dberrors.NewDatabaseError("follow_state", err)
	}
	return row.Mine > 0, row.Total, nil
}

// ListUserIDsByInstance returns the user IDs that follow the instance.
func (r *GORMInstanceFollowerRepository) ListUserIDsByInstance(ctx context.Context, instanceID string) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).
		Model(&models.InstanceFollower{}).
		Where("instance_id = ?", instanceID).
		Order("user_id").
		Pluck("user_id", &ids).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_followers", err)
	}
	return ids, nil
}

// DeleteByInstance removes all followers of the instance.
func (r *GORMInstanceFollowerRepository) DeleteByInstance(ctx context.Context, instanceID string) error {
	if err := r.db.WithContext(ctx).
		Where("instance_id = ?", instanceID).
		Delete(&models.InstanceFollower{}).Error; err != nil {
		return dberrors.NewDatabaseError("delete_followers_by_instance", err)
	}
	return nil
}

// DeleteByUser removes all follows of the user.
func (r *GORMInstanceFollowerRepository) DeleteByUser(ctx context.Context, userID string) error {
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Delete(&models.InstanceFollower{}).Error; err != nil {
		return dberrors.NewDatabaseError("delete_followers_by_user", err)
	}
	return nil
}
