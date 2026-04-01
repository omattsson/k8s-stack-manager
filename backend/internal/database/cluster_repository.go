package database

import (
	"errors"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Compile-time interface check.
var _ models.ClusterRepository = (*GORMClusterRepository)(nil)

// GORMClusterRepository implements models.ClusterRepository using GORM.
type GORMClusterRepository struct {
	db *gorm.DB
}

// NewGORMClusterRepository creates a new GORM-backed cluster repository.
func NewGORMClusterRepository(db *gorm.DB) *GORMClusterRepository {
	return &GORMClusterRepository{db: db}
}

// Create inserts a new cluster record.
func (r *GORMClusterRepository) Create(cluster *models.Cluster) error {
	if cluster.ID == "" {
		cluster.ID = uuid.New().String()
	}
	if cluster.HealthStatus == "" {
		cluster.HealthStatus = models.ClusterUnreachable
	}
	now := time.Now().UTC()
	cluster.CreatedAt = now
	cluster.UpdatedAt = now
	if err := r.db.Create(cluster).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns a cluster by its ID.
func (r *GORMClusterRepository) FindByID(id string) (*models.Cluster, error) {
	var cluster models.Cluster
	if err := r.db.Where("id = ?", id).First(&cluster).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &cluster, nil
}

// Update persists changes to an existing cluster record.
func (r *GORMClusterRepository) Update(cluster *models.Cluster) error {
	cluster.UpdatedAt = time.Now().UTC()
	if err := r.db.Save(cluster).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("update", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("update", err)
	}
	return nil
}

// Delete removes a cluster by ID.
func (r *GORMClusterRepository) Delete(id string) error {
	result := r.db.Where("id = ?", id).Delete(&models.Cluster{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// List returns all clusters.
func (r *GORMClusterRepository) List() ([]models.Cluster, error) {
	var clusters []models.Cluster
	if err := r.db.Find(&clusters).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}
	return clusters, nil
}

// FindDefault returns the cluster marked as default.
func (r *GORMClusterRepository) FindDefault() (*models.Cluster, error) {
	var cluster models.Cluster
	if err := r.db.Where("is_default = ?", true).First(&cluster).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_default", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_default", err)
	}
	return &cluster, nil
}

// SetDefault unsets all existing defaults and marks the given cluster as default.
func (r *GORMClusterRepository) SetDefault(id string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// Unset all current defaults.
		if err := tx.Model(&models.Cluster{}).
			Where("is_default = ?", true).
			Update("is_default", false).Error; err != nil {
			return dberrors.NewDatabaseError("set_default", err)
		}

		// Set the target cluster as default.
		result := tx.Model(&models.Cluster{}).
			Where("id = ?", id).
			Updates(map[string]interface{}{
				"is_default": true,
				"updated_at": time.Now().UTC(),
			})
		if result.Error != nil {
			return dberrors.NewDatabaseError("set_default", result.Error)
		}
		if result.RowsAffected == 0 {
			return dberrors.NewDatabaseError("set_default", dberrors.ErrNotFound)
		}
		return nil
	})
}
