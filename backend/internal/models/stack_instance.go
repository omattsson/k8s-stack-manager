package models

import "time"

// StackInstance represents a deployed instance of a stack definition.
type StackInstance struct {
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastDeployedAt *time.Time `json:"last_deployed_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	// ExpiryWarnedAt is the time the TTL expiry warning for the current
	// ExpiresAt was sent; nil when no warning was sent. Only the expiry
	// warner sets it (MarkExpiryWarned). Update never writes it, and an
	// Update that changes ExpiresAt clears it.
	ExpiryWarnedAt *time.Time `json:"-"`
	// StoppedAt is the time the last successful stop finished. It is nil
	// when the instance is not stopped. A deploy or a clean clears it.
	// Cleanup policies use it for the stopped_days condition.
	StoppedAt          *time.Time `json:"stopped_at,omitempty"`
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

	// PostDeployHookUntil is set while blocking post-deploy hooks run: the
	// latest end of the wait (start + the sum of the hook timeouts). The k8s
	// status watcher (on the leader replica) does not set error for a
	// stabilizing instance before this time. A deploy clears it. It expires
	// by itself after a crash of the deploying replica.
	PostDeployHookUntil *time.Time `json:"-"`

	// ValuesDrift is computed, not stored. GET, PUT and POST .../extend on
	// /stack-instances/{id} set it to true when the running values come from
	// a successful rollback and the stored overrides produce different values
	// (the next deploy undoes the rollback). List responses do not compute it.
	ValuesDrift bool `json:"values_drift,omitempty" gorm:"-"`
	// The name fields below are computed, not stored. The API sets them in
	// list and detail responses (and in the create, update, clone and
	// extend responses) with one batch lookup per kind and response.

	// OwnerUsername is the username of the owner. Omitted when the owner
	// no longer exists (for example, after a delete).
	OwnerUsername string `json:"owner_username,omitempty" gorm:"-" readonly:"true"`
	// DefinitionName is the name of the stack definition. Omitted when the
	// definition no longer exists.
	DefinitionName string `json:"definition_name,omitempty" gorm:"-" readonly:"true"`
	// ClusterName is the name of the target cluster. Omitted when the
	// cluster no longer exists or cluster_id is empty (older instances).
	ClusterName string `json:"cluster_name,omitempty" gorm:"-" readonly:"true"`
	// Following and FollowerCount are computed, not stored. GET, PUT and
	// POST .../extend on /stack-instances/{id} set them: following is true
	// when the caller follows the instance, follower_count is the number of
	// followers. List responses do not set them.
	Following     *bool  `json:"following,omitempty" gorm:"-" readonly:"true"`
	FollowerCount *int64 `json:"follower_count,omitempty" gorm:"-" readonly:"true"`
}

// StackInstanceFilter selects the stack instances that ListPaged returns.
// An empty field does not filter. The handler resolves "me" and usernames
// to OwnerID before it calls the repository.
type StackInstanceFilter struct {
	Name         string
	Status       string
	ClusterID    string
	DefinitionID string
	OwnerID      string
}

// IsValidStackStatus reports whether status is a known stack instance status.
func IsValidStackStatus(status string) bool {
	switch status {
	case StackStatusDraft, StackStatusQueued, StackStatusDeploying, StackStatusStabilizing,
		StackStatusRunning, StackStatusStopping, StackStatusStopped, StackStatusCleaning,
		StackStatusPartial, StackStatusError:
		return true
	}
	return false
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
	// ListPaged returns one page of the instances that match filter, newest
	// first, and the total number of matching instances.
	ListPaged(filter StackInstanceFilter, limit, offset int) ([]StackInstance, int, error)
	ListByOwner(ownerID string) ([]StackInstance, error)
	FindByName(name string) ([]StackInstance, error)
	FindByCluster(clusterID string) ([]StackInstance, error)
	CountByClusterAndOwner(clusterID, ownerID string) (int, error)
	CountAll() (int, error)
	CountByStatus(status string) (int, error)
	CountByStatuses(statuses []string) (int, error)
	CountByDefinitionIDs(definitionIDs []string) (map[string]int, error)
	CountByOwnerIDs(ownerIDs []string) (map[string]int, error)
	ListIDsByDefinitionIDs(definitionIDs []string) (map[string][]string, error)
	ListIDsByOwnerIDs(ownerIDs []string) (map[string][]string, error)
	ExistsByDefinitionAndStatus(definitionID, status string) (bool, error)
	ListExpired() ([]*StackInstance, error)
	// ListExpiringSoon returns running or partial instances that expire
	// within threshold and have no expiry warning yet.
	ListExpiringSoon(threshold time.Duration) ([]*StackInstance, error)
	// MarkExpiryWarned sets ExpiryWarnedAt to warnedAt when the instance has
	// no expiry warning and its ExpiresAt is still expiresAt. It reports
	// whether it changed the row: only one caller gets true.
	MarkExpiryWarned(id string, expiresAt, warnedAt time.Time) (bool, error)
	ListByStatus(status string, limit int) ([]*StackInstance, error)
}
