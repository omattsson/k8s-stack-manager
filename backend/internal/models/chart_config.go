package models

import "time"

// ChartConfig represents a Helm chart configuration within a stack definition.
type ChartConfig struct {
	ID                string    `json:"id"`
	StackDefinitionID string    `json:"stack_definition_id"`
	ChartName         string    `json:"chart_name"`
	RepositoryURL     string    `json:"repository_url"`
	SourceRepoURL     string    `json:"source_repo_url"`
	ChartPath         string    `json:"chart_path"`
	ChartVersion      string    `json:"chart_version"`
	DefaultValues     string    `json:"default_values"`
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
