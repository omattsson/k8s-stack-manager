package models

import "time"

// CleanupPolicy defines an automated maintenance policy for stack instances.
type CleanupPolicy struct {
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	ClusterID string     `json:"cluster_id"` // or "all" for all clusters
	Action    string     `json:"action"`     // "stop", "clean", "delete"
	Condition string     `json:"condition"`  // e.g. "idle_days:7", "status:stopped,age_days:14", "ttl_expired"
	Schedule  string     `json:"schedule"`   // Cron expression, e.g. "0 2 * * *"
	Enabled   bool       `json:"enabled"`
	DryRun    bool       `json:"dry_run"` // If true, only report matches without acting
}

// CleanupPolicyRepository defines data access operations for cleanup policies.
type CleanupPolicyRepository interface {
	Create(policy *CleanupPolicy) error
	FindByID(id string) (*CleanupPolicy, error)
	Update(policy *CleanupPolicy) error
	Delete(id string) error
	List() ([]CleanupPolicy, error)
	ListEnabled() ([]CleanupPolicy, error)
}
