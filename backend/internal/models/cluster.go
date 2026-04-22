package models

import "time"

// Cluster health status constants.
const (
	ClusterHealthy     = "healthy"
	ClusterDegraded    = "degraded"
	ClusterUnreachable = "unreachable"
)

// Cluster represents a Kubernetes cluster that stack instances can be deployed to.
type Cluster struct {
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
	ID                  string    `json:"id" gorm:"primaryKey;size:36"`
	Name                string    `json:"name" gorm:"size:255;uniqueIndex"`
	Description         string    `json:"description" gorm:"type:text"`
	APIServerURL        string    `json:"api_server_url" gorm:"size:500"`
	KubeconfigData      string    `json:"-" gorm:"type:longtext"`
	KubeconfigPath      string    `json:"-" gorm:"size:500"`
	Region              string    `json:"region" gorm:"size:100"`
	HealthStatus        string    `json:"health_status" gorm:"size:50"`
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
type ClusterRepository interface {
	Create(cluster *Cluster) error
	FindByID(id string) (*Cluster, error)
	Update(cluster *Cluster) error
	Delete(id string) error
	List() ([]Cluster, error)
	FindDefault() (*Cluster, error)
	SetDefault(id string) error
}
