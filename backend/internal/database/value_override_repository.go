package database

import (
	"errors"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"gorm.io/gorm"
)

// Compile-time interface check.
var _ models.ValueOverrideRepository = (*GORMValueOverrideRepository)(nil)

// GORMValueOverrideRepository implements models.ValueOverrideRepository using GORM.
type GORMValueOverrideRepository struct {
	db *gorm.DB
}

// NewGORMValueOverrideRepository creates a new GORM-backed value override repository.
func NewGORMValueOverrideRepository(db *gorm.DB) *GORMValueOverrideRepository {
	return &GORMValueOverrideRepository{db: db}
}

// Create inserts a new value override record.
func (r *GORMValueOverrideRepository) Create(override *models.ValueOverride) error {
	override.UpdatedAt = time.Now().UTC()
	if err := r.db.Create(override).Error; err != nil {
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns a value override by its ID.
func (r *GORMValueOverrideRepository) FindByID(id string) (*models.ValueOverride, error) {
	var override models.ValueOverride
	if err := r.db.Where("id = ?", id).First(&override).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &override, nil
}

// FindByInstanceAndChart returns the value override for a specific instance and chart config.
func (r *GORMValueOverrideRepository) FindByInstanceAndChart(instanceID, chartConfigID string) (*models.ValueOverride, error) {
	var override models.ValueOverride
	if err := r.db.Where("stack_instance_id = ? AND chart_config_id = ?", instanceID, chartConfigID).
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
	if err := r.db.Save(override).Error; err != nil {
		return dberrors.NewDatabaseError("update", err)
	}
	return nil
}

// Delete removes a value override by ID.
func (r *GORMValueOverrideRepository) Delete(id string) error {
	result := r.db.Where("id = ?", id).Delete(&models.ValueOverride{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// ListByInstance returns all value overrides for a given stack instance.
func (r *GORMValueOverrideRepository) ListByInstance(instanceID string) ([]models.ValueOverride, error) {
	var overrides []models.ValueOverride
	if err := r.db.Where("stack_instance_id = ?", instanceID).
		Find(&overrides).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_instance", err)
	}
	return overrides, nil
}
