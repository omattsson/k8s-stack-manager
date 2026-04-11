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
	MaxNamespaces       int       `json:"max_namespaces"`
	MaxInstancesPerUser int       `json:"max_instances_per_user" gorm:"default:0"` // 0 = unlimited
	IsDefault           bool      `json:"is_default"`
	UseInCluster        bool      `json:"use_in_cluster"`
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
