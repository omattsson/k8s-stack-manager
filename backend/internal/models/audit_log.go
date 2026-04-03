package models

import "time"

// AuditLog records a user action for auditing purposes.
type AuditLog struct {
	ID         string    `json:"id" gorm:"primaryKey;size:36"`
	UserID     string    `json:"user_id" gorm:"size:36"`
	Username   string    `json:"username" gorm:"size:100"`
	Action     string    `json:"action" gorm:"size:100"`
	EntityType string    `json:"entity_type" gorm:"size:100"`
	EntityID   string    `json:"entity_id" gorm:"size:36"`
	Details    string    `json:"details" gorm:"type:longtext"`
	Timestamp  time.Time `json:"timestamp"`
}

// AuditLogFilters holds optional filters for querying audit logs.
type AuditLogFilters struct {
	StartDate  *time.Time
	EndDate    *time.Time
	UserID     string
	EntityType string
	EntityID   string
	Action     string
	Cursor     string
	Limit      int
	Offset     int
}

// PaginatedAuditLogs wraps a page of audit log results with pagination metadata.
type PaginatedAuditLogs struct {
	Data       []AuditLog `json:"data"`
	Total      int64      `json:"total"`
	Limit      int        `json:"limit"`
	Offset     int        `json:"offset"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

// AuditLogResult holds the result of an audit log list query including cursor pagination metadata.
type AuditLogResult struct {
	Data       []AuditLog
	Total      int64
	NextCursor string
}

// AuditLogRepository defines data access operations for audit logs.
type AuditLogRepository interface {
	Create(log *AuditLog) error
	List(filters AuditLogFilters) (*AuditLogResult, error)
}
