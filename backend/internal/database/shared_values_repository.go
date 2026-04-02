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
	BaseRepository[models.SharedValues]
}

// NewGORMSharedValuesRepository creates a new GORM-backed shared values repository.
func NewGORMSharedValuesRepository(db *gorm.DB) *GORMSharedValuesRepository {
	return &GORMSharedValuesRepository{
		BaseRepository: BaseRepository[models.SharedValues]{DB: db},
	}
}

// Create inserts a new shared values record.
func (r *GORMSharedValuesRepository) Create(sv *models.SharedValues) error {
	if sv.ID == "" {
		sv.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	sv.CreatedAt = now
	sv.UpdatedAt = now
	return r.BaseRepository.Create(sv)
}

// FindByClusterAndID returns a shared values record matching both cluster ID and record ID.
func (r *GORMSharedValuesRepository) FindByClusterAndID(clusterID, id string) (*models.SharedValues, error) {
	var sv models.SharedValues
	if err := r.DB.Where("cluster_id = ? AND id = ?", clusterID, id).First(&sv).Error; err != nil {
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
	return r.Save(sv)
}

// ListByCluster returns all shared values for a given cluster, ordered by priority ascending.
func (r *GORMSharedValuesRepository) ListByCluster(clusterID string) ([]models.SharedValues, error) {
	var values []models.SharedValues
	if err := r.DB.Where("cluster_id = ?", clusterID).
		Order("priority ASC").
		Find(&values).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_cluster", err)
	}
	return values, nil
}
