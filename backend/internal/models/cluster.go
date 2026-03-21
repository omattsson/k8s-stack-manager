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
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	APIServerURL   string    `json:"api_server_url"`
	KubeconfigData string    `json:"-"`
	KubeconfigPath string    `json:"-"`
	Region         string    `json:"region"`
	HealthStatus   string    `json:"health_status"`
	MaxNamespaces  int       `json:"max_namespaces"`
	IsDefault      bool      `json:"is_default"`
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
