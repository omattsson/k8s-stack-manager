package database

import (
	"errors"
	"fmt"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Compile-time interface check.
var _ models.ValueOverrideRepository = (*GORMValueOverrideRepository)(nil)

// GORMValueOverrideRepository implements models.ValueOverrideRepository using GORM.
type GORMValueOverrideRepository struct {
	BaseRepository[models.ValueOverride]
}

// NewGORMValueOverrideRepository creates a new GORM-backed value override repository.
func NewGORMValueOverrideRepository(db *gorm.DB) *GORMValueOverrideRepository {
	return &GORMValueOverrideRepository{
		BaseRepository: BaseRepository[models.ValueOverride]{DB: db},
	}
}

// Create inserts a new value override record with validation.
func (r *GORMValueOverrideRepository) Create(override *models.ValueOverride) error {
	if err := override.Validate(); err != nil {
		return dberrors.NewDatabaseError("create", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
	}

	if override.ID == "" {
		override.ID = uuid.New().String()
	}
	override.UpdatedAt = time.Now().UTC()
	return r.BaseRepository.Create(override)
}

// FindByInstanceAndChart returns the value override for a specific instance and chart config.
func (r *GORMValueOverrideRepository) FindByInstanceAndChart(instanceID, chartConfigID string) (*models.ValueOverride, error) {
	var override models.ValueOverride
	if err := r.DB.Where("stack_instance_id = ? AND chart_config_id = ?", instanceID, chartConfigID).
		First(&override).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_instance_and_chart", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_instance_and_chart", err)
	}
	return &override, nil
}

// Update persists changes to an existing value override record.
func (r *GORMValueOverrideRepository) Update(override *models.ValueOverride) error {
	override.UpdatedAt = time.Now().UTC()
	return r.Save(override)
}

// ListByInstance returns all value overrides for a given stack instance.
func (r *GORMValueOverrideRepository) ListByInstance(instanceID string) ([]models.ValueOverride, error) {
	var overrides []models.ValueOverride
	if err := r.DB.Where("stack_instance_id = ?", instanceID).
		Find(&overrides).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_instance", err)
	}
	return overrides, nil
}
