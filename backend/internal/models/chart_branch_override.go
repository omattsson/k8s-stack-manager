package models

import "time"

// ChartBranchOverride holds a per-chart branch override for a stack instance.
// When set, the chart uses this branch instead of the instance-level Branch.
type ChartBranchOverride struct {
	ID              string    `json:"id" gorm:"primaryKey;size:36"`
	StackInstanceID string    `json:"stack_instance_id" gorm:"size:36;uniqueIndex:idx_instance_chart"`
	ChartConfigID   string    `json:"chart_config_id" gorm:"size:36;uniqueIndex:idx_instance_chart"`
	Branch          string    `json:"branch" gorm:"size:255"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ChartBranchOverrideRepository defines data access operations for per-chart branch overrides.
type ChartBranchOverrideRepository interface {
	List(instanceID string) ([]*ChartBranchOverride, error)
	Get(instanceID, chartConfigID string) (*ChartBranchOverride, error)
	Set(override *ChartBranchOverride) error
	Delete(instanceID, chartConfigID string) error
	DeleteByInstance(instanceID string) error
}
