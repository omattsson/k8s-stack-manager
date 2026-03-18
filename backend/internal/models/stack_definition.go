package models

import "time"

// StackDefinition represents a user's stack configuration.
type StackDefinition struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	Description           string    `json:"description"`
	OwnerID               string    `json:"owner_id"`
	SourceTemplateID      string    `json:"source_template_id,omitempty"`
	SourceTemplateVersion string    `json:"source_template_version,omitempty"`
	DefaultBranch         string    `json:"default_branch"`
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
	ListByOwner(ownerID string) ([]StackDefinition, error)
	ListByTemplate(templateID string) ([]StackDefinition, error)
}
