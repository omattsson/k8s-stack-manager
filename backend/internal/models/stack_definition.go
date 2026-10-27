package models

import (
	"context"
	"time"
)

// StackDefinition represents a user's stack configuration.
type StackDefinition struct {
	ID                    string `json:"id" gorm:"primaryKey;size:36"`
	Name                  string `json:"name" gorm:"size:255"`
	Description           string `json:"description" gorm:"type:text"`
	OwnerID               string `json:"owner_id" gorm:"size:36"`
	SourceTemplateID      string `json:"source_template_id,omitempty" gorm:"size:36"`
	SourceTemplateVersion string `json:"source_template_version,omitempty" gorm:"size:50"`
	DefaultBranch         string `json:"default_branch" gorm:"size:255"`
	// OwnerInstanceID is set when quick deploy created the definition for one
	// instance. Deleting that instance also deletes the definition when no
	// other instance uses it. Empty for definitions that users create.
	OwnerInstanceID string    `json:"owner_instance_id,omitempty" gorm:"size:36;index"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	// OwnerUsername is the username of the owner. It is computed, not
	// stored; the API sets it in list and detail responses. Omitted when
	// the owner no longer exists (for example, after a delete).
	OwnerUsername string `json:"owner_username,omitempty" gorm:"-" readonly:"true"`
	// ChartCount is the number of charts. It is computed, not stored; the
	// paged list sets it (the list does not load the charts).
	ChartCount *int `json:"chart_count,omitempty" gorm:"-" readonly:"true"`
}

// StackDefinitionFilter selects the stack definitions that ListPaged
// returns. An empty field does not filter.
type StackDefinitionFilter struct {
	Name    string
	OwnerID string
}

// DefinitionTemplateRef is the template of a stack definition
// (TemplateRefsByIDs).
type DefinitionTemplateRef struct {
	TemplateID       string
	TemplateName     string
	TemplateCategory string
}

// StackDefinitionRepository defines data access operations for stack definitions.
type StackDefinitionRepository interface {
	Create(ctx context.Context, definition *StackDefinition) error
	FindByID(ctx context.Context, id string) (*StackDefinition, error)
	// FindByIDForUpdate is FindByID with a row lock (SELECT ... FOR UPDATE).
	// Use it inside a transaction.
	FindByIDForUpdate(ctx context.Context, id string) (*StackDefinition, error)
	FindByName(ctx context.Context, name string) ([]StackDefinition, error)
	Update(ctx context.Context, definition *StackDefinition) error
	// SetTemplateLink writes only source_template_id,
	// source_template_version and updated_at, so a concurrent full-row
	// update of other fields is not lost. Empty values remove the link.
	SetTemplateLink(ctx context.Context, id, templateID, version string) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]StackDefinition, error)
	// ListPaged returns one page of the definitions that match filter,
	// newest first, and the total number of matching definitions.
	ListPaged(ctx context.Context, filter StackDefinitionFilter, limit, offset int) ([]StackDefinition, int64, error)
	// NamesByIDs returns the name of each definition in ids that exists,
	// keyed by ID, in one query.
	NamesByIDs(ctx context.Context, ids []string) (map[string]string, error)
	// TemplateRefsByIDs returns the template of each definition in ids whose
	// source_template_id names an existing template, keyed by definition ID,
	// in one query. A definition without a template is missing.
	TemplateRefsByIDs(ctx context.Context, ids []string) (map[string]DefinitionTemplateRef, error)
	ListByOwner(ctx context.Context, ownerID string) ([]StackDefinition, error)
	ListByTemplate(ctx context.Context, templateID string) ([]StackDefinition, error)
	CountByTemplateIDs(ctx context.Context, templateIDs []string) (map[string]int, error)
	ListIDsByTemplateIDs(ctx context.Context, templateIDs []string) (map[string][]string, error)
	Count(ctx context.Context) (int64, error)
}
