package database

import (
	"context"
	"errors"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"gorm.io/gorm"
)

// GORMTemplateVersionRepository implements TemplateVersionRepository using GORM.
type GORMTemplateVersionRepository struct {
	db *gorm.DB
}

// NewGORMTemplateVersionRepository creates a new GORM-backed template version repository.
func NewGORMTemplateVersionRepository(db *gorm.DB) *GORMTemplateVersionRepository {
	return &GORMTemplateVersionRepository{db: db}
}

// Create inserts a new template version record.
func (r *GORMTemplateVersionRepository) Create(ctx context.Context, version *models.TemplateVersion) error {
	if err := r.db.WithContext(ctx).Create(version).Error; err != nil {
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// ListByTemplate returns all versions for a template, ordered newest first.
func (r *GORMTemplateVersionRepository) ListByTemplate(ctx context.Context, templateID string) ([]models.TemplateVersion, error) {
	var versions []models.TemplateVersion
	if err := r.db.WithContext(ctx).
		Where("template_id = ?", templateID).
		Order("created_at DESC").
		Find(&versions).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}
	return versions, nil
}

// GetByID returns a single template version by its ID.
func (r *GORMTemplateVersionRepository) GetByID(ctx context.Context, templateID, id string) (*models.TemplateVersion, error) {
	var version models.TemplateVersion
	if err := r.db.WithContext(ctx).Where("template_id = ? AND id = ?", templateID, id).First(&version).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find", err)
	}
	return &version, nil
}

// GetLatestByTemplate returns the most recent version for a template.
func (r *GORMTemplateVersionRepository) GetLatestByTemplate(ctx context.Context, templateID string) (*models.TemplateVersion, error) {
	var version models.TemplateVersion
	if err := r.db.WithContext(ctx).
		Where("template_id = ?", templateID).
		Order("created_at DESC").
		First(&version).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find", err)
	}
	return &version, nil
}
