package database

import (
	"errors"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"gorm.io/gorm"
)

// Compile-time interface check.
var _ models.TemplateChartConfigRepository = (*GORMTemplateChartConfigRepository)(nil)

// GORMTemplateChartConfigRepository implements models.TemplateChartConfigRepository using GORM.
type GORMTemplateChartConfigRepository struct {
	db *gorm.DB
}

// NewGORMTemplateChartConfigRepository creates a new GORM-backed template chart config repository.
func NewGORMTemplateChartConfigRepository(db *gorm.DB) *GORMTemplateChartConfigRepository {
	return &GORMTemplateChartConfigRepository{db: db}
}

// Create inserts a new template chart config record.
func (r *GORMTemplateChartConfigRepository) Create(config *models.TemplateChartConfig) error {
	config.CreatedAt = time.Now().UTC()
	if err := r.db.Create(config).Error; err != nil {
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns a template chart config by its ID.
func (r *GORMTemplateChartConfigRepository) FindByID(id string) (*models.TemplateChartConfig, error) {
	var config models.TemplateChartConfig
	if err := r.db.Where("id = ?", id).First(&config).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &config, nil
}

// Update persists changes to an existing template chart config record.
func (r *GORMTemplateChartConfigRepository) Update(config *models.TemplateChartConfig) error {
	if err := r.db.Save(config).Error; err != nil {
		return dberrors.NewDatabaseError("update", err)
	}
	return nil
}

// Delete removes a template chart config by ID.
func (r *GORMTemplateChartConfigRepository) Delete(id string) error {
	result := r.db.Where("id = ?", id).Delete(&models.TemplateChartConfig{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// ListByTemplate returns all template chart configs for a given stack template.
func (r *GORMTemplateChartConfigRepository) ListByTemplate(templateID string) ([]models.TemplateChartConfig, error) {
	var configs []models.TemplateChartConfig
	if err := r.db.Where("stack_template_id = ?", templateID).
		Order("deploy_order ASC").
		Find(&configs).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_template", err)
	}
	return configs, nil
}
