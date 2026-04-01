package database

import (
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

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, dberrors.NewDatabaseError("count", err)
	}

	limit := filters.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := filters.Offset

	var logs []models.AuditLog
	if err := query.Order("timestamp DESC").Limit(limit).Offset(offset).Find(&logs).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}

	return &models.AuditLogResult{
		Data:  logs,
		Total: total,
	}, nil
}
