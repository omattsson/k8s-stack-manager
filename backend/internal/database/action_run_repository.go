package database

import (
	"context"
	"fmt"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Compile-time interface check.
var _ models.ActionRunRepository = (*GORMActionRunRepository)(nil)

// GORMActionRunRepository implements models.ActionRunRepository using GORM.
type GORMActionRunRepository struct {
	db *gorm.DB
}

// NewGORMActionRunRepository creates a GORM-backed action run repository.
func NewGORMActionRunRepository(db *gorm.DB) *GORMActionRunRepository {
	return &GORMActionRunRepository{db: db}
}

// Create inserts run. It sets ID when empty and StartedAt when zero.
func (r *GORMActionRunRepository) Create(ctx context.Context, run *models.ActionRun) error {
	if run.ID == "" {
		run.ID = uuid.New().String()
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	if err := r.db.WithContext(ctx).Create(run).Error; err != nil {
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// ListByInstance returns the runs of the instance, newest first, with keyset
// paging on (started_at, id). It reads limit+1 rows to find the next cursor.
func (r *GORMActionRunRepository) ListByInstance(ctx context.Context, instanceID, cursor string, limit int) ([]models.ActionRun, string, error) {
	if limit <= 0 {
		limit = models.DefaultActionRunPageSize
	}
	limit = min(limit, models.MaxActionRunPageSize)

	query := r.db.WithContext(ctx).Where("instance_id = ?", instanceID)
	if cursor != "" {
		cursorTS, cursorID, err := decodeDeployLogCursor(cursor)
		if err != nil {
			return nil, "", dberrors.NewDatabaseError("list_action_runs", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
		}
		query = query.Where("((started_at < ?) OR (started_at = ? AND id < ?))", cursorTS, cursorTS, cursorID)
	}

	var runs []models.ActionRun
	if err := query.Order("started_at DESC, id DESC").Limit(limit + 1).Find(&runs).Error; err != nil {
		return nil, "", dberrors.NewDatabaseError("list_action_runs", err)
	}
	next := ""
	if len(runs) > limit {
		runs = runs[:limit]
		last := runs[limit-1]
		next = encodeDeployLogCursor(last.StartedAt, last.ID)
	}
	return runs, next, nil
}

// SetJobFinalStatus stores the final job status with one conditional UPDATE.
// The condition (no stored status and no finished_at) makes the first caller
// win when replicas poll the same job; a later call changes no row.
func (r *GORMActionRunRepository) SetJobFinalStatus(ctx context.Context, instanceID, action, jobID, status string, finishedAt time.Time) (bool, error) {
	if jobID == "" || status == "" {
		return false, nil
	}
	res := r.db.WithContext(ctx).Model(&models.ActionRun{}).
		Where("instance_id = ? AND action = ? AND job_id = ? AND job_status = '' AND finished_at IS NULL", instanceID, action, jobID).
		Updates(map[string]any{"job_status": status, "finished_at": finishedAt})
	if res.Error != nil {
		return false, dberrors.NewDatabaseError("set_job_final_status", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// DeleteByInstance removes all runs of the instance.
func (r *GORMActionRunRepository) DeleteByInstance(ctx context.Context, instanceID string) error {
	if err := r.db.WithContext(ctx).Where("instance_id = ?", instanceID).Delete(&models.ActionRun{}).Error; err != nil {
		return dberrors.NewDatabaseError("delete_action_runs", err)
	}
	return nil
}
