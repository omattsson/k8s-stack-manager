package models

import "time"

// TemplateChartConfig represents a chart configuration within a stack template.
type TemplateChartConfig struct {
	ID              string    `json:"id" gorm:"primaryKey;size:36"`
	StackTemplateID string    `json:"stack_template_id" gorm:"size:36"`
	ChartName       string    `json:"chart_name" gorm:"size:255"`
	RepositoryURL   string    `json:"repository_url" gorm:"size:500"`
	SourceRepoURL   string    `json:"source_repo_url" gorm:"size:500"`
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
	Create(config *TemplateChartConfig) error
	FindByID(id string) (*TemplateChartConfig, error)
	Update(config *TemplateChartConfig) error
	Delete(id string) error
	ListByTemplate(templateID string) ([]TemplateChartConfig, error)
}
