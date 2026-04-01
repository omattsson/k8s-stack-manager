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
var _ models.InstanceQuotaOverrideRepository = (*GORMInstanceQuotaOverrideRepository)(nil)

// GORMInstanceQuotaOverrideRepository implements models.InstanceQuotaOverrideRepository using GORM.
type GORMInstanceQuotaOverrideRepository struct {
	db *gorm.DB
}

// NewGORMInstanceQuotaOverrideRepository creates a new GORM-based instance quota override repository.
func NewGORMInstanceQuotaOverrideRepository(db *gorm.DB) *GORMInstanceQuotaOverrideRepository {
	return &GORMInstanceQuotaOverrideRepository{db: db}
}

// GetByInstanceID retrieves the quota override for a stack instance.
func (r *GORMInstanceQuotaOverrideRepository) GetByInstanceID(ctx context.Context, instanceID string) (*models.InstanceQuotaOverride, error) {
	var override models.InstanceQuotaOverride
	if err := r.db.WithContext(ctx).Where("stack_instance_id = ?", instanceID).First(&override).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("GetByInstanceID", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("GetByInstanceID", fmt.Errorf("query instance quota override: %w", err))
	}
	return &override, nil
}

// Upsert creates or updates the quota override for a stack instance.
func (r *GORMInstanceQuotaOverrideRepository) Upsert(ctx context.Context, override *models.InstanceQuotaOverride) error {
	if err := override.Validate(); err != nil {
		return dberrors.NewDatabaseError("Upsert", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
	}

	var existing models.InstanceQuotaOverride
	err := r.db.WithContext(ctx).Where("stack_instance_id = ?", override.StackInstanceID).First(&existing).Error

	now := time.Now().UTC()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Create new.
		override.ID = uuid.New().String()
		override.CreatedAt = now
		override.UpdatedAt = now
		if createErr := r.db.WithContext(ctx).Create(override).Error; createErr != nil {
			return dberrors.NewDatabaseError("Upsert", fmt.Errorf("create instance quota override: %w", createErr))
		}
		return nil
	}
	if err != nil {
		return dberrors.NewDatabaseError("Upsert", fmt.Errorf("query instance quota override: %w", err))
	}

	// Update existing.
	override.ID = existing.ID
	override.CreatedAt = existing.CreatedAt
	override.UpdatedAt = now
	if updateErr := r.db.WithContext(ctx).Save(override).Error; updateErr != nil {
		return dberrors.NewDatabaseError("Upsert", fmt.Errorf("update instance quota override: %w", updateErr))
	}
	return nil
}

// Delete removes the quota override for a stack instance.
func (r *GORMInstanceQuotaOverrideRepository) Delete(ctx context.Context, instanceID string) error {
	result := r.db.WithContext(ctx).Where("stack_instance_id = ?", instanceID).Delete(&models.InstanceQuotaOverride{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("Delete", fmt.Errorf("delete instance quota override: %w", result.Error))
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("Delete", dberrors.ErrNotFound)
	}
	return nil
}
