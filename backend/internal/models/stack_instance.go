package models

import (
	"context"
	"errors"
	"time"
)

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
	StoppedAt *time.Time `json:"stopped_at,omitempty"`
	// CleanedAt is the time the last successful clean finished. It is nil
	// when the instance was not cleaned since its last deploy. A deploy
	// clears it. Cleanup policies use it for the cleaned_days condition (a
	// clean also clears LastDeployedAt, so idle_days cannot count from it).
	CleanedAt          *time.Time `json:"cleaned_at,omitempty"`
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

	// DeleteAfterClean is true while a clean runs as the first step of a
	// delete: the deploy manager deletes the row when the clean succeeds.
	// Only MarkCleanStart and ClearDeleteAfterClean write it (Update does
	// not), so a concurrent Update cannot set or clear it. A failed clean
	// and the recovery of an interrupted operation clear it. A plain clean
	// does not start while it is set.
	DeleteAfterClean bool `json:"-" gorm:"not null;default:false"`

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

// CleanStatuses are the statuses of an instance with cluster resources: a
// clean can start, and a delete cleans it first (Helm uninstall and
// namespace delete).
var CleanStatuses = []string{StackStatusRunning, StackStatusPartial, StackStatusStopped, StackStatusError}

// DeleteCleanFailedMessage starts the error message of an instance whose
// clean for a delete failed.
const DeleteCleanFailedMessage = "Clean failed; the stack was not deleted."

// ErrDeleteConflict is returned by MarkCleanStart for a delete when the
// instance is not in one of the allowed statuses any more (for example a
// second delete started the clean first).
var ErrDeleteConflict = errors.New("another operation changed the instance")

// ErrCleanConflict is returned by MarkCleanStart for a plain clean when the
// instance is not in one of the allowed statuses any more, or a delete
// already marked it (delete_after_clean).
var ErrCleanConflict = errors.New("another operation changed the instance")

// CleanStartMarker is implemented by stack instance repositories that start
// a clean with a conditional update. The deploy manager uses it when the
// repository implements it.
type CleanStartMarker interface {
	// MarkCleanStart sets status cleaning and an empty error message when
	// the instance has one of fromStatuses. With deleteAfterClean it also
	// sets delete_after_clean (else ErrDeleteConflict when no row matched).
	// Without it, the row must not have delete_after_clean (else
	// ErrCleanConflict), so a plain clean never overwrites a delete.
	MarkCleanStart(ctx context.Context, id string, fromStatuses []string, deleteAfterClean bool) error
	// ClearDeleteAfterClean clears delete_after_clean.
	ClearDeleteAfterClean(ctx context.Context, id string) error
}

// InstanceErrorMarker is implemented by stack instance repositories that set
// the error status of the k8s status watcher with a conditional update. The
// watcher reads the instances first and writes later. With a plain update it
// could overwrite the status of an operation that started in between (for
// example a deploy). The watcher uses it when the repository implements it.
type InstanceErrorMarker interface {
	// MarkNamespaceError sets status error and the error message when the
	// status is running or partial, or stabilizing without active blocking
	// post-deploy hooks (post_deploy_hook_until empty or before now). It
	// reports whether the row changed.
	MarkNamespaceError(ctx context.Context, id, message string, now time.Time) (bool, error)
}

// InstanceStatusMarker is implemented by stack instance repositories that
// change the status with a conditional update. The deploy manager uses it
// for the status stabilizing: a stop that commits between its ownership
// check and the write keeps its status.
type InstanceStatusMarker interface {
	// MarkStatus sets status to to when the stored status is one of from.
	// It reports whether the stored status is to afterwards.
	MarkStatus(ctx context.Context, id, to string, from []string) (bool, error)
}

