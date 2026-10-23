package models

import (
	"context"
	"time"
)

// CleanupPolicy defines an automated maintenance policy for stack instances.
type CleanupPolicy struct {
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	ID        string     `json:"id" gorm:"primaryKey;size:36"`
	Name      string     `json:"name" gorm:"size:255"`
	ClusterID string     `json:"cluster_id" gorm:"size:36"`  // or "all" for all clusters
	Action    string     `json:"action" gorm:"size:50"`      // "stop", "clean", "delete"
	Condition string     `json:"condition" gorm:"type:text"` // e.g. "idle_days:7", "status:stopped,age_days:14", "stopped_days:3", "ttl_expired", "status:running,expires_within_hours:12"
	Schedule  string     `json:"schedule" gorm:"size:100"`   // Cron expression, e.g. "0 2 * * *"
	Enabled   bool       `json:"enabled"`
	DryRun    bool       `json:"dry_run"` // If true, only report matches without acting
}

// CleanupPolicyRepository defines data access operations for cleanup policies.
type CleanupPolicyRepository interface {
	Create(ctx context.Context, policy *CleanupPolicy) error
	FindByID(ctx context.Context, id string) (*CleanupPolicy, error)
	Update(ctx context.Context, policy *CleanupPolicy) error
	Delete(ctx context.Context, id string) error
	// SetLastRunAt writes last_run_at only, so a concurrent edit of the other fields stays.
	SetLastRunAt(ctx context.Context, id string, t time.Time) error
	List(ctx context.Context) ([]CleanupPolicy, error)
	ListEnabled(ctx context.Context) ([]CleanupPolicy, error)
}
