package models

import "time"

// StackInstance represents a deployed instance of a stack definition.
type StackInstance struct {
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	LastDeployedAt    *time.Time `json:"last_deployed_at,omitempty"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	ID                string     `json:"id"`
	StackDefinitionID string     `json:"stack_definition_id"`
	Name              string     `json:"name"`
	Namespace         string     `json:"namespace"`
	OwnerID           string     `json:"owner_id"`
	Branch            string     `json:"branch"`
	ClusterID         string     `json:"cluster_id,omitempty"`
	Status            string     `json:"status"`
	ErrorMessage      string     `json:"error_message,omitempty"`
	TTLMinutes        int        `json:"ttl_minutes"`
}

// Valid stack instance statuses.
const (
	StackStatusDraft     = "draft"
	StackStatusQueued    = "queued"
	StackStatusDeploying = "deploying"
	StackStatusRunning   = "running"
	StackStatusStopping  = "stopping"
	StackStatusStopped   = "stopped"
	StackStatusCleaning  = "cleaning"
	StackStatusError     = "error"
)

// StackInstanceRepository defines data access operations for stack instances.
type StackInstanceRepository interface {
	Create(instance *StackInstance) error
	FindByID(id string) (*StackInstance, error)
	FindByNamespace(namespace string) (*StackInstance, error)
	Update(instance *StackInstance) error
	Delete(id string) error
	List() ([]StackInstance, error)
	ListPaged(limit, offset int) ([]StackInstance, int, error)
	ListByOwner(ownerID string) ([]StackInstance, error)
	FindByCluster(clusterID string) ([]StackInstance, error)
	CountByClusterAndOwner(clusterID, ownerID string) (int, error)
	CountAll() (int, error)
	CountByStatus(status string) (int, error)
	ExistsByDefinitionAndStatus(definitionID, status string) (bool, error)
	ListExpired() ([]*StackInstance, error)
}
