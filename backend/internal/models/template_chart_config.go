package models

import "time"

// TemplateChartConfig represents a chart configuration within a stack template.
type TemplateChartConfig struct {
	ID              string    `json:"id"`
	StackTemplateID string    `json:"stack_template_id"`
	ChartName       string    `json:"chart_name"`
	RepositoryURL   string    `json:"repository_url"`
	SourceRepoURL   string    `json:"source_repo_url"`
	ChartPath       string    `json:"chart_path"`
	ChartVersion    string    `json:"chart_version"`
	DefaultValues   string    `json:"default_values"`
	LockedValues    string    `json:"locked_values"`
	DeployOrder     int       `json:"deploy_order"`
	Required        bool      `json:"required"`
	CreatedAt       time.Time `json:"created_at"`
}

// TemplateChartConfigRepository defines data access operations for template chart configs.
type TemplateChartConfigRepository interface {
	Create(config *TemplateChartConfig) error
	FindByID(id string) (*TemplateChartConfig, error)
	Update(config *TemplateChartConfig) error
	Delete(id string) error
	ListByTemplate(templateID string) ([]TemplateChartConfig, error)
}
