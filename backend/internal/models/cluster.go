package models

import (
	"context"
	"time"
)

// Cluster health status constants.
const (
	ClusterHealthy     = "healthy"
	ClusterDegraded    = "degraded"
	ClusterUnreachable = "unreachable"
)

// Cluster represents a Kubernetes cluster that stack instances can be deployed to.
type Cluster struct {
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ID             string    `json:"id" gorm:"primaryKey;size:36"`
	Name           string    `json:"name" gorm:"size:255;uniqueIndex"`
	Description    string    `json:"description" gorm:"type:text"`
	APIServerURL   string    `json:"api_server_url" gorm:"size:500"`
	KubeconfigData string    `json:"-" gorm:"type:longtext"`
	KubeconfigPath string    `json:"-" gorm:"size:500"`
	Region         string    `json:"region" gorm:"size:100"`
	HealthStatus   string    `json:"health_status" gorm:"size:50"`
	// Registry fields for automatic image pull secret provisioning.
	// When RegistryURL is non-empty, a docker-registry secret is created/refreshed
	// in each stack namespace before chart installs.
	RegistryURL      string `json:"registry_url" gorm:"size:500"`
	RegistryUsername string `json:"registry_username" gorm:"size:255"`
	RegistryPassword string `json:"-" gorm:"type:text"`
	// RegistryPasswordUnreadable is set by the repository when the stored
	// password cannot be decrypted. RegistryPassword is then empty, and a save
	// keeps the stored value unless the caller sets a new one.
	RegistryPasswordUnreadable bool   `json:"-" gorm:"-"`
	ImagePullSecretName        string `json:"image_pull_secret_name" gorm:"size:255"`
	MaxNamespaces              int    `json:"max_namespaces"`
	MaxInstancesPerUser        int    `json:"max_instances_per_user" gorm:"default:0"` // 0 = unlimited
	IsDefault                  bool   `json:"is_default"`
	UseInCluster               bool   `json:"use_in_cluster"`
	// AllowPortForward lets users open port-forward tunnels to the stacks of
	// this cluster. Off by default; only an admin sets it.
	AllowPortForward bool `json:"allow_port_forward" gorm:"default:false"`
	// AllowExec lets users run commands in the containers of the stacks of
	// this cluster. Off by default; only an admin sets it. Exec shows the
	// container environment and its secrets.
	AllowExec bool `json:"allow_exec" gorm:"default:false"`
}

// ClusterStreamFlags are the per-cluster switches of the stream routes
// (ClusterRepository.StreamFlagsByIDs).
type ClusterStreamFlags struct {
	PortForward bool
	Exec        bool
}

// RegistryConfig returns the registry configuration for this cluster, or nil
// if no registry is configured.
func (c *Cluster) RegistryConfig() *RegistryConfig {
	if c.RegistryURL == "" || c.RegistryPassword == "" {
		return nil
	}
	secretName := c.ImagePullSecretName
	if secretName == "" {
		secretName = "registry-pull-secret"
	}
	return &RegistryConfig{
		URL:        c.RegistryURL,
		Username:   c.RegistryUsername,
		Password:   c.RegistryPassword,
		SecretName: secretName,
	}
}

// RegistryConfig holds container registry credentials for pull secret provisioning.
type RegistryConfig struct {
	URL        string
	Username   string
	Password   string
	SecretName string
}

// DefaultClusterKey is the key of the default cluster in the map that
// ClusterRepository.StreamFlagsByIDs returns. It is also the cluster ID of an
// instance without an assigned cluster.
const DefaultClusterKey = ""

// ClusterRepository defines data access operations for clusters.
// Every method runs its queries with ctx (#656).
type ClusterRepository interface {
	Create(ctx context.Context, cluster *Cluster) error
	FindByID(ctx context.Context, id string) (*Cluster, error)
	// Update writes the cluster row except is_default (SetDefault writes it)
	// and health_status (UpdateHealthStatus writes it).
	Update(ctx context.Context, cluster *Cluster) error
	// UpdateHealthStatus writes health_status and updated_at only, so a
	// concurrent admin edit of the other columns stays (#678). It returns
	// ErrNotFound when the cluster no longer exists.
	UpdateHealthStatus(ctx context.Context, id, status string) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]Cluster, error)
	// NamesByIDs returns the name of each cluster in ids that exists, keyed
	// by ID, in one query.
	NamesByIDs(ctx context.Context, ids []string) (map[string]string, error)
	// StreamFlagsByIDs returns allow_port_forward and allow_exec of each
	// cluster in ids that exists, keyed by ID, in one query that selects only
	// id, is_default, allow_port_forward and allow_exec (no decryption of
	// kubeconfig or registry password). With includeDefault the flags of the
	// default cluster are added under the key DefaultClusterKey (absent when no
	// default cluster exists).
	StreamFlagsByIDs(ctx context.Context, ids []string, includeDefault bool) (map[string]ClusterStreamFlags, error)
	CountAll(ctx context.Context) (int, error)
	CountByHealthStatus(ctx context.Context, status string) (int, error)
	FindDefault(ctx context.Context) (*Cluster, error)
	SetDefault(ctx context.Context, id string) error
}

// ClusterCredentialChecker is implemented by a repository that can refuse
// credentials it cannot store (no encryption key). The handler calls it before
// a write with side effects, such as SetDefault.
type ClusterCredentialChecker interface {
	CheckCredentialsStorable(ctx context.Context, cluster *Cluster) error
}
