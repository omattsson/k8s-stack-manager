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

// FindByName returns all stack definitions with the given exact name.
func (r *GORMStackDefinitionRepository) FindByName(name string) ([]models.StackDefinition, error) {
	var definitions []models.StackDefinition
	if err := r.db.Where("name = ?", name).Find(&definitions).Error; err != nil {
		return nil, dberrors.NewDatabaseError("find_by_name", err)
	}
	return definitions, nil
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

// ListPaged returns a page of stack definitions ordered by created_at DESC,
// along with the total count. Only columns needed for list views are selected.
func (r *GORMStackDefinitionRepository) ListPaged(limit, offset int) ([]models.StackDefinition, int64, error) {
	var total int64
	if err := r.db.Model(&models.StackDefinition{}).Count(&total).Error; err != nil {
		return nil, 0, dberrors.NewDatabaseError("count", err)
	}
	var definitions []models.StackDefinition
	if err := r.db.Select("id, name, description, owner_id, source_template_id, default_branch, created_at, updated_at").
		Order("created_at DESC").Limit(limit).Offset(offset).Find(&definitions).Error; err != nil {
		return nil, 0, dberrors.NewDatabaseError("list_paged", err)
	}
	return definitions, total, nil
}

// Count returns the total number of stack definitions.
func (r *GORMStackDefinitionRepository) Count() (int64, error) {
	var count int64
	if err := r.db.Model(&models.StackDefinition{}).Count(&count).Error; err != nil {
		return 0, dberrors.NewDatabaseError("count", err)
	}
	return count, nil
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

// CountByTemplateIDs returns a map of template ID to definition count for the
// given template IDs in a single query, eliminating N+1 lookups. IDs are
// processed in chunks of 500 to stay within MySQL's IN clause limits.
func (r *GORMStackDefinitionRepository) CountByTemplateIDs(templateIDs []string) (map[string]int, error) {
	if len(templateIDs) == 0 {
		return make(map[string]int), nil
	}
	result := make(map[string]int, len(templateIDs))
	const chunkSize = 500
	for start := 0; start < len(templateIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(templateIDs) {
			end = len(templateIDs)
		}
		chunk := templateIDs[start:end]

		type countRow struct {
			SourceTemplateID string
			Count            int
		}
		var rows []countRow
		if err := r.db.Model(&models.StackDefinition{}).
			Select("source_template_id, COUNT(*) as count").
			Where("source_template_id IN ?", chunk).
			Group("source_template_id").
			Find(&rows).Error; err != nil {
			return nil, dberrors.NewDatabaseError("count_by_template_ids", err)
		}
		for _, row := range rows {
			result[row.SourceTemplateID] = row.Count
		}
	}
	return result, nil
}

// ListIDsByTemplateIDs returns a map of template ID to definition IDs, selecting
// only the id and source_template_id columns for efficiency. IDs are processed
// in chunks of 500 to stay within MySQL's IN clause limits.
func (r *GORMStackDefinitionRepository) ListIDsByTemplateIDs(templateIDs []string) (map[string][]string, error) {
	if len(templateIDs) == 0 {
		return make(map[string][]string), nil
	}
	result := make(map[string][]string, len(templateIDs))
	const chunkSize = 500
	for start := 0; start < len(templateIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(templateIDs) {
			end = len(templateIDs)
		}
		chunk := templateIDs[start:end]

		type idRow struct {
			ID               string
			SourceTemplateID string
		}
		var rows []idRow
		if err := r.db.Model(&models.StackDefinition{}).
			Select("id, source_template_id").
			Where("source_template_id IN ?", chunk).
			Find(&rows).Error; err != nil {
			return nil, dberrors.NewDatabaseError("list_ids_by_template_ids", err)
		}
		for _, row := range rows {
			result[row.SourceTemplateID] = append(result[row.SourceTemplateID], row.ID)
		}
	}
	return result, nil
}
