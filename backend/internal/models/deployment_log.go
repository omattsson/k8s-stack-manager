package models

import (
	"context"
	"time"
)

// DeploymentLog records the output and status of a deployment operation.
type DeploymentLog struct {
	StartedAt       time.Time  `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	ID              string     `json:"id" gorm:"primaryKey;size:36"`
	StackInstanceID string     `json:"stack_instance_id" gorm:"size:36"`
	Action          string     `json:"action" gorm:"size:50"` // "deploy", "stop", "clean", "rollback"
	Status          string     `json:"status" gorm:"size:50"` // "running", "success", "error"
	Output          string     `json:"output" gorm:"type:longtext"`
	ErrorMessage    string     `json:"error_message,omitempty" gorm:"type:text"`
	ValuesSnapshot  string     `json:"values_snapshot,omitempty" gorm:"type:longtext"`
	TargetLogID     string     `json:"target_log_id,omitempty" gorm:"size:36"`
	// Branch is the instance branch of a deploy, or the branch of the target
	// deploy for a rollback to a target. Empty for older logs and for stop
	// and clean logs.
	Branch string `json:"branch,omitempty" gorm:"size:255"`
	// ChartVersions is a JSON object chart name -> chart version that a
	// deploy used. A rollback to this deploy installs the same versions.
	// Empty for logs written before the column existed.
	ChartVersions string `json:"chart_versions,omitempty" gorm:"type:text"`
	// UserID is the user who started a deploy. Per-user analytics count
	// deploys by this field, so the history stays after the instance is
	// deleted. Empty for system deploys and for other actions. Migration 49
	// sets it for older deploy logs to the owner of the instance and adds
	// the composite index idx_deployment_logs_user_action (user_id, action,
	// started_at, completed_at, status) for SummarizeByUsers.
	UserID string `json:"user_id,omitempty" gorm:"size:36"`
}

// Deployment log action constants.
const (
	DeployActionDeploy   = "deploy"
	DeployActionStop     = "stop"
	DeployActionClean    = "clean"
	DeployActionRollback = "rollback"
)

// Deployment log status constants.
const (
	DeployLogRunning = "running"
	DeployLogSuccess = "success"
	DeployLogError   = "error"
)

// DeploymentLogFilters holds optional filters and pagination for querying deployment logs.
type DeploymentLogFilters struct {
	InstanceID string
	Cursor     string
	Limit      int
	Offset     int
}

// DeploymentLogResult holds the result of a paginated deployment log query.
type DeploymentLogResult struct {
	Data       []DeploymentLog `json:"data"`
	Total      int64           `json:"total"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// DeployLogSummary provides lightweight aggregate counts for an instance's
// deployment logs, avoiding the need to fetch full log entities with their
// potentially large Output and Details fields.
type DeployLogSummary struct {
	LastDeployAt *time.Time
	InstanceID   string
	// UserID is set by SummarizeByUsers; InstanceID stays empty there.
	UserID       string
	DeployCount  int
	SuccessCount int
	ErrorCount   int
}

// DeploymentLogWithContext extends DeploymentLog with denormalized instance
// and user fields, populated via JOIN to avoid N+1 queries.
type DeploymentLogWithContext struct {
	DeploymentLog
	InstanceName  string `json:"instance_name"`
	OwnerUsername string `json:"owner_username"`
}

// DeploymentLogRepository defines data access operations for deployment logs.
type DeploymentLogRepository interface {
	Create(ctx context.Context, log *DeploymentLog) error
	FindByID(ctx context.Context, id string) (*DeploymentLog, error)
	Update(ctx context.Context, log *DeploymentLog) error
	ListByInstance(ctx context.Context, instanceID string) ([]DeploymentLog, error)
	ListByInstancePaginated(ctx context.Context, filters DeploymentLogFilters) (*DeploymentLogResult, error)
	GetLatestByInstance(ctx context.Context, instanceID string) (*DeploymentLog, error)
	// ListLatestByActions returns up to limit logs of the instance whose
	// action is one of actions, newest first (by started_at). Only the small
	// columns are loaded (no output, values snapshot or chart versions).
	ListLatestByActions(ctx context.Context, instanceID string, actions []string, limit int) ([]DeploymentLog, error)
	SummarizeByInstance(ctx context.Context, instanceID string) (*DeployLogSummary, error)
	SummarizeBatch(ctx context.Context, instanceIDs []string) (map[string]*DeployLogSummary, error)
	// SummarizeByUsers returns deploy statistics per user (DeploymentLog.UserID)
	// over all deploy logs, also of deleted instances. Users without deploys
	// are not in the map.
	SummarizeByUsers(ctx context.Context, userIDs []string) (map[string]*DeployLogSummary, error)
	CountByAction(ctx context.Context, action string) (int, error)
	ListRecentGlobal(ctx context.Context, limit int) ([]DeploymentLogWithContext, error)
}
