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

// Compile-time interface checks.
var (
	_ models.DeploymentLogRepository       = (*GORMDeploymentLogRepository)(nil)
	_ models.DeploymentLogDeadlineExtender = (*GORMDeploymentLogRepository)(nil)
)

// GORMDeploymentLogRepository implements models.DeploymentLogRepository using GORM.
type GORMDeploymentLogRepository struct {
	db *gorm.DB
}

// NewGORMDeploymentLogRepository creates a new GORM-backed deployment log repository.
func NewGORMDeploymentLogRepository(db *gorm.DB) *GORMDeploymentLogRepository {
	return &GORMDeploymentLogRepository{db: db}
}

// Create inserts a new deployment log record.
func (r *GORMDeploymentLogRepository) Create(ctx context.Context, log *models.DeploymentLog) error {
	if log.ID == "" {
		log.ID = uuid.New().String()
	}
	if log.StartedAt.IsZero() {
		log.StartedAt = time.Now().UTC()
	}
	if err := r.db.WithContext(ctx).Create(log).Error; err != nil {
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns a deployment log by its ID.
func (r *GORMDeploymentLogRepository) FindByID(ctx context.Context, id string) (*models.DeploymentLog, error) {
	var log models.DeploymentLog
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&log).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &log, nil
}

// Update writes all columns of a deployment log, only while the stored log
// is running (conditional UPDATE ... WHERE id = ? AND status = 'running').
// When no running row matches it changes nothing and returns
// models.ErrDeployLogNotRunning: the leader can have ended the operation
// (interrupted operation recovery), and a late writer must not overwrite
// that result. Save is not used: it inserts a row when the update matches
// nothing.
func (r *GORMDeploymentLogRepository) Update(ctx context.Context, log *models.DeploymentLog) error {
	res := r.db.WithContext(ctx).Model(&models.DeploymentLog{}).
		Where("id = ? AND status = ?", log.ID, models.DeployLogRunning).
		Select("*").Omit("id").
		Updates(log)
	if res.Error != nil {
		return dberrors.NewDatabaseError("update", res.Error)
	}
	if res.RowsAffected > 0 {
		return nil
	}
	// MySQL counts only changed rows: an update without a change of a
	// running log also gives 0 rows.
	var running int64
	if err := r.db.WithContext(ctx).Model(&models.DeploymentLog{}).
		Where("id = ? AND status = ?", log.ID, models.DeployLogRunning).
		Count(&running).Error; err != nil {
		return dberrors.NewDatabaseError("update", err)
	}
	if running > 0 {
		return nil
	}
	return models.ErrDeployLogNotRunning
}

// ExtendDeadline sets deadline_at to deadline when the log is running and
// its deadline is earlier (or not set). It returns
// models.ErrDeployLogNotRunning when the log is no longer running (for
// example the leader ended it while the operation waited for its slot).
func (r *GORMDeploymentLogRepository) ExtendDeadline(ctx context.Context, id string, deadline time.Time) error {
	res := r.db.WithContext(ctx).Model(&models.DeploymentLog{}).
		Where("id = ? AND status = ? AND (deadline_at IS NULL OR deadline_at < ?)", id, models.DeployLogRunning, deadline.UTC()).
		Update("deadline_at", deadline.UTC())
	if res.Error != nil {
		return dberrors.NewDatabaseError("extend_deadline", res.Error)
	}
	if res.RowsAffected > 0 {
		return nil
	}
	// No change: the deadline is already later, or the log is not running.
	var running int64
	if err := r.db.WithContext(ctx).Model(&models.DeploymentLog{}).
		Where("id = ? AND status = ?", id, models.DeployLogRunning).
		Count(&running).Error; err != nil {
		return dberrors.NewDatabaseError("extend_deadline", err)
	}
	if running == 0 {
		return models.ErrDeployLogNotRunning
	}
	return nil
}

// ListByInstance returns all deployment logs for a given stack instance, ordered by started_at descending.
func (r *GORMDeploymentLogRepository) ListByInstance(ctx context.Context, instanceID string) ([]models.DeploymentLog, error) {
	var logs []models.DeploymentLog
	if err := r.db.WithContext(ctx).Where("stack_instance_id = ?", instanceID).
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
func (r *GORMDeploymentLogRepository) ListByInstancePaginated(ctx context.Context, filters models.DeploymentLogFilters) (*models.DeploymentLogResult, error) {
	query := r.db.WithContext(ctx).Model(&models.DeploymentLog{}).Where("stack_instance_id = ?", filters.InstanceID)

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
// Uses RawURLEncoding so the cursor is safe for use as a query parameter.
func encodeDeployLogCursor(ts time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(ts.UTC().Format(time.RFC3339Nano) + "|" + id),
	)
}

// decodeDeployLogCursor extracts a timestamp and ID from an opaque cursor.
func decodeDeployLogCursor(cursor string) (time.Time, string, error) {
	data, err := base64.RawURLEncoding.DecodeString(cursor)
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
func (r *GORMDeploymentLogRepository) GetLatestByInstance(ctx context.Context, instanceID string) (*models.DeploymentLog, error) {
	var log models.DeploymentLog
	if err := r.db.WithContext(ctx).Where("stack_instance_id = ?", instanceID).
		Order("started_at DESC").
		First(&log).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("get_latest", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("get_latest", err)
	}
	return &log, nil
}

// ListLatestByActions returns up to limit logs of the instance whose action
// is one of actions, newest first. It selects only the small columns (the
// index idx_deployment_logs_instance_action_started covers the filter).
func (r *GORMDeploymentLogRepository) ListLatestByActions(ctx context.Context, instanceID string, actions []string, limit int) ([]models.DeploymentLog, error) {
	if limit <= 0 {
		limit = 1
	}
	var logs []models.DeploymentLog
	if err := r.db.WithContext(ctx).
		Select("id, stack_instance_id, action, status, started_at, completed_at, target_log_id, branch").
		Where("stack_instance_id = ? AND action IN ?", instanceID, actions).
		Order("started_at DESC").
		Limit(limit).
		Find(&logs).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_latest_by_actions", err)
	}
	return logs, nil
}

// SummarizeByInstance returns aggregate deployment statistics for a given stack instance.
// Only logs from the last 90 days are considered.
func (r *GORMDeploymentLogRepository) SummarizeByInstance(ctx context.Context, instanceID string) (*models.DeployLogSummary, error) {
	cutoff := time.Now().UTC().Add(-90 * 24 * time.Hour)

	summary := &models.DeployLogSummary{InstanceID: instanceID}

	// Count deploy actions and their statuses.
	row := r.db.WithContext(ctx).Model(&models.DeploymentLog{}).
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
	// Scan into sql.NullString to handle both MySQL (time.Time via parseTime)
	// and SQLite (returns string for computed datetime expressions).
	var lastDeployRaw sql.NullString
	row2 := r.db.WithContext(ctx).Model(&models.DeploymentLog{}).
		Select("MAX(COALESCE(completed_at, started_at))").
		Where("stack_instance_id = ? AND started_at >= ?", instanceID, cutoff).
		Row()
	if err := row2.Err(); err != nil {
		return nil, dberrors.NewDatabaseError("summarize_by_instance", err)
	}
	if err := row2.Scan(&lastDeployRaw); err != nil {
		return nil, dberrors.NewDatabaseError("summarize_by_instance", err)
	}
	if lastDeployRaw.Valid && lastDeployRaw.String != "" {
		layouts := []string{
			"2006-01-02 15:04:05.999999999+00:00", // SQLite with timezone
			"2006-01-02 15:04:05.999999999",       // SQLite without timezone
			"2006-01-02 15:04:05",                 // MySQL datetime
			time.RFC3339Nano,
			time.RFC3339,
		}
		for _, layout := range layouts {
			if parsed, pErr := time.Parse(layout, lastDeployRaw.String); pErr == nil {
				utc := parsed.UTC()
				summary.LastDeployAt = &utc
				break
			}
		}
	}

	return summary, nil
}

// SummarizeBatch returns aggregate deployment statistics for multiple instances
// in a single query, eliminating N+1 query patterns. Only logs from the last
// 90 days are considered. Instance IDs are processed in chunks of 500 to stay
// within MySQL's IN clause limits.
func (r *GORMDeploymentLogRepository) SummarizeBatch(ctx context.Context, instanceIDs []string) (map[string]*models.DeployLogSummary, error) {
	result := make(map[string]*models.DeployLogSummary, len(instanceIDs))
	if len(instanceIDs) == 0 {
		return result, nil
	}

	cutoff := time.Now().UTC().Add(-90 * 24 * time.Hour)

	const chunkSize = 500
	for start := 0; start < len(instanceIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(instanceIDs) {
			end = len(instanceIDs)
		}
		chunk := instanceIDs[start:end]

		if err := r.summarizeBatchChunk(ctx, chunk, cutoff, result); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// SummarizeByUsers returns deploy statistics per user who started the deploy
// (DeploymentLog.UserID), over all deploy logs and also for deleted instances.
// User IDs are processed in chunks of 500 to stay within MySQL's IN clause
// limits. Users without deploys are not in the result.
func (r *GORMDeploymentLogRepository) SummarizeByUsers(ctx context.Context, userIDs []string) (map[string]*models.DeployLogSummary, error) {
	result := make(map[string]*models.DeployLogSummary, len(userIDs))
	const chunkSize = 500
	for start := 0; start < len(userIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(userIDs) {
			end = len(userIDs)
		}
		var rows []summarizeByUserRow
		err := r.db.WithContext(ctx).Model(&models.DeploymentLog{}).
			Select(`user_id,
				COUNT(*) as deploy_count,
				COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) as success_count,
				COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) as error_count,
				MAX(COALESCE(completed_at, started_at)) as last_deploy`,
				models.DeployLogSuccess, models.DeployLogError).
			Where("user_id IN ? AND action = ?", userIDs[start:end], models.DeployActionDeploy).
			Group("user_id").
			Find(&rows).Error
		if err != nil {
			return nil, dberrors.NewDatabaseError("summarize_by_users", err)
		}
		for _, row := range rows {
			summary := &models.DeployLogSummary{
				UserID:       row.UserID,
				DeployCount:  row.DeployCount,
				SuccessCount: row.SuccessCount,
				ErrorCount:   row.ErrorCount,
			}
			if t, ok := parseAggregateTime(row.LastDeploy); ok {
				summary.LastDeployAt = &t
			}
			result[row.UserID] = summary
		}
	}
	return result, nil
}

// summarizeByUserRow holds the raw scan results of SummarizeByUsers.
type summarizeByUserRow struct {
	UserID       string
	DeployCount  int
	SuccessCount int
	ErrorCount   int
	LastDeploy   sql.NullString
}

// parseAggregateTime parses the result of MAX() over a datetime column,
// scanned as text. MySQL (parseTime=true) returns a time.Time, which
// database/sql converts to RFC 3339 text; SQLite returns its own text format.
func parseAggregateTime(v sql.NullString) (time.Time, bool) {
	if !v.Valid {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999"} {
		if t, err := time.Parse(layout, v.String); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// summarizeBatchRow holds the raw scan results from the batch summary query.
// Uses sql.NullTime for LastDeploy — MySQL with parseTime=true returns
// time.Time natively for computed datetime columns.
type summarizeBatchRow struct {
	InstanceID   string
	DeployCount  int
	SuccessCount int
	ErrorCount   int
	LastDeploy   sql.NullTime
}

// summarizeBatchChunk executes the batch summary query for a single chunk of
// instance IDs and merges results into the provided map.
func (r *GORMDeploymentLogRepository) summarizeBatchChunk(
	ctx context.Context,
	chunk []string,
	cutoff time.Time,
	result map[string]*models.DeployLogSummary,
) error {
	var rows []summarizeBatchRow
	err := r.db.WithContext(ctx).Model(&models.DeploymentLog{}).
		Select(`stack_instance_id as instance_id,
			COUNT(CASE WHEN action = ? THEN 1 END) as deploy_count,
			COALESCE(SUM(CASE WHEN action = ? AND status = ? THEN 1 ELSE 0 END), 0) as success_count,
			COALESCE(SUM(CASE WHEN action = ? AND status = ? THEN 1 ELSE 0 END), 0) as error_count,
			MAX(COALESCE(completed_at, started_at)) as last_deploy`,
			models.DeployActionDeploy,
			models.DeployActionDeploy, models.DeployLogSuccess,
			models.DeployActionDeploy, models.DeployLogError).
		Where("stack_instance_id IN ? AND started_at >= ?", chunk, cutoff).
		Group("stack_instance_id").
		Find(&rows).Error
	if err != nil {
		return dberrors.NewDatabaseError("summarize_batch", err)
	}

	for _, row := range rows {
		summary := &models.DeployLogSummary{
			InstanceID:   row.InstanceID,
			DeployCount:  row.DeployCount,
			SuccessCount: row.SuccessCount,
			ErrorCount:   row.ErrorCount,
		}
		if row.LastDeploy.Valid {
			utc := row.LastDeploy.Time.UTC()
			summary.LastDeployAt = &utc
		}
		result[row.InstanceID] = summary
	}

	return nil
}

// ListRecentGlobal returns the most recent deployment log entries across all
// instances, enriched with instance name and owner username via a 3-table JOIN.
func (r *GORMDeploymentLogRepository) ListRecentGlobal(ctx context.Context, limit int) ([]models.DeploymentLogWithContext, error) {
	if limit <= 0 {
		limit = 10
	}

	type row struct {
		models.DeploymentLog
		InstanceName  string `gorm:"column:instance_name"`
		OwnerUsername string `gorm:"column:owner_username"`
	}

	var rows []row
	err := r.db.WithContext(ctx).
		Table("deployment_logs dl").
		Select("dl.id, dl.stack_instance_id, dl.action, dl.status, dl.branch, dl.started_at, dl.completed_at, dl.error_message, COALESCE(si.name, '(deleted)') AS instance_name, COALESCE(u.username, '(unknown)') AS owner_username").
		Joins("LEFT JOIN stack_instances si ON dl.stack_instance_id = si.id").
		Joins("LEFT JOIN users u ON si.owner_id = u.id").
		Order("dl.started_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, dberrors.NewDatabaseError("list_recent_global", err)
	}

	result := make([]models.DeploymentLogWithContext, len(rows))
	for i, r := range rows {
		result[i] = models.DeploymentLogWithContext{
			DeploymentLog: r.DeploymentLog,
			InstanceName:  r.InstanceName,
			OwnerUsername: r.OwnerUsername,
		}
	}
	return result, nil
}

// CountByAction returns the total number of deployment log entries with the
// given action within the last 90 days (matching the SummarizeByInstance cutoff).
func (r *GORMDeploymentLogRepository) CountByAction(ctx context.Context, action string) (int, error) {
	cutoff := time.Now().UTC().Add(-90 * 24 * time.Hour)
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.DeploymentLog{}).
		Where("action = ? AND started_at >= ?", action, cutoff).
		Count(&count).Error; err != nil {
		return 0, dberrors.NewDatabaseError("count_by_action", err)
	}
	return int(count), nil
}
