package database

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
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

// ListByInstancePaginated returns deployment logs for a given stack instance with
// cursor-based or offset-based pagination. When filters.Cursor is set, keyset
// pagination is used for efficient deep-page traversal. Otherwise, traditional
// OFFSET pagination is used for backward compatibility.
func (r *GORMDeploymentLogRepository) ListByInstancePaginated(_ context.Context, filters models.DeploymentLogFilters) (*models.DeploymentLogResult, error) {
	query := r.db.Model(&models.DeploymentLog{}).Where("stack_instance_id = ?", filters.InstanceID)

	limit := filters.Limit
	if limit <= 0 {
		limit = 50
	}

	// Cursor-based pagination path.
	if filters.Cursor != "" {
		cursorTS, cursorID, err := decodeDeployLogCursor(filters.Cursor)
		if err != nil {
			return nil, dberrors.NewDatabaseError("list_by_instance", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
		}
		query = query.Where(
			"(started_at < ?) OR (started_at = ? AND id < ?)",
			cursorTS, cursorTS, cursorID,
		)

		var logs []models.DeploymentLog
		if err := query.Order("started_at DESC, id DESC").Limit(limit + 1).Find(&logs).Error; err != nil {
			return nil, dberrors.NewDatabaseError("list_by_instance", err)
		}

		result := &models.DeploymentLogResult{
			Total: -1,
		}
		if len(logs) > limit {
			logs = logs[:limit]
			last := logs[limit-1]
			result.NextCursor = encodeDeployLogCursor(last.StartedAt, last.ID)
		}
		result.Data = logs
		return result, nil
	}

	// Traditional OFFSET pagination path.
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, dberrors.NewDatabaseError("count", err)
	}

	var logs []models.DeploymentLog
	if err := query.Order("started_at DESC, id DESC").Limit(limit).Offset(filters.Offset).Find(&logs).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_instance", err)
	}

	return &models.DeploymentLogResult{
		Data:  logs,
		Total: total,
	}, nil
}

// encodeDeployLogCursor creates an opaque cursor from a timestamp and ID.
func encodeDeployLogCursor(ts time.Time, id string) string {
	return base64.StdEncoding.EncodeToString(
		[]byte(ts.UTC().Format(time.RFC3339Nano) + "|" + id),
	)
}

// decodeDeployLogCursor extracts a timestamp and ID from an opaque cursor.
func decodeDeployLogCursor(cursor string) (time.Time, string, error) {
	data, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid cursor encoding")
	}
	parts := strings.SplitN(string(data), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return time.Time{}, "", fmt.Errorf("invalid cursor format")
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid cursor timestamp")
	}
	return ts, parts[1], nil
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
	// Scan into sql.NullString for cross-driver compatibility (MySQL returns
	// time.Time natively but SQLite returns strings for computed columns).
	var lastDeployRaw sql.NullString
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
	if lastDeployRaw.Valid {
		summary.LastDeployAt = parseTimestamp(lastDeployRaw.String)
	}

	return summary, nil
}

// timestampFormats lists the formats drivers may use for datetime strings,
// ordered from most to least common.
var timestampFormats = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05Z",
	"2006-01-02 15:04:05",
}

// parseTimestamp attempts to parse a timestamp string using common DB driver
// formats. Returns nil if no format matches.
func parseTimestamp(s string) *time.Time {
	for _, layout := range timestampFormats {
		if t, err := time.Parse(layout, s); err == nil {
			utc := t.UTC()
			return &utc
		}
	}
	return nil
}
