package models

import (
	"context"
	"sort"
	"time"
)

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
	Create(ctx context.Context, sv *SharedValues) error
	FindByID(ctx context.Context, id string) (*SharedValues, error)
	FindByClusterAndID(ctx context.Context, clusterID, id string) (*SharedValues, error)
	Update(ctx context.Context, sv *SharedValues) error
	Delete(ctx context.Context, id string) error
	ListByCluster(ctx context.Context, clusterID string) ([]SharedValues, error)
}

// SortSharedValues orders shared values for merging and listing: priority
// ascending, then name, then ID. Names and IDs compare byte-wise in Go, so the
// order does not depend on the database collation ("Beta" sorts before
// "alpha"). The repository and the values pipeline both use it.
func SortSharedValues(values []SharedValues) {
	sort.SliceStable(values, func(i, j int) bool {
		a, b := values[i], values[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
}
