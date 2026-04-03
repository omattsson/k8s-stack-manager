package models

import "time"

// StackDefinition represents a user's stack configuration.
type StackDefinition struct {
	ID                    string    `json:"id" gorm:"primaryKey;size:36"`
	Name                  string    `json:"name" gorm:"size:255"`
	Description           string    `json:"description" gorm:"type:text"`
	OwnerID               string    `json:"owner_id" gorm:"size:36"`
	SourceTemplateID      string    `json:"source_template_id,omitempty" gorm:"size:36"`
	SourceTemplateVersion string    `json:"source_template_version,omitempty" gorm:"size:50"`
	DefaultBranch         string    `json:"default_branch" gorm:"size:255"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// StackDefinitionRepository defines data access operations for stack definitions.
type StackDefinitionRepository interface {
	Create(definition *StackDefinition) error
	FindByID(id string) (*StackDefinition, error)
	Update(definition *StackDefinition) error
	Delete(id string) error
	List() ([]StackDefinition, error)
	ListPaged(limit, offset int) ([]StackDefinition, int64, error)
	ListByOwner(ownerID string) ([]StackDefinition, error)
	ListByTemplate(templateID string) ([]StackDefinition, error)
	CountByTemplateIDs(templateIDs []string) (map[string]int, error)
	Count() (int64, error)
}
