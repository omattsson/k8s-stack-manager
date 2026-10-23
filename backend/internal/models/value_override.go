package models

import (
	"context"
	"time"
)

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
	Create(ctx context.Context, override *ValueOverride) error
	FindByID(ctx context.Context, id string) (*ValueOverride, error)
	FindByInstanceAndChart(ctx context.Context, instanceID, chartConfigID string) (*ValueOverride, error)
	Update(ctx context.Context, override *ValueOverride) error
	Delete(ctx context.Context, id string) error
	ListByInstance(ctx context.Context, instanceID string) ([]ValueOverride, error)
	// DeleteByInstance deletes all value overrides of an instance (no error
	// when there are none).
	DeleteByInstance(ctx context.Context, instanceID string) error
}
