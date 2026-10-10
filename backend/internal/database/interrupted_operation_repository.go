package database

import (
	"context"
	"errors"
	"time"

	"backend/internal/models"

	"gorm.io/gorm"
)

// GORMInterruptedOperationRepository implements
// models.InterruptedOperationRepository using GORM.
type GORMInterruptedOperationRepository struct {
	db *gorm.DB
}

var _ models.InterruptedOperationRepository = (*GORMInterruptedOperationRepository)(nil)

// NewGORMInterruptedOperationRepository creates a GORM-backed repository for
// the recovery of interrupted operations.
func NewGORMInterruptedOperationRepository(db *gorm.DB) *GORMInterruptedOperationRepository {
	return &GORMInterruptedOperationRepository{db: db}
}

// staleHeartbeatCond is the SQL condition "the replica of the log (column
// expression replicaCol) has no heartbeat within staleAfter". It compares
// last_seen with the database time only.
func staleHeartbeatCond(db *gorm.DB, replicaCol string, staleAfter time.Duration) string {
	return "NOT EXISTS (SELECT 1 FROM replica_heartbeats h WHERE h.id = " + replicaCol +
		" AND h.last_seen >= " + dbNowMinus(db, staleAfter) + ")"
}

// ListInterruptCandidates returns at most limit running deploy logs that
// passed their deadline, belong to another process and whose process has no
// fresh heartbeat, oldest first. The index idx_deployment_logs_status_started
// (status, started_at) serves the status filter and the order. deadline_at
// was written with the clock of the replica, so it is compared with now (the
// clock of the leader), not with the database time: the driver can store
// app times in another zone than UTC_TIMESTAMP. A skew of seconds does not
// matter for a margin of minutes.
func (r *GORMInterruptedOperationRepository) ListInterruptCandidates(ctx context.Context, now time.Time, selfID string, staleAfter time.Duration, limit int) ([]models.DeploymentLog, error) {
	var logs []models.DeploymentLog
	// #nosec G202 -- staleHeartbeatCond is built from constants and a duration.
	err := r.db.WithContext(ctx).Model(&models.DeploymentLog{}).
		Select("id, stack_instance_id, action, status, started_at, replica_id, deadline_at").
		Where("status = ? AND deadline_at IS NOT NULL AND deadline_at < ? AND replica_id IS NOT NULL AND replica_id <> '' AND replica_id <> ?",
			models.DeployLogRunning, now.UTC(), selfID).
		Where(staleHeartbeatCond(r.db, "deployment_logs.replica_id", staleAfter)).
		Order("started_at ASC").
		Limit(limit).
		Find(&logs).Error
	return logs, err
}

// errInterruptRollback rolls back the transaction of InterruptOperation.
var errInterruptRollback = errors.New("rollback")

// InterruptOperation ends the operation of req.LogID in one transaction:
//
//  1. The log gets error with req.Message and completed_at = req.Now, only
//     when its status is still running and its replica (req.ReplicaID) has
//     no heartbeat within req.StaleAfter. Otherwise nothing changes.
//  2. When req.InstanceStatus is set and no log of the instance started
//     after req.LogStartedAt, the instance gets error with req.Message, and
//     post_deploy_hook_until is cleared. The update requires the status
//     and updated_at that the caller read (optimistic lock): when another
//     process changed the instance, the transaction rolls back and
//     ErrInterruptConflict is returned.
func (r *GORMInterruptedOperationRepository) InterruptOperation(ctx context.Context, req models.InterruptRequest) (models.InterruptResult, error) {
	var result models.InterruptResult
	now := req.Now.UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// #nosec G202 -- staleHeartbeatCond is built from constants and a duration.
		res := tx.Model(&models.DeploymentLog{}).
			Where("id = ? AND status = ? AND replica_id = ?", req.LogID, models.DeployLogRunning, req.ReplicaID).
			Where(staleHeartbeatCond(tx, "deployment_logs.replica_id", req.StaleAfter)).
			Updates(map[string]any{
				"status":        models.DeployLogError,
				"error_message": req.Message,
				"completed_at":  now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// The operation ended in the meantime.
			return nil
		}
		result.LogClosed = true
		if req.InstanceStatus == "" {
			return nil
		}

		var newer int64
		if err := tx.Model(&models.DeploymentLog{}).
			Where("stack_instance_id = ? AND id <> ? AND started_at > ?", req.InstanceID, req.LogID, req.LogStartedAt.UTC()).
			Count(&newer).Error; err != nil {
			return err
		}
		if newer > 0 {
			// A newer operation owns the instance status.
			return nil
		}

		res = tx.Model(&models.StackInstance{}).
			Where("id = ? AND status = ? AND updated_at = ?", req.InstanceID, req.InstanceStatus, req.InstanceUpdatedAt).
			Updates(map[string]any{
				"status":                 models.StackStatusError,
				"error_message":          req.Message,
				"post_deploy_hook_until": nil,
				"updated_at":             now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errInterruptRollback
		}
		result.InstanceUpdated = true
		return nil
	})
	if errors.Is(err, errInterruptRollback) {
		return models.InterruptResult{}, models.ErrInterruptConflict
	}
	if err != nil {
		return models.InterruptResult{}, err
	}
	return result, nil
}
