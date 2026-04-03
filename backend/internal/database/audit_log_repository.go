package database

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Compile-time interface check.
var _ models.AuditLogRepository = (*GORMAuditLogRepository)(nil)

// GORMAuditLogRepository implements models.AuditLogRepository using GORM.
type GORMAuditLogRepository struct {
	db *gorm.DB
}

// NewGORMAuditLogRepository creates a new GORM-backed audit log repository.
func NewGORMAuditLogRepository(db *gorm.DB) *GORMAuditLogRepository {
	return &GORMAuditLogRepository{db: db}
}

// Create inserts a new audit log record.
func (r *GORMAuditLogRepository) Create(log *models.AuditLog) error {
	if log.ID == "" {
		log.ID = uuid.New().String()
	}
	if log.Timestamp.IsZero() {
		log.Timestamp = time.Now().UTC()
	}
	if err := r.db.Create(log).Error; err != nil {
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// List returns audit logs matching the provided filters with pagination.
// When filters.Cursor is set, keyset (cursor-based) pagination is used for
// efficient traversal of large datasets. Otherwise, traditional OFFSET
// pagination is used for backward compatibility.
func (r *GORMAuditLogRepository) List(filters models.AuditLogFilters) (*models.AuditLogResult, error) {
	query := r.db.Model(&models.AuditLog{})

	if filters.Action != "" {
		query = query.Where("action = ?", filters.Action)
	}
	if filters.EntityType != "" {
		query = query.Where("entity_type = ?", filters.EntityType)
	}
	if filters.EntityID != "" {
		query = query.Where("entity_id = ?", filters.EntityID)
	}
	if filters.UserID != "" {
		query = query.Where("user_id = ?", filters.UserID)
	}
	if filters.StartDate != nil {
		query = query.Where("timestamp >= ?", *filters.StartDate)
	}
	if filters.EndDate != nil {
		query = query.Where("timestamp <= ?", *filters.EndDate)
	}

	limit := filters.Limit
	if limit <= 0 {
		limit = 50
	}

	// Cursor-based pagination: skip COUNT and OFFSET, use keyset filtering.
	if filters.Cursor != "" {
		cursorTS, cursorID, err := decodeAuditCursor(filters.Cursor)
		if err != nil {
			return nil, dberrors.NewDatabaseError("list", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
		}
		// Keyset condition for DESC ordering: rows with earlier timestamp,
		// or same timestamp but lexicographically smaller ID (tie-breaker).
		query = query.Where(
			"(timestamp < ?) OR (timestamp = ? AND id < ?)",
			cursorTS, cursorTS, cursorID,
		)

		// Fetch limit+1 to detect whether a next page exists.
		var logs []models.AuditLog
		if err := query.Order("timestamp DESC, id DESC").Limit(limit + 1).Find(&logs).Error; err != nil {
			return nil, dberrors.NewDatabaseError("list", err)
		}

		result := &models.AuditLogResult{
			Total: -1, // exact total not computed in cursor mode
		}
		if len(logs) > limit {
			logs = logs[:limit]
			last := logs[limit-1]
			result.NextCursor = encodeAuditCursor(last.Timestamp, last.ID)
		}
		result.Data = logs
		return result, nil
	}

	// Traditional OFFSET pagination.
	// Use an estimated count for large unfiltered result sets to avoid expensive
	// full table scans on this append-only table. When any filter is active the
	// result set is bounded, so exact count is acceptable.
	hasFilters := filters.Action != "" || filters.EntityType != "" ||
		filters.EntityID != "" || filters.UserID != "" ||
		filters.StartDate != nil || filters.EndDate != nil

	var total int64
	if hasFilters {
		if err := query.Count(&total).Error; err != nil {
			return nil, dberrors.NewDatabaseError("count", err)
		}
	} else {
		// For unfiltered queries, try MySQL table stats estimate (~O(1)) instead
		// of COUNT(*). Falls back to exact count for non-MySQL dialects (SQLite).
		estimated := false
		row := r.db.Raw("SELECT table_rows FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'audit_logs'").Row()
		if row.Err() == nil {
			if err := row.Scan(&total); err == nil && total > 0 {
				estimated = true
			}
		}
		if !estimated {
			if err := query.Count(&total).Error; err != nil {
				return nil, dberrors.NewDatabaseError("count", err)
			}
		}
	}

	var logs []models.AuditLog
	if err := query.Order("timestamp DESC, id DESC").Limit(limit).Offset(filters.Offset).Find(&logs).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}

	return &models.AuditLogResult{
		Data:  logs,
		Total: total,
	}, nil
}

// encodeAuditCursor creates an opaque cursor from a timestamp and ID.
// Uses RawURLEncoding so the cursor is safe for use as a query parameter.
func encodeAuditCursor(ts time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(ts.UTC().Format(time.RFC3339Nano) + "|" + id),
	)
}

// decodeAuditCursor extracts a timestamp and ID from an opaque cursor.
func decodeAuditCursor(cursor string) (time.Time, string, error) {
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
