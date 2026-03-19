package models

import "time"

// DeploymentLog records the output and status of a deployment operation.
type DeploymentLog struct {
	StartedAt       time.Time  `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	ID              string     `json:"id"`
	StackInstanceID string     `json:"stack_instance_id"`
	Action          string     `json:"action"` // "deploy" or "stop"
	Status          string     `json:"status"` // "running", "success", "error"
	Output          string     `json:"output"`
	ErrorMessage    string     `json:"error_message,omitempty"`
}

// Deployment log action constants.
const (
	DeployActionDeploy = "deploy"
	DeployActionStop   = "stop"
)

// Deployment log status constants.
const (
	DeployLogRunning = "running"
	DeployLogSuccess = "success"
	DeployLogError   = "error"
)

// DeploymentLogRepository defines data access operations for deployment logs.
type DeploymentLogRepository interface {
	Create(log *DeploymentLog) error
	FindByID(id string) (*DeploymentLog, error)
	Update(log *DeploymentLog) error
	ListByInstance(instanceID string) ([]DeploymentLog, error)
	GetLatestByInstance(instanceID string) (*DeploymentLog, error)
}
