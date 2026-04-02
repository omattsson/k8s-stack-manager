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
var _ models.StackTemplateRepository = (*GORMStackTemplateRepository)(nil)

// GORMStackTemplateRepository implements models.StackTemplateRepository using GORM.
type GORMStackTemplateRepository struct {
	db *gorm.DB
}

// NewGORMStackTemplateRepository creates a new GORM-backed stack template repository.
func NewGORMStackTemplateRepository(db *gorm.DB) *GORMStackTemplateRepository {
	return &GORMStackTemplateRepository{db: db}
}

// Create inserts a new stack template record.
func (r *GORMStackTemplateRepository) Create(template *models.StackTemplate) error {
	if template.ID == "" {
		template.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	template.CreatedAt = now
	template.UpdatedAt = now
	if err := r.db.Create(template).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns a stack template by its ID.
func (r *GORMStackTemplateRepository) FindByID(id string) (*models.StackTemplate, error) {
	var template models.StackTemplate
	if err := r.db.Where("id = ?", id).First(&template).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &template, nil
}

// Update persists changes to an existing stack template.
func (r *GORMStackTemplateRepository) Update(template *models.StackTemplate) error {
	template.UpdatedAt = time.Now().UTC()
	if err := r.db.Save(template).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("update", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("update", err)
	}
	return nil
}

// Delete removes a stack template by ID.
func (r *GORMStackTemplateRepository) Delete(id string) error {
	result := r.db.Where("id = ?", id).Delete(&models.StackTemplate{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// List returns all stack templates.
func (r *GORMStackTemplateRepository) List() ([]models.StackTemplate, error) {
	var templates []models.StackTemplate
	if err := r.db.Find(&templates).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}
	return templates, nil
}

// Count returns the total number of stack templates.
func (r *GORMStackTemplateRepository) Count() (int64, error) {
	var count int64
	if err := r.db.Model(&models.StackTemplate{}).Count(&count).Error; err != nil {
		return 0, dberrors.NewDatabaseError("count", err)
	}
	return count, nil
}

// ListPublished returns all published stack templates.
func (r *GORMStackTemplateRepository) ListPublished() ([]models.StackTemplate, error) {
	var templates []models.StackTemplate
	if err := r.db.Where("is_published = ?", true).Find(&templates).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_published", err)
	}
	return templates, nil
}

// ListByOwner returns all stack templates owned by the given user.
func (r *GORMStackTemplateRepository) ListByOwner(ownerID string) ([]models.StackTemplate, error) {
	var templates []models.StackTemplate
	if err := r.db.Where("owner_id = ?", ownerID).Find(&templates).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_owner", err)
	}
	return templates, nil
}
