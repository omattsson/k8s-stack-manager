package models

import (
	"context"
	"time"
)

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
	// OwnerUsername is the username of the owner. It is computed, not
	// stored; the API sets it in list and detail responses. Omitted when
	// the owner no longer exists (for example, after a delete).
	OwnerUsername string `json:"owner_username,omitempty" gorm:"-" readonly:"true"`
}

// StackTemplateRepository defines data access operations for stack templates.
type StackTemplateRepository interface {
	Create(ctx context.Context, template *StackTemplate) error
	FindByID(ctx context.Context, id string) (*StackTemplate, error)
	// FindByIDForUpdate reads the template and locks its row until the end of
	// the transaction (SELECT ... FOR UPDATE). Publish uses it.
	FindByIDForUpdate(ctx context.Context, id string) (*StackTemplate, error)
	Update(ctx context.Context, template *StackTemplate) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]StackTemplate, error)
	// ListPaged returns a page of templates; a non-empty name filters by
	// exact name.
	ListPaged(ctx context.Context, limit, offset int, name string) ([]StackTemplate, int64, error)
	ListPublished(ctx context.Context) ([]StackTemplate, error)
	// ListPublishedPaged is ListPaged for published templates only.
	ListPublishedPaged(ctx context.Context, limit, offset int, name string) ([]StackTemplate, int64, error)
	ListByOwner(ctx context.Context, ownerID string) ([]StackTemplate, error)
	Count(ctx context.Context) (int64, error)
}
