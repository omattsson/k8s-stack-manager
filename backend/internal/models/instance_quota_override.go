package models

import (
	"context"
	"time"
)

// InstanceQuotaOverride defines per-instance resource quota overrides that take
// precedence over the cluster-wide ResourceQuotaConfig defaults.
type InstanceQuotaOverride struct {
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ID              string    `json:"id" gorm:"primaryKey;size:36"`
	StackInstanceID string    `json:"stack_instance_id" gorm:"size:36;uniqueIndex;not null"`
	CPURequest      string    `json:"cpu_request" gorm:"size:20"`
	CPULimit        string    `json:"cpu_limit" gorm:"size:20"`
	MemoryRequest   string    `json:"memory_request" gorm:"size:20"`
	MemoryLimit     string    `json:"memory_limit" gorm:"size:20"`
	StorageLimit    string    `json:"storage_limit" gorm:"size:20"`
	PodLimit        *int      `json:"pod_limit"`
}

// InstanceQuotaOverrideRepository defines data access for per-instance quota overrides.
type InstanceQuotaOverrideRepository interface {
	GetByInstanceID(ctx context.Context, instanceID string) (*InstanceQuotaOverride, error)
	Upsert(ctx context.Context, override *InstanceQuotaOverride) error
	Delete(ctx context.Context, instanceID string) error
}
