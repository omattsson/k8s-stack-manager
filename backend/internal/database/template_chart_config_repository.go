package database

import (
	"context"
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
func (r *GORMTemplateChartConfigRepository) Create(ctx context.Context, config *models.TemplateChartConfig) error {
	if err := config.Validate(); err != nil {
		return dberrors.NewDatabaseError("create", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
	}

	if config.ID == "" {
		config.ID = uuid.New().String()
	}
	config.CreatedAt = time.Now().UTC()
	return r.BaseRepository.Create(ctx, config)
}

// Update persists changes to an existing template chart config record.
func (r *GORMTemplateChartConfigRepository) Update(ctx context.Context, config *models.TemplateChartConfig) error {
	if err := config.Validate(); err != nil {
		return dberrors.NewDatabaseError("update", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
	}
	return r.BaseRepository.Save(ctx, config)
}

// ListByTemplate returns all template chart configs for a given stack template.
func (r *GORMTemplateChartConfigRepository) ListByTemplate(ctx context.Context, templateID string) ([]models.TemplateChartConfig, error) {
	var configs []models.TemplateChartConfig
	if err := r.DB.WithContext(ctx).Where("stack_template_id = ?", templateID).
		Order("deploy_order ASC").
		Find(&configs).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_template", err)
	}
	return configs, nil
}

// FindByID returns a template chart config by its ID.
func (r *GORMTemplateChartConfigRepository) FindByID(ctx context.Context, id string) (*models.TemplateChartConfig, error) {
	return r.BaseRepository.FindByID(ctx, id)
}

// Delete removes a template chart config by ID.
func (r *GORMTemplateChartConfigRepository) Delete(ctx context.Context, id string) error {
	return r.BaseRepository.Delete(ctx, id)
}
