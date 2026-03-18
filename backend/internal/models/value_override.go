package models

import "time"

// ValueOverride holds per-chart value overrides for a stack instance.
type ValueOverride struct {
	ID              string    `json:"id"`
	StackInstanceID string    `json:"stack_instance_id"`
	ChartConfigID   string    `json:"chart_config_id"`
	Values          string    `json:"values"`
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