// InstanceNameLookup is implemented by stack instance repositories that can
// look up instance names on a cluster. UNIQUE_INSTANCE_NAMES needs it.
type InstanceNameLookup interface {
	// FindByNameOnCluster returns the instances that use name on clusterID.
	// It compares the names without case. With includeUnassigned it also
	// returns the instances without a cluster (they use the default cluster).
	FindByNameOnCluster(ctx context.Context, name, clusterID string, includeUnassigned bool) ([]StackInstance, error)
	// DuplicateInstanceNames returns the names that more than one instance
	// uses on the same cluster (compared without case). Instances without a
	// cluster count as defaultClusterID.
	DuplicateInstanceNames(ctx context.Context, defaultClusterID string) ([]DuplicateInstanceName, error)
}

// DuplicateInstanceName is a name that Count instances use on one cluster.
type DuplicateInstanceName struct {
	ClusterID string
	Name      string
	Count     int
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
	Create(ctx context.Context, instance *StackInstance) error
	FindByID(ctx context.Context, id string) (*StackInstance, error)
	// FindByIDs returns the instances with these IDs that exist, keyed by ID,
	// in one query. The rows are projected: only ID, OwnerID, Status,
	// Namespace and ClusterID are set (enough for access checks).
	FindByIDs(ctx context.Context, ids []string) (map[string]*StackInstance, error)
	FindByNamespace(ctx context.Context, namespace string) (*StackInstance, error)
	// NamespaceShared reports whether an instance other than id has this
	// namespace. Rows from before the namespace became immutable can share
	// one; the port-forward route refuses such a namespace.
	NamespaceShared(ctx context.Context, namespace, id string) (bool, error)
	Update(ctx context.Context, instance *StackInstance) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]StackInstance, error)
	// ListPaged returns one page of the instances that match filter, newest
	// first, and the total number of matching instances.
	ListPaged(ctx context.Context, filter StackInstanceFilter, limit, offset int) ([]StackInstance, int, error)
	ListByOwner(ctx context.Context, ownerID string) ([]StackInstance, error)
	FindByName(ctx context.Context, name string) ([]StackInstance, error)
	FindByCluster(ctx context.Context, clusterID string) ([]StackInstance, error)
	// CountForInstanceLimit counts the instances of ownerID on clusterID; with
	// includeUnassigned also those without a cluster (they run on the default
	// cluster). It first locks the user row of ownerID (FOR UPDATE), so
	// concurrent creates of one owner run one after the other. Call it as the
	// first statement of the create transaction (#682).
	CountForInstanceLimit(ctx context.Context, clusterID, ownerID string, includeUnassigned bool) (int, error)
	CountAll(ctx context.Context) (int, error)
	CountByStatus(ctx context.Context, status string) (int, error)
	CountByStatuses(ctx context.Context, statuses []string) (int, error)
	CountByDefinitionIDs(ctx context.Context, definitionIDs []string) (map[string]int, error)
	CountByOwnerIDs(ctx context.Context, ownerIDs []string) (map[string]int, error)
	ListIDsByDefinitionIDs(ctx context.Context, definitionIDs []string) (map[string][]string, error)
	ListIDsByOwnerIDs(ctx context.Context, ownerIDs []string) (map[string][]string, error)
	ExistsByDefinitionAndStatus(ctx context.Context, definitionID, status string) (bool, error)
	ListExpired(ctx context.Context) ([]*StackInstance, error)
	// ListExpiringSoon returns running or partial instances that expire
	// within threshold and have no expiry warning yet.
	ListExpiringSoon(ctx context.Context, threshold time.Duration) ([]*StackInstance, error)
	// MarkExpiryWarned sets ExpiryWarnedAt to warnedAt when the instance has
	// no expiry warning and its ExpiresAt is still expiresAt. It reports
	// whether it changed the row: only one caller gets true.
	MarkExpiryWarned(ctx context.Context, id string, expiresAt, warnedAt time.Time) (bool, error)
	ListByStatus(ctx context.Context, status string, limit int) ([]*StackInstance, error)
}
