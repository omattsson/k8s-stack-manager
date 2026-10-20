package models

import (
	"context"
	"time"
)

// Page sizes of the action run list.
const (
	DefaultActionRunPageSize = 20
	MaxActionRunPageSize     = 100
)

// ActionRun records one call of a custom action on a stack instance (#640).
// Parameters holds only the declared parameters (JSON object); a sensitive
// parameter holds "[redacted]". The row never holds the subscriber URL, the
// result body or the secret.
type ActionRun struct {
	// StartedAt is the time before the subscriber call.
	StartedAt time.Time `json:"started_at" gorm:"not null;index:idx_action_runs_instance_started,priority:2;index:idx_action_runs_started"`
	// FinishedAt is the answer time of a run without a job, or the time the
	// job log proxy saw the final job status. Nil while a job has no stored
	// final status.
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ID         string     `json:"id" gorm:"primaryKey;size:36;index:idx_action_runs_instance_started,priority:3"`
	InstanceID string     `json:"instance_id" gorm:"size:36;not null;index:idx_action_runs_instance_started,priority:1;index:idx_action_runs_instance_job,priority:1"`
	Action     string     `json:"action" gorm:"size:255;not null"`
	UserID     string     `json:"user_id" gorm:"size:36;not null"`
	// mediumtext: 20 declared strings of 1024 bytes can exceed 64 KiB as
	// escaped JSON.
	Parameters string `json:"-" gorm:"type:mediumtext"`
	Message    string `json:"message,omitempty" gorm:"size:500;not null;default:''"`
	JobID      string `json:"job_id,omitempty" gorm:"size:128;not null;default:'';index:idx_action_runs_instance_job,priority:2"`
	JobStatus  string `json:"job_status,omitempty" gorm:"size:32;not null;default:''"`
	RequestID  string `json:"request_id,omitempty" gorm:"size:64;not null;default:''"`
	// StartedBy is the username of UserID (response only).
	StartedBy string `json:"started_by,omitempty" gorm:"-"`
	// StatusCode is the HTTP status of the subscriber; 0 means no answer.
	StatusCode int `json:"status_code" gorm:"size:32;not null;default:0"`
}

// ActionRunRepository defines data access operations for action runs.
type ActionRunRepository interface {
	// Create inserts run. It sets ID when empty and StartedAt when zero.
	Create(ctx context.Context, run *ActionRun) error
	// ListByInstance returns the runs of the instance, newest first
	// (started_at DESC, id DESC). limit <= 0 uses DefaultActionRunPageSize;
	// a larger limit than MaxActionRunPageSize counts as the maximum.
	// nextCursor is empty on the last page. A bad cursor gives ErrValidation.
	ListByInstance(ctx context.Context, instanceID, cursor string, limit int) (runs []ActionRun, nextCursor string, err error)
	// SetJobFinalStatus stores the final job status of the run with jobID,
	// only when no final status is stored yet (one conditional UPDATE).
	// updated is false when no row changed.
	SetJobFinalStatus(ctx context.Context, instanceID, action, jobID, status string, finishedAt time.Time) (updated bool, err error)
	// DeleteByInstance removes all runs of the instance.
	DeleteByInstance(ctx context.Context, instanceID string) error
}
