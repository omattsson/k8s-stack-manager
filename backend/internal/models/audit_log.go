package models

import "time"

// AuditLog records a user action for auditing purposes.
type AuditLog struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	Username   string    `json:"username"`
	Action     string    `json:"action"`
	EntityType string    `json:"entity_type"`
	EntityID   string    `json:"entity_id"`
	Details    string    `json:"details"`
	Timestamp  time.Time `json:"timestamp"`
}

// AuditLogFilters holds optional filters for querying audit logs.
type AuditLogFilters struct {
	UserID     string
	EntityType string
	EntityID   string
	Action     string
	StartDate  *time.Time
	EndDate    *time.Time
}

// AuditLogRepository defines data access operations for audit logs.
type AuditLogRepository interface {
	Create(log *AuditLog) error
	List(filters AuditLogFilters) ([]AuditLog, error)
}
