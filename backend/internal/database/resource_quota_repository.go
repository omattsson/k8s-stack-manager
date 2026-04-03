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
var _ models.ResourceQuotaRepository = (*GORMResourceQuotaRepository)(nil)

// GORMResourceQuotaRepository implements models.ResourceQuotaRepository using GORM.
type GORMResourceQuotaRepository struct {
	db *gorm.DB
}

// NewGORMResourceQuotaRepository creates a new GORM-based resource quota repository.
func NewGORMResourceQuotaRepository(db *gorm.DB) *GORMResourceQuotaRepository {
	return &GORMResourceQuotaRepository{db: db}
}

// GetByClusterID retrieves the resource quota config for a cluster.
func (r *GORMResourceQuotaRepository) GetByClusterID(ctx context.Context, clusterID string) (*models.ResourceQuotaConfig, error) {
	var config models.ResourceQuotaConfig
	if err := r.db.WithContext(ctx).Where("cluster_id = ?", clusterID).First(&config).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("GetByClusterID", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("GetByClusterID", fmt.Errorf("query resource quota config: %w", err))
	}
	return &config, nil
}

// Upsert creates or updates the resource quota config for a cluster.
func (r *GORMResourceQuotaRepository) Upsert(ctx context.Context, config *models.ResourceQuotaConfig) error {
	if err := config.Validate(); err != nil {
		return dberrors.NewDatabaseError("Upsert", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
	}

	var existing models.ResourceQuotaConfig
	err := r.db.WithContext(ctx).Where("cluster_id = ?", config.ClusterID).First(&existing).Error

	now := time.Now().UTC()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Create new.
		config.ID = uuid.New().String()
		config.CreatedAt = now
		config.UpdatedAt = now
		if createErr := r.db.WithContext(ctx).Create(config).Error; createErr != nil {
			return dberrors.NewDatabaseError("Upsert", fmt.Errorf("create resource quota config: %w", createErr))
		}
		return nil
	}
	if err != nil {
		return dberrors.NewDatabaseError("Upsert", fmt.Errorf("query resource quota config: %w", err))
	}

	// Update existing.
	config.ID = existing.ID
	config.CreatedAt = existing.CreatedAt
	config.UpdatedAt = now
	if updateErr := r.db.WithContext(ctx).Save(config).Error; updateErr != nil {
		return dberrors.NewDatabaseError("Upsert", fmt.Errorf("update resource quota config: %w", updateErr))
	}
	return nil
}

// Delete removes the resource quota config for a cluster.
func (r *GORMResourceQuotaRepository) Delete(ctx context.Context, clusterID string) error {
	result := r.db.WithContext(ctx).Where("cluster_id = ?", clusterID).Delete(&models.ResourceQuotaConfig{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("Delete", fmt.Errorf("delete resource quota config: %w", result.Error))
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("Delete", dberrors.ErrNotFound)
	}
	return nil
}
