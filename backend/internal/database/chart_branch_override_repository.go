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
var _ models.ChartBranchOverrideRepository = (*GORMChartBranchOverrideRepository)(nil)

// GORMChartBranchOverrideRepository implements models.ChartBranchOverrideRepository using GORM.
type GORMChartBranchOverrideRepository struct {
	db *gorm.DB
}

// NewGORMChartBranchOverrideRepository creates a new GORM-backed chart branch override repository.
func NewGORMChartBranchOverrideRepository(db *gorm.DB) *GORMChartBranchOverrideRepository {
	return &GORMChartBranchOverrideRepository{db: db}
}

// List returns all branch overrides for a stack instance.
func (r *GORMChartBranchOverrideRepository) List(instanceID string) ([]*models.ChartBranchOverride, error) {
	var overrides []*models.ChartBranchOverride
	if err := r.db.Where("stack_instance_id = ?", instanceID).Find(&overrides).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}
	return overrides, nil
}

// Get returns the branch override for a specific instance and chart config.
func (r *GORMChartBranchOverrideRepository) Get(instanceID, chartConfigID string) (*models.ChartBranchOverride, error) {
	var override models.ChartBranchOverride
	if err := r.db.Where("stack_instance_id = ? AND chart_config_id = ?", instanceID, chartConfigID).
		First(&override).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("get", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("get", err)
	}
	return &override, nil
}

// Set creates or updates a branch override (upsert).
func (r *GORMChartBranchOverrideRepository) Set(override *models.ChartBranchOverride) error {
	if err := override.Validate(); err != nil {
		return dberrors.NewDatabaseError("set", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
	}

	now := time.Now().UTC()
	override.UpdatedAt = now

	// Check if an override already exists for this instance + chart combo.
	var existing models.ChartBranchOverride
	err := r.db.Where("stack_instance_id = ? AND chart_config_id = ?", override.StackInstanceID, override.ChartConfigID).
		First(&existing).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Create new.
		if override.ID == "" {
			override.ID = uuid.New().String()
		}
		if createErr := r.db.Create(override).Error; createErr != nil {
			return dberrors.NewDatabaseError("set", createErr)
		}
		return nil
	}
	if err != nil {
		return dberrors.NewDatabaseError("set", err)
	}

	// Update existing.
	override.ID = existing.ID
	if updateErr := r.db.Save(override).Error; updateErr != nil {
		return dberrors.NewDatabaseError("set", updateErr)
	}
	return nil
}

// Delete removes a branch override for a specific instance and chart config.
func (r *GORMChartBranchOverrideRepository) Delete(instanceID, chartConfigID string) error {
	result := r.db.Where("stack_instance_id = ? AND chart_config_id = ?", instanceID, chartConfigID).
		Delete(&models.ChartBranchOverride{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// DeleteByInstance removes all branch overrides for a stack instance.
func (r *GORMChartBranchOverrideRepository) DeleteByInstance(instanceID string) error {
	if err := r.db.Where("stack_instance_id = ?", instanceID).
		Delete(&models.ChartBranchOverride{}).Error; err != nil {
		return dberrors.NewDatabaseError("delete_by_instance", err)
	}
	return nil
}
