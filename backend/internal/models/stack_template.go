package models

import "time"

// StackTemplate represents a DevOps-managed reusable stack recipe.
type StackTemplate struct {
	ID            string    `json:"id" gorm:"primaryKey;size:36"`
	Name          string    `json:"name" gorm:"size:255"`
	Description   string    `json:"description" gorm:"type:text"`
	Category      string    `json:"category" gorm:"size:100"`
	Version       string    `json:"version" gorm:"size:50"`
	OwnerID       string    `json:"owner_id" gorm:"size:36"`
	DefaultBranch string    `json:"default_branch" gorm:"size:255"`
	IsPublished   bool      `json:"is_published"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// StackTemplateRepository defines data access operations for stack templates.
type StackTemplateRepository interface {
	Create(template *StackTemplate) error
	FindByID(id string) (*StackTemplate, error)
	// FindByIDForUpdate reads the template and locks its row until the end of
	// the transaction (SELECT ... FOR UPDATE). Publish uses it.
	FindByIDForUpdate(id string) (*StackTemplate, error)
	Update(template *StackTemplate) error
	Delete(id string) error
	List() ([]StackTemplate, error)
	// ListPaged returns a page of templates; a non-empty name filters by
	// exact name.
	ListPaged(limit, offset int, name string) ([]StackTemplate, int64, error)
	ListPublished() ([]StackTemplate, error)
	// ListPublishedPaged is ListPaged for published templates only.
	ListPublishedPaged(limit, offset int, name string) ([]StackTemplate, int64, error)
	ListByOwner(ownerID string) ([]StackTemplate, error)
	Count() (int64, error)
}
