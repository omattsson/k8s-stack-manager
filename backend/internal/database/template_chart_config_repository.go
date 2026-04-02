package database

import (
	"fmt"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Compile-time interface check.
var _ models.TemplateChartConfigRepository = (*GORMTemplateChartConfigRepository)(nil)

// GORMTemplateChartConfigRepository implements models.TemplateChartConfigRepository using GORM.
type GORMTemplateChartConfigRepository struct {
	BaseRepository[models.TemplateChartConfig]
}

// NewGORMTemplateChartConfigRepository creates a new GORM-backed template chart config repository.
func NewGORMTemplateChartConfigRepository(db *gorm.DB) *GORMTemplateChartConfigRepository {
	return &GORMTemplateChartConfigRepository{
		BaseRepository: BaseRepository[models.TemplateChartConfig]{DB: db},
	}
}

// Create inserts a new template chart config record with validation.
func (r *GORMTemplateChartConfigRepository) Create(config *models.TemplateChartConfig) error {
	if err := config.Validate(); err != nil {
		return dberrors.NewDatabaseError("create", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
	}

	if config.ID == "" {
		config.ID = uuid.New().String()
	}
	config.CreatedAt = time.Now().UTC()
	return r.BaseRepository.Create(config)
}

// Update persists changes to an existing template chart config record.
func (r *GORMTemplateChartConfigRepository) Update(config *models.TemplateChartConfig) error {
	return r.Save(config)
}

// ListByTemplate returns all template chart configs for a given stack template.
func (r *GORMTemplateChartConfigRepository) ListByTemplate(templateID string) ([]models.TemplateChartConfig, error) {
	var configs []models.TemplateChartConfig
	if err := r.DB.Where("stack_template_id = ?", templateID).
		Order("deploy_order ASC").
		Find(&configs).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_template", err)
	}
	return configs, nil
}
