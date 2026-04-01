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
var _ models.ChartConfigRepository = (*GORMChartConfigRepository)(nil)

// GORMChartConfigRepository implements models.ChartConfigRepository using GORM.
type GORMChartConfigRepository struct {
	db *gorm.DB
}

// NewGORMChartConfigRepository creates a new GORM-backed chart config repository.
func NewGORMChartConfigRepository(db *gorm.DB) *GORMChartConfigRepository {
	return &GORMChartConfigRepository{db: db}
}

// Create inserts a new chart config record.
func (r *GORMChartConfigRepository) Create(config *models.ChartConfig) error {
	if config.ID == "" {
		config.ID = uuid.New().String()
	}
	config.CreatedAt = time.Now().UTC()
	if err := r.db.Create(config).Error; err != nil {
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns a chart config by its ID.
func (r *GORMChartConfigRepository) FindByID(id string) (*models.ChartConfig, error) {
	var config models.ChartConfig
	if err := r.db.Where("id = ?", id).First(&config).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &config, nil
}

// Update persists changes to an existing chart config record.
func (r *GORMChartConfigRepository) Update(config *models.ChartConfig) error {
	if err := r.db.Save(config).Error; err != nil {
		return dberrors.NewDatabaseError("update", err)
	}
	return nil
}

// Delete removes a chart config by ID.
func (r *GORMChartConfigRepository) Delete(id string) error {
	result := r.db.Where("id = ?", id).Delete(&models.ChartConfig{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// ListByDefinition returns all chart configs for a given stack definition.
func (r *GORMChartConfigRepository) ListByDefinition(definitionID string) ([]models.ChartConfig, error) {
	var configs []models.ChartConfig
	if err := r.db.Where("stack_definition_id = ?", definitionID).
		Order("deploy_order ASC").
		Find(&configs).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_definition", err)
	}
	return configs, nil
}
