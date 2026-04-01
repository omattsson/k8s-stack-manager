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
var _ models.SharedValuesRepository = (*GORMSharedValuesRepository)(nil)

// GORMSharedValuesRepository implements models.SharedValuesRepository using GORM.
type GORMSharedValuesRepository struct {
	db *gorm.DB
}

// NewGORMSharedValuesRepository creates a new GORM-backed shared values repository.
func NewGORMSharedValuesRepository(db *gorm.DB) *GORMSharedValuesRepository {
	return &GORMSharedValuesRepository{db: db}
}

// Create inserts a new shared values record.
func (r *GORMSharedValuesRepository) Create(sv *models.SharedValues) error {
	if sv.ID == "" {
		sv.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	sv.CreatedAt = now
	sv.UpdatedAt = now
	if err := r.db.Create(sv).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns a shared values record by its ID.
func (r *GORMSharedValuesRepository) FindByID(id string) (*models.SharedValues, error) {
	var sv models.SharedValues
	if err := r.db.Where("id = ?", id).First(&sv).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &sv, nil
}

// FindByClusterAndID returns a shared values record matching both cluster ID and record ID.
func (r *GORMSharedValuesRepository) FindByClusterAndID(clusterID, id string) (*models.SharedValues, error) {
	var sv models.SharedValues
	if err := r.db.Where("cluster_id = ? AND id = ?", clusterID, id).First(&sv).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_cluster_and_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_cluster_and_id", err)
	}
	return &sv, nil
}

// Update persists changes to an existing shared values record.
func (r *GORMSharedValuesRepository) Update(sv *models.SharedValues) error {
	sv.UpdatedAt = time.Now().UTC()
	if err := r.db.Save(sv).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("update", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("update", err)
	}
	return nil
}

// Delete removes a shared values record by ID.
func (r *GORMSharedValuesRepository) Delete(id string) error {
	result := r.db.Where("id = ?", id).Delete(&models.SharedValues{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// ListByCluster returns all shared values for a given cluster, ordered by priority ascending.
func (r *GORMSharedValuesRepository) ListByCluster(clusterID string) ([]models.SharedValues, error) {
	var values []models.SharedValues
	if err := r.db.Where("cluster_id = ?", clusterID).
		Order("priority ASC").
		Find(&values).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_cluster", err)
	}
	return values, nil
}
