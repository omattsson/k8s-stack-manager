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
var _ models.StackDefinitionRepository = (*GORMStackDefinitionRepository)(nil)

// GORMStackDefinitionRepository implements models.StackDefinitionRepository using GORM.
type GORMStackDefinitionRepository struct {
	db *gorm.DB
}

// NewGORMStackDefinitionRepository creates a new GORM-backed stack definition repository.
func NewGORMStackDefinitionRepository(db *gorm.DB) *GORMStackDefinitionRepository {
	return &GORMStackDefinitionRepository{db: db}
}

// Create inserts a new stack definition record.
func (r *GORMStackDefinitionRepository) Create(definition *models.StackDefinition) error {
	if definition.ID == "" {
		definition.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	definition.CreatedAt = now
	definition.UpdatedAt = now
	if err := r.db.Create(definition).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns a stack definition by its ID.
func (r *GORMStackDefinitionRepository) FindByID(id string) (*models.StackDefinition, error) {
	var definition models.StackDefinition
	if err := r.db.Where("id = ?", id).First(&definition).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &definition, nil
}

// Update persists changes to an existing stack definition.
func (r *GORMStackDefinitionRepository) Update(definition *models.StackDefinition) error {
	definition.UpdatedAt = time.Now().UTC()
	if err := r.db.Save(definition).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("update", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("update", err)
	}
	return nil
}

// Delete removes a stack definition by ID.
func (r *GORMStackDefinitionRepository) Delete(id string) error {
	result := r.db.Where("id = ?", id).Delete(&models.StackDefinition{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// List returns all stack definitions.
func (r *GORMStackDefinitionRepository) List() ([]models.StackDefinition, error) {
	var definitions []models.StackDefinition
	if err := r.db.Find(&definitions).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}
	return definitions, nil
}

// ListByOwner returns all stack definitions owned by the given user.
func (r *GORMStackDefinitionRepository) ListByOwner(ownerID string) ([]models.StackDefinition, error) {
	var definitions []models.StackDefinition
	if err := r.db.Where("owner_id = ?", ownerID).Find(&definitions).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_owner", err)
	}
	return definitions, nil
}

// ListByTemplate returns all stack definitions derived from the given template.
func (r *GORMStackDefinitionRepository) ListByTemplate(templateID string) ([]models.StackDefinition, error) {
	var definitions []models.StackDefinition
	if err := r.db.Where("source_template_id = ?", templateID).Find(&definitions).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_template", err)
	}
	return definitions, nil
}
