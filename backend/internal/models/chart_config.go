package models

import "time"

// ChartConfig represents a Helm chart configuration within a stack definition.
type ChartConfig struct {
	ID                string    `json:"id" gorm:"primaryKey;size:36"`
	StackDefinitionID string    `json:"stack_definition_id" gorm:"size:36"`
	ChartName         string    `json:"chart_name" gorm:"size:255"`
	RepositoryURL     string    `json:"repository_url" gorm:"size:500"`
	SourceRepoURL     string    `json:"source_repo_url" gorm:"size:500"`
	BuildPipelineID   string    `json:"build_pipeline_id" gorm:"size:100"` // CI pipeline ID to trigger for image builds
	ChartPath         string    `json:"chart_path" gorm:"size:500"`
	ChartVersion      string    `json:"chart_version" gorm:"size:50"`
	DefaultValues     string    `json:"default_values" gorm:"type:longtext"`
	DeployOrder       int       `json:"deploy_order"`
	CreatedAt         time.Time `json:"created_at"`
}

// ChartConfigRepository defines data access operations for chart configs.
type ChartConfigRepository interface {
	Create(config *ChartConfig) error
	FindByID(id string) (*ChartConfig, error)
	Update(config *ChartConfig) error
	Delete(id string) error
	ListByDefinition(definitionID string) ([]ChartConfig, error)
}
