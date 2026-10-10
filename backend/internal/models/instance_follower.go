package models

import (
	"context"
	"time"
)

// InstanceFollower records that a user follows a stack instance. A follower
// gets the in-app notifications of the instance, as the owner does, filtered
// by the event-type preferences of the follower. The pair (user_id,
// instance_id) is the primary key, so a user follows an instance once.
type InstanceFollower struct {
	CreatedAt  time.Time `json:"created_at"`
	UserID     string    `json:"user_id" gorm:"primaryKey;size:36"`
	InstanceID string    `json:"instance_id" gorm:"primaryKey;size:36;index:idx_instance_followers_instance"`
}

// InstanceFollowerRepository defines data access operations for instance
// followers.
type InstanceFollowerRepository interface {
	// Follow adds the pair. An existing pair is not an error (idempotent).
	// A missing instance gives ErrNotFound; the check and the insert are
	// atomic against an instance delete.
	Follow(ctx context.Context, userID, instanceID string) error
	// Unfollow removes the pair. A missing pair is not an error (idempotent).
	Unfollow(ctx context.Context, userID, instanceID string) error
	// FollowState returns whether userID follows the instance and the
	// number of followers of the instance, with one query.
	FollowState(ctx context.Context, userID, instanceID string) (following bool, count int64, err error)
	// ListUserIDsByInstance returns the user IDs that follow the instance.
	ListUserIDsByInstance(ctx context.Context, instanceID string) ([]string, error)
	// DeleteByInstance removes all followers of the instance.
	DeleteByInstance(ctx context.Context, instanceID string) error
	// DeleteByUser removes all follows of the user.
	DeleteByUser(ctx context.Context, userID string) error
}
