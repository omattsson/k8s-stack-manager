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
	DeployCount  int
	SuccessCount int
	ErrorCount   int
}

// DeploymentLogRepository defines data access operations for deployment logs.
type DeploymentLogRepository interface {
	Create(ctx context.Context, log *DeploymentLog) error
	FindByID(ctx context.Context, id string) (*DeploymentLog, error)
	Update(ctx context.Context, log *DeploymentLog) error
	ListByInstance(ctx context.Context, instanceID string) ([]DeploymentLog, error)
	ListByInstancePaginated(ctx context.Context, filters DeploymentLogFilters) (*DeploymentLogResult, error)
	GetLatestByInstance(ctx context.Context, instanceID string) (*DeploymentLog, error)
	SummarizeByInstance(ctx context.Context, instanceID string) (*DeployLogSummary, error)
	SummarizeBatch(ctx context.Context, instanceIDs []string) (map[string]*DeployLogSummary, error)
	CountByAction(ctx context.Context, action string) (int, error)
}
