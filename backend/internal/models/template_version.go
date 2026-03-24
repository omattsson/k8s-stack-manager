package models

import (
	"context"
	"time"
)

// TemplateVersion represents a versioned snapshot of a stack template at the time of publish.
type TemplateVersion struct {
	ID            string    `json:"id" gorm:"primaryKey;size:36"`
	TemplateID    string    `json:"template_id" gorm:"size:36;index;not null"`
	Version       string    `json:"version" gorm:"size:50;not null"`
	Snapshot      string    `json:"-" gorm:"type:longtext;not null"`
	ChangeSummary string    `json:"change_summary" gorm:"type:text"`
	CreatedBy     string    `json:"created_by" gorm:"size:100"`
	CreatedAt     time.Time `json:"created_at"`
}

// TemplateSnapshot is the serialized structure stored in the Snapshot field.
type TemplateSnapshot struct {
	Template TemplateSnapshotData        `json:"template"`
	Charts   []TemplateChartSnapshotData `json:"charts"`
}

// TemplateSnapshotData holds the template fields captured at publish time.
type TemplateSnapshotData struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Category      string `json:"category"`
	DefaultBranch string `json:"default_branch"`
	IsPublished   bool   `json:"is_published"`
	Version       string `json:"version"`
}

// TemplateChartSnapshotData holds the chart config fields captured at publish time.
type TemplateChartSnapshotData struct {
	ChartName     string `json:"chart_name"`
	RepoURL       string `json:"repo_url"`
	DefaultValues string `json:"default_values"`
	LockedValues  string `json:"locked_values"`
	IsRequired    bool   `json:"is_required"`
	SortOrder     int    `json:"sort_order"`
}

// TemplateVersionRepository defines data access operations for template versions.
type TemplateVersionRepository interface {
	Create(ctx context.Context, version *TemplateVersion) error
	ListByTemplate(ctx context.Context, templateID string) ([]TemplateVersion, error)
	GetByID(ctx context.Context, templateID, id string) (*TemplateVersion, error)
	GetLatestByTemplate(ctx context.Context, templateID string) (*TemplateVersion, error)
}
