package database

import (
	"context"
	"errors"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Compile-time interface check.
var _ models.DeploymentLogRepository = (*GORMDeploymentLogRepository)(nil)

// GORMDeploymentLogRepository implements models.DeploymentLogRepository using GORM.
type GORMDeploymentLogRepository struct {
	db *gorm.DB
}

// NewGORMDeploymentLogRepository creates a new GORM-backed deployment log repository.
func NewGORMDeploymentLogRepository(db *gorm.DB) *GORMDeploymentLogRepository {
	return &GORMDeploymentLogRepository{db: db}
}

// Create inserts a new deployment log record.
func (r *GORMDeploymentLogRepository) Create(_ context.Context, log *models.DeploymentLog) error {
	if log.ID == "" {
		log.ID = uuid.New().String()
	}
	if log.StartedAt.IsZero() {
		log.StartedAt = time.Now().UTC()
	}
	if err := r.db.Create(log).Error; err != nil {
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns a deployment log by its ID.
func (r *GORMDeploymentLogRepository) FindByID(_ context.Context, id string) (*models.DeploymentLog, error) {
	var log models.DeploymentLog
	if err := r.db.Where("id = ?", id).First(&log).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &log, nil
}

// Update persists changes to an existing deployment log record.
func (r *GORMDeploymentLogRepository) Update(_ context.Context, log *models.DeploymentLog) error {
	if err := r.db.Save(log).Error; err != nil {
		return dberrors.NewDatabaseError("update", err)
	}
	return nil
}

// ListByInstance returns all deployment logs for a given stack instance, ordered by started_at descending.
func (r *GORMDeploymentLogRepository) ListByInstance(_ context.Context, instanceID string) ([]models.DeploymentLog, error) {
	var logs []models.DeploymentLog
	if err := r.db.Where("stack_instance_id = ?", instanceID).
		Order("started_at DESC").
		Find(&logs).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_instance", err)
	}
	return logs, nil
}

// GetLatestByInstance returns the most recent deployment log for a given stack instance.
func (r *GORMDeploymentLogRepository) GetLatestByInstance(_ context.Context, instanceID string) (*models.DeploymentLog, error) {
	var log models.DeploymentLog
	if err := r.db.Where("stack_instance_id = ?", instanceID).
		Order("started_at DESC").
		First(&log).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("get_latest", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("get_latest", err)
	}
	return &log, nil
}

// SummarizeByInstance returns aggregate deployment statistics for a given stack instance.
// Only logs from the last 90 days are considered.
func (r *GORMDeploymentLogRepository) SummarizeByInstance(_ context.Context, instanceID string) (*models.DeployLogSummary, error) {
	cutoff := time.Now().UTC().Add(-90 * 24 * time.Hour)

	summary := &models.DeployLogSummary{InstanceID: instanceID}

	// Count deploy actions and their statuses.
	row := r.db.Model(&models.DeploymentLog{}).
		Select("COUNT(*) as deploy_count, "+
			"COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) as success_count, "+
			"COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) as error_count",
			models.DeployLogSuccess, models.DeployLogError).
		Where("stack_instance_id = ? AND action = ? AND started_at >= ?",
			instanceID, models.DeployActionDeploy, cutoff).
		Row()
	if err := row.Err(); err != nil {
		return nil, dberrors.NewDatabaseError("summarize_by_instance", err)
	}
	if err := row.Scan(&summary.DeployCount, &summary.SuccessCount, &summary.ErrorCount); err != nil {
		return nil, dberrors.NewDatabaseError("summarize_by_instance", err)
	}

	// Get the latest activity timestamp across all actions.
	var lastDeployRaw *string
	row2 := r.db.Model(&models.DeploymentLog{}).
		Select("MAX(COALESCE(completed_at, started_at))").
		Where("stack_instance_id = ? AND started_at >= ?", instanceID, cutoff).
		Row()
	if err := row2.Err(); err != nil {
		return nil, dberrors.NewDatabaseError("summarize_by_instance", err)
	}
	if err := row2.Scan(&lastDeployRaw); err != nil {
		return nil, dberrors.NewDatabaseError("summarize_by_instance", err)
	}
	if lastDeployRaw != nil {
		parsed, err := time.Parse("2006-01-02T15:04:05Z07:00", *lastDeployRaw)
		if err != nil {
			// Try alternate format used by some DB drivers.
			parsed, err = time.Parse("2006-01-02 15:04:05.999999999-07:00", *lastDeployRaw)
		}
		if err == nil {
			summary.LastDeployAt = &parsed
		}
	}

	return summary, nil
}
