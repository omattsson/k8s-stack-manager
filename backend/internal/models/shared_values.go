package models

import "time"

// SharedValues represents cluster-wide shared values that are applied to all
// stack instances deployed to a given cluster before chart-specific defaults.
type SharedValues struct {
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ID          string    `json:"id" gorm:"primaryKey;size:36"`
	ClusterID   string    `json:"cluster_id" gorm:"size:36"`
	Name        string    `json:"name" gorm:"size:255"`
	Description string    `json:"description" gorm:"type:text"`
	Values      string    `json:"values" gorm:"type:longtext"` // YAML content
	Priority    int       `json:"priority"`                    // Lower = applied first
}

// SharedValuesRepository defines data access operations for shared values.
type SharedValuesRepository interface {
	Create(sv *SharedValues) error
	FindByID(id string) (*SharedValues, error)
	FindByClusterAndID(clusterID, id string) (*SharedValues, error)
	Update(sv *SharedValues) error
	Delete(id string) error
	ListByCluster(clusterID string) ([]SharedValues, error)
}
