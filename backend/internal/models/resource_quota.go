package models

import (
	"context"
	"time"
)

// ResourceQuotaConfig defines resource limits applied to namespaces in a cluster.
type ResourceQuotaConfig struct {
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	ID            string    `json:"id" gorm:"primaryKey;size:36"`
	ClusterID     string    `json:"cluster_id" gorm:"size:36;uniqueIndex;not null"`
	CPURequest    string    `json:"cpu_request" gorm:"size:20"`
	CPULimit      string    `json:"cpu_limit" gorm:"size:20"`
	MemoryRequest string    `json:"memory_request" gorm:"size:20"`
	MemoryLimit   string    `json:"memory_limit" gorm:"size:20"`
	StorageLimit  string    `json:"storage_limit" gorm:"size:20"`
	PodLimit      int       `json:"pod_limit"`
}

// ResourceQuotaRepository defines data access operations for resource quota configs.
type ResourceQuotaRepository interface {
	GetByClusterID(ctx context.Context, clusterID string) (*ResourceQuotaConfig, error)
	Upsert(ctx context.Context, config *ResourceQuotaConfig) error
	Delete(ctx context.Context, clusterID string) error
}
