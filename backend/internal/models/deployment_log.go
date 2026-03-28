package models

import (
	"context"
	"time"
)

// DeploymentLog records the output and status of a deployment operation.
type DeploymentLog struct {
	StartedAt       time.Time  `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	ID              string     `json:"id"`
	StackInstanceID string     `json:"stack_instance_id"`
	Action          string     `json:"action"` // "deploy", "stop", or "clean"
	Status          string     `json:"status"` // "running", "success", "error"
	Output          string     `json:"output"`
	ErrorMessage    string     `json:"error_message,omitempty"`
}

// Deployment log action constants.
const (
	DeployActionDeploy = "deploy"
	DeployActionStop   = "stop"
	DeployActionClean  = "clean"
)

// Deployment log status constants.
const (
	DeployLogRunning = "running"
	DeployLogSuccess = "success"
	DeployLogError   = "error"
)

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
	GetLatestByInstance(ctx context.Context, instanceID string) (*DeploymentLog, error)
	SummarizeByInstance(ctx context.Context, instanceID string) (*DeployLogSummary, error)
}
