package models

import (
	"context"
	"time"
)

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
	Create(ctx context.Context, config *ChartConfig) error
	FindByID(ctx context.Context, id string) (*ChartConfig, error)
	Update(ctx context.Context, config *ChartConfig) error
	Delete(ctx context.Context, id string) error
	ListByDefinition(ctx context.Context, definitionID string) ([]ChartConfig, error)
	// CountByDefinitionIDs returns the number of chart configs per definition
	// in one query. Definitions without charts are not in the map.
	CountByDefinitionIDs(ctx context.Context, definitionIDs []string) (map[string]int, error)
	// DeleteByDefinition deletes all chart configs of a definition (no error
	// when there are none).
	DeleteByDefinition(ctx context.Context, definitionID string) error
}
