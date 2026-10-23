package models

import (
	"context"
	"time"
)

// TemplateChartConfig represents a chart configuration within a stack template.
type TemplateChartConfig struct {
	ID              string    `json:"id" gorm:"primaryKey;size:36"`
	StackTemplateID string    `json:"stack_template_id" gorm:"size:36"`
	ChartName       string    `json:"chart_name" gorm:"size:255"`
	RepositoryURL   string    `json:"repository_url" gorm:"size:500"`
	SourceRepoURL   string    `json:"source_repo_url" gorm:"size:500"`
	BuildPipelineID string    `json:"build_pipeline_id" gorm:"size:100"` // CI pipeline ID to trigger for image builds
	ChartPath       string    `json:"chart_path" gorm:"size:500"`
	ChartVersion    string    `json:"chart_version" gorm:"size:50"`
	DefaultValues   string    `json:"default_values" gorm:"type:longtext"`
	LockedValues    string    `json:"locked_values" gorm:"type:longtext"`
	DeployOrder     int       `json:"deploy_order"`
	Required        bool      `json:"required"`
	CreatedAt       time.Time `json:"created_at"`
}

// TemplateChartConfigRepository defines data access operations for template chart configs.
type TemplateChartConfigRepository interface {
	Create(ctx context.Context, config *TemplateChartConfig) error
	FindByID(ctx context.Context, id string) (*TemplateChartConfig, error)
	Update(ctx context.Context, config *TemplateChartConfig) error
	Delete(ctx context.Context, id string) error
	ListByTemplate(ctx context.Context, templateID string) ([]TemplateChartConfig, error)
}
