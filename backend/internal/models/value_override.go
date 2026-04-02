package models

import "time"

// ValueOverride holds per-chart value overrides for a stack instance.
type ValueOverride struct {
	ID              string    `json:"id" gorm:"primaryKey;size:36"`
	StackInstanceID string    `json:"stack_instance_id" gorm:"size:36;uniqueIndex:idx_override_instance_chart"`
	ChartConfigID   string    `json:"chart_config_id" gorm:"size:36;uniqueIndex:idx_override_instance_chart"`
	Values          string    `json:"values" gorm:"type:longtext"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ValueOverrideRepository defines data access operations for value overrides.
type ValueOverrideRepository interface {
	Create(override *ValueOverride) error
	FindByID(id string) (*ValueOverride, error)
	FindByInstanceAndChart(instanceID, chartConfigID string) (*ValueOverride, error)
	Update(override *ValueOverride) error
	Delete(id string) error
	ListByInstance(instanceID string) ([]ValueOverride, error)
}
