package models

import (
	"context"
	"time"
)

// MaxAuditEntityIDLen is the size of audit_logs.entity_id: the RFC 1123
// label maximum (a namespace name). A UUID has 36 characters.
const MaxAuditEntityIDLen = 63

// Sizes of the audit_logs context columns (#785, #787).
const (
	MaxAuditEntityNameLen = 255
	MaxAuditClientIPLen   = 45 // IPv6 text form
	MaxAuditRequestIDLen  = 64
)

// Audit auth methods that are not request auth methods. Requests carry
// "jwt" or "api_key" (middleware.AuthMethodJWT, middleware.AuthMethodAPIKey).
const (
	AuditAuthSystem   = "system"   // background jobs
	AuditAuthPassword = "password" // local login
	AuditAuthOIDC     = "oidc"     // OIDC sign-in
	// AuditAuthRefreshToken: a public auth route that the refresh-token
	// cookie authenticates (refresh-token reuse, logout without a bearer).
	AuditAuthRefreshToken = "refresh_token"
)

// AuditLog records a user action for auditing purposes.
type AuditLog struct {
	ID         string    `json:"id" gorm:"primaryKey;size:36"`
	UserID     string    `json:"user_id" gorm:"size:36"`
	Username   string    `json:"username" gorm:"size:100"`
	Action     string    `json:"action" gorm:"size:100"`
	EntityType string    `json:"entity_type" gorm:"size:100"`
	EntityID   string    `json:"entity_id" gorm:"size:63"` // 63: RFC 1123 label (namespace name); a UUID has 36
	Details    string    `json:"details" gorm:"type:longtext"`
	Timestamp  time.Time `json:"timestamp"`
	EntityName string    `json:"entity_name,omitempty" gorm:"size:255"`
	ClientIP   string    `json:"client_ip,omitempty" gorm:"size:45"`
	RequestID  string    `json:"request_id,omitempty" gorm:"size:64"`
	TraceID    string    `json:"trace_id,omitempty" gorm:"size:32"`
	AuthMethod string    `json:"auth_method,omitempty" gorm:"size:16"`
	APIKeyID   string    `json:"api_key_id,omitempty" gorm:"size:36"`
}

// AuditChange is one changed field in the "changes" of an audit entry (#786).
// Old and New are set only for fields that are safe to show; Redacted marks a
// secret field that changed.
type AuditChange struct {
	Field    string `json:"field"`
	Old      any    `json:"old,omitempty"`
	New      any    `json:"new,omitempty"`
	Redacted bool   `json:"redacted,omitempty"`
}

// AuditLogFilters holds optional filters for querying audit logs.
type AuditLogFilters struct {
	StartDate  *time.Time
	EndDate    *time.Time
	UserID     string
	Username   string
	APIKeyID   string
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
	Create(ctx context.Context, log *AuditLog) error
	List(ctx context.Context, filters AuditLogFilters) (*AuditLogResult, error)
}
