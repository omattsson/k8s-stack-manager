package models

import "time"

// StackInstance represents a deployed instance of a stack definition.
type StackInstance struct {
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	LastDeployedAt     *time.Time `json:"last_deployed_at,omitempty"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
	ID                 string     `json:"id" gorm:"primaryKey;size:36"`
	StackDefinitionID  string     `json:"stack_definition_id" gorm:"size:36"`
	Name               string     `json:"name" gorm:"size:255"`
	Namespace          string     `json:"namespace" gorm:"size:255"`
	OwnerID            string     `json:"owner_id" gorm:"size:36"`
	Branch             string     `json:"branch" gorm:"size:255"`
	ClusterID          string     `json:"cluster_id,omitempty" gorm:"size:36"`
	Status             string     `json:"status" gorm:"size:50"`
	ErrorMessage       string     `json:"error_message,omitempty" gorm:"type:text"`
	LastDeployedValues string     `json:"-" gorm:"type:longtext"`
	TTLMinutes         int        `json:"ttl_minutes"`
}

// Valid stack instance statuses.
const (
	StackStatusDraft       = "draft"
	StackStatusQueued      = "queued"
	StackStatusDeploying   = "deploying"
	StackStatusStabilizing = "stabilizing"
	StackStatusRunning     = "running"
	StackStatusStopping    = "stopping"
	StackStatusStopped     = "stopped"
	StackStatusCleaning    = "cleaning"
	StackStatusPartial     = "partial"
	StackStatusError       = "error"
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
	FindByName(name string) ([]StackInstance, error)
	FindByCluster(clusterID string) ([]StackInstance, error)
	CountByClusterAndOwner(clusterID, ownerID string) (int, error)
	CountAll() (int, error)
	CountByStatus(status string) (int, error)
	CountByDefinitionIDs(definitionIDs []string) (map[string]int, error)
	CountByOwnerIDs(ownerIDs []string) (map[string]int, error)
	ListIDsByDefinitionIDs(definitionIDs []string) (map[string][]string, error)
	ListIDsByOwnerIDs(ownerIDs []string) (map[string][]string, error)
	ExistsByDefinitionAndStatus(definitionID, status string) (bool, error)
	ListExpired() ([]*StackInstance, error)
	ListExpiringSoon(threshold time.Duration) ([]*StackInstance, error)
	ListByStatus(status string, limit int) ([]*StackInstance, error)
}
