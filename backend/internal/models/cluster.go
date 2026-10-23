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
	RegistryURL         string `json:"registry_url" gorm:"size:500"`
	RegistryUsername    string `json:"registry_username" gorm:"size:255"`
	RegistryPassword    string `json:"-" gorm:"type:text"`
	ImagePullSecretName string `json:"image_pull_secret_name" gorm:"size:255"`
	MaxNamespaces       int    `json:"max_namespaces"`
	MaxInstancesPerUser int    `json:"max_instances_per_user" gorm:"default:0"` // 0 = unlimited
	IsDefault           bool   `json:"is_default"`
	UseInCluster        bool   `json:"use_in_cluster"`
}

// RegistryConfig returns the registry configuration for this cluster, or nil
// if no registry is configured.
func (c *Cluster) RegistryConfig() *RegistryConfig {
	if c.RegistryURL == "" {
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
	CountAll(ctx context.Context) (int, error)
	CountByHealthStatus(ctx context.Context, status string) (int, error)
	FindDefault(ctx context.Context) (*Cluster, error)
	SetDefault(ctx context.Context, id string) error
}
