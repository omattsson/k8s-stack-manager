package database

import (
	"context"
	"fmt"
	"time"

	"backend/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GORMReplicaHeartbeatRepository implements models.ReplicaHeartbeatRepository
// using GORM. last_seen is written and compared with the database server
// clock, so a clock difference between the replicas does not matter.
type GORMReplicaHeartbeatRepository struct {
	db *gorm.DB
}

var _ models.ReplicaHeartbeatRepository = (*GORMReplicaHeartbeatRepository)(nil)

// NewGORMReplicaHeartbeatRepository creates a GORM-backed heartbeat
// repository.
func NewGORMReplicaHeartbeatRepository(db *gorm.DB) *GORMReplicaHeartbeatRepository {
	return &GORMReplicaHeartbeatRepository{db: db}
}

// dbNow returns the SQL expression for the current database time with
// millisecond precision: UTC_TIMESTAMP(3) on MySQL, an ISO text with
// milliseconds (UTC) on SQLite (tests).
func dbNow(db *gorm.DB) string {
	if db.Dialector.Name() == "mysql" {
		return "UTC_TIMESTAMP(3)"
	}
	return "strftime('%Y-%m-%d %H:%M:%f', 'now')"
}

// dbNowMinus returns the SQL expression for the current database time minus
// age (millisecond precision).
func dbNowMinus(db *gorm.DB, age time.Duration) string {
	ms := age.Milliseconds()
	if db.Dialector.Name() == "mysql" {
		return fmt.Sprintf("(UTC_TIMESTAMP(3) - INTERVAL %d MICROSECOND)", ms*1000)
	}
	return fmt.Sprintf("strftime('%%Y-%%m-%%d %%H:%%M:%%f', 'now', '-%d.%03d seconds')", ms/1000, ms%1000)
}

// Beat inserts the row of id or updates its last_seen to the database time
// (upsert: ON DUPLICATE KEY UPDATE on MySQL, ON CONFLICT on SQLite).
func (r *GORMReplicaHeartbeatRepository) Beat(ctx context.Context, id string) error {
	now := gorm.Expr(dbNow(r.db))
	return r.db.WithContext(ctx).Model(&models.ReplicaHeartbeat{}).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{"last_seen": now}),
	}).Create(map[string]any{"id": id, "last_seen": now}).Error
}

// SeenWithin returns the IDs of ids whose last_seen is at most maxAge
// before the database time.
func (r *GORMReplicaHeartbeatRepository) SeenWithin(ctx context.Context, ids []string, maxAge time.Duration) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var found []string
	// #nosec G202 -- the expression is built from a duration, not from input.
	if err := r.db.WithContext(ctx).Model(&models.ReplicaHeartbeat{}).
		Where("id IN ? AND last_seen >= "+dbNowMinus(r.db, maxAge), ids).
		Pluck("id", &found).Error; err != nil {
		return nil, err
	}
	for _, id := range found {
		out[id] = true
	}
	return out, nil
}

// Remove deletes the row of id. A missing row is not an error.
func (r *GORMReplicaHeartbeatRepository) Remove(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.ReplicaHeartbeat{}).Error
}

// DeleteOlderThan deletes the rows whose last_seen is more than maxAge
// before the database time.
func (r *GORMReplicaHeartbeatRepository) DeleteOlderThan(ctx context.Context, maxAge time.Duration) (int64, error) {
	// #nosec G202 -- the expression is built from a duration, not from input.
	res := r.db.WithContext(ctx).Where("last_seen < " + dbNowMinus(r.db, maxAge)).Delete(&models.ReplicaHeartbeat{})
	return res.RowsAffected, res.Error
}
