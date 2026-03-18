package models

import "time"

// StackTemplate represents a DevOps-managed reusable stack recipe.
type StackTemplate struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Category      string    `json:"category"`
	Version       string    `json:"version"`
	OwnerID       string    `json:"owner_id"`
	DefaultBranch string    `json:"default_branch"`
	IsPublished   bool      `json:"is_published"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// StackTemplateRepository defines data access operations for stack templates.
type StackTemplateRepository interface {
	Create(template *StackTemplate) error
	FindByID(id string) (*StackTemplate, error)
	Update(template *StackTemplate) error
	Delete(id string) error
	List() ([]StackTemplate, error)
	ListPublished() ([]StackTemplate, error)
	ListByOwner(ownerID string) ([]StackTemplate, error)
}
