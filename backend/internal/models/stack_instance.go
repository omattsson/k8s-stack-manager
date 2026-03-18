package models

import "time"

// StackInstance represents a deployed instance of a stack definition.
type StackInstance struct {
	ID                string    `json:"id"`
	StackDefinitionID string    `json:"stack_definition_id"`
	Name              string    `json:"name"`
	Namespace         string    `json:"namespace"`
	OwnerID           string    `json:"owner_id"`
	Branch            string    `json:"branch"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Valid stack instance statuses.
const (
	StackStatusDraft     = "draft"
	StackStatusDeploying = "deploying"
	StackStatusRunning   = "running"
	StackStatusStopped   = "stopped"
	StackStatusError     = "error"
)

// StackInstanceRepository defines data access operations for stack instances.
type StackInstanceRepository interface {
	Create(instance *StackInstance) error
	FindByID(id string) (*StackInstance, error)
	Update(instance *StackInstance) error
	Delete(id string) error
	List() ([]StackInstance, error)
	ListByOwner(ownerID string) ([]StackInstance, error)
}
