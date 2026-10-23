package database

import (
	"context"
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
func (r *GORMValueOverrideRepository) Create(ctx context.Context, override *models.ValueOverride) error {
	if err := override.Validate(); err != nil {
		return dberrors.NewDatabaseError("create", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
	}

	if override.ID == "" {
		override.ID = uuid.New().String()
	}
	override.UpdatedAt = time.Now().UTC()
	return r.BaseRepository.Create(ctx, override)
}

// FindByID returns a value override by ID.
func (r *GORMValueOverrideRepository) FindByID(ctx context.Context, id string) (*models.ValueOverride, error) {
	return r.BaseRepository.FindByID(ctx, id)
}

// Delete deletes a value override by ID.
func (r *GORMValueOverrideRepository) Delete(ctx context.Context, id string) error {
	return r.BaseRepository.Delete(ctx, id)
}

// FindByInstanceAndChart returns the value override for a specific instance and chart config.
func (r *GORMValueOverrideRepository) FindByInstanceAndChart(ctx context.Context, instanceID, chartConfigID string) (*models.ValueOverride, error) {
	var override models.ValueOverride
	if err := r.DB.WithContext(ctx).Where("stack_instance_id = ? AND chart_config_id = ?", instanceID, chartConfigID).
		First(&override).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_instance_and_chart", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_instance_and_chart", err)
	}
	return &override, nil
}

// Update persists changes to an existing value override record.
func (r *GORMValueOverrideRepository) Update(ctx context.Context, override *models.ValueOverride) error {
	override.UpdatedAt = time.Now().UTC()
	return r.BaseRepository.Save(ctx, override)
}

// ListByInstance returns all value overrides for a given stack instance.
func (r *GORMValueOverrideRepository) ListByInstance(ctx context.Context, instanceID string) ([]models.ValueOverride, error) {
	var overrides []models.ValueOverride
	if err := r.DB.WithContext(ctx).Where("stack_instance_id = ?", instanceID).
		Find(&overrides).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_instance", err)
	}
	return overrides, nil
}

// DeleteByInstance deletes all value overrides of an instance in one statement.
func (r *GORMValueOverrideRepository) DeleteByInstance(ctx context.Context, instanceID string) error {
	if err := r.DB.WithContext(ctx).Where("stack_instance_id = ?", instanceID).Delete(&models.ValueOverride{}).Error; err != nil {
		return dberrors.NewDatabaseError("delete_by_instance", err)
	}
	return nil
}
