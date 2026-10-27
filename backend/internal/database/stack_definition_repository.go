package database

import (
	"context"
	"errors"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
func (r *GORMStackDefinitionRepository) Create(ctx context.Context, definition *models.StackDefinition) error {
	if definition.ID == "" {
		definition.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	definition.CreatedAt = now
	definition.UpdatedAt = now
	if err := r.db.WithContext(ctx).Create(definition).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns a stack definition by its ID.
func (r *GORMStackDefinitionRepository) FindByID(ctx context.Context, id string) (*models.StackDefinition, error) {
	var definition models.StackDefinition
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&definition).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &definition, nil
}

// FindByIDForUpdate returns a stack definition by its ID and locks its row
// (SELECT ... FOR UPDATE) until the surrounding transaction ends.
func (r *GORMStackDefinitionRepository) FindByIDForUpdate(ctx context.Context, id string) (*models.StackDefinition, error) {
	var definition models.StackDefinition
	if err := r.db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&definition).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id_for_update", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id_for_update", err)
	}
	return &definition, nil
}

// FindByName returns all stack definitions with the given exact name.
func (r *GORMStackDefinitionRepository) FindByName(ctx context.Context, name string) ([]models.StackDefinition, error) {
	var definitions []models.StackDefinition
	if err := r.db.WithContext(ctx).Where("name = ?", name).Find(&definitions).Error; err != nil {
		return nil, dberrors.NewDatabaseError("find_by_name", err)
	}
	return definitions, nil
}

// SetTemplateLink writes the template link columns of one definition.
func (r *GORMStackDefinitionRepository) SetTemplateLink(ctx context.Context, id, templateID, version string) error {
	result := r.db.WithContext(ctx).Model(&models.StackDefinition{}).Where("id = ?", id).Updates(map[string]any{
		"source_template_id":      templateID,
		"source_template_version": version,
		"updated_at":              time.Now().UTC(),
	})
	if result.Error != nil {
		return dberrors.NewDatabaseError("set_template_link", result.Error)
	}
	if result.RowsAffected == 0 {
		// MySQL counts only changed rows: check that the row exists.
		var count int64
		if err := r.db.WithContext(ctx).Model(&models.StackDefinition{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return dberrors.NewDatabaseError("set_template_link", err)
		}
		if count == 0 {
			return dberrors.NewDatabaseError("set_template_link", dberrors.ErrNotFound)
		}
	}
	return nil
}

// Update persists changes to an existing stack definition.
func (r *GORMStackDefinitionRepository) Update(ctx context.Context, definition *models.StackDefinition) error {
	definition.UpdatedAt = time.Now().UTC()
	if err := r.db.WithContext(ctx).Save(definition).Error; err != nil {
		// Save can run in a default transaction: keep the context error (#656).
		if ctx.Err() != nil {
			return withContextErr(ctx, "update", err)
		}
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("update", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("update", err)
	}
	return nil
}

// Delete removes a stack definition by ID.
func (r *GORMStackDefinitionRepository) Delete(ctx context.Context, id string) error {
	result := r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.StackDefinition{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// List returns all stack definitions.
func (r *GORMStackDefinitionRepository) List(ctx context.Context) ([]models.StackDefinition, error) {
	var definitions []models.StackDefinition
	if err := r.db.WithContext(ctx).Find(&definitions).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}
	return definitions, nil
}

// applyDefinitionFilter adds a WHERE condition for each non-empty field of f.
// ListPaged uses it for the count query and for the page query.
func applyDefinitionFilter(q *gorm.DB, f models.StackDefinitionFilter) *gorm.DB {
	if f.Name != "" {
		q = q.Where("name = ?", f.Name)
	}
	if f.OwnerID != "" {
		q = q.Where("owner_id = ?", f.OwnerID)
	}
	return q
}

// ListPaged returns a page of the stack definitions that match filter,
// ordered by created_at DESC, along with the total count of matching
// definitions. Only columns needed for list views are selected.
func (r *GORMStackDefinitionRepository) ListPaged(ctx context.Context, filter models.StackDefinitionFilter, limit, offset int) ([]models.StackDefinition, int64, error) {
	var total int64
	if err := applyDefinitionFilter(r.db.WithContext(ctx).Model(&models.StackDefinition{}), filter).Count(&total).Error; err != nil {
		return nil, 0, dberrors.NewDatabaseError("count", err)
	}
	definitions := []models.StackDefinition{}
	if err := applyDefinitionFilter(r.db.WithContext(ctx).Select("id, name, description, owner_id, source_template_id, default_branch, owner_instance_id, created_at, updated_at"), filter).
		Order("created_at DESC").Limit(limit).Offset(offset).Find(&definitions).Error; err != nil {
		return nil, 0, dberrors.NewDatabaseError("list_paged", err)
	}
	return definitions, total, nil
}

// NamesByIDs returns the name of each definition in ids that exists, keyed
// by ID. It selects only the id and name columns in one query.
func (r *GORMStackDefinitionRepository) NamesByIDs(ctx context.Context, ids []string) (map[string]string, error) {
	return namesByIDs(r.db.WithContext(ctx), &models.StackDefinition{}, ids)
}

// TemplateRefsByIDs returns the template of each definition in ids whose
// source_template_id names an existing template, keyed by definition ID. One
// query joins the template on its primary key and selects only the ID, name
// and category.
func (r *GORMStackDefinitionRepository) TemplateRefsByIDs(ctx context.Context, ids []string) (map[string]models.DefinitionTemplateRef, error) {
	result := make(map[string]models.DefinitionTemplateRef, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []struct {
		DefinitionID     string
		TemplateID       string
		TemplateName     string
		TemplateCategory string
	}
	if err := r.db.WithContext(ctx).Model(&models.StackDefinition{}).
		Select("stack_definitions.id AS definition_id, stack_templates.id AS template_id, stack_templates.name AS template_name, stack_templates.category AS template_category").
		Joins("JOIN stack_templates ON stack_templates.id = stack_definitions.source_template_id").
		Where("stack_definitions.id IN ?", ids).
		Find(&rows).Error; err != nil {
		return nil, dberrors.NewDatabaseError("template_refs_by_ids", err)
	}
	for _, row := range rows {
		result[row.DefinitionID] = models.DefinitionTemplateRef{
			TemplateID:       row.TemplateID,
			TemplateName:     row.TemplateName,
			TemplateCategory: row.TemplateCategory,
		}
	}
	return result, nil
}

// Count returns the total number of stack definitions.
func (r *GORMStackDefinitionRepository) Count(ctx context.Context) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.StackDefinition{}).Count(&count).Error; err != nil {
		return 0, dberrors.NewDatabaseError("count", err)
	}
	return count, nil
}

// ListByOwner returns all stack definitions owned by the given user.
func (r *GORMStackDefinitionRepository) ListByOwner(ctx context.Context, ownerID string) ([]models.StackDefinition, error) {
	var definitions []models.StackDefinition
	if err := r.db.WithContext(ctx).Where("owner_id = ?", ownerID).Find(&definitions).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_owner", err)
	}
	return definitions, nil
}

// ListByTemplate returns all stack definitions derived from the given template.
func (r *GORMStackDefinitionRepository) ListByTemplate(ctx context.Context, templateID string) ([]models.StackDefinition, error) {
	var definitions []models.StackDefinition
	if err := r.db.WithContext(ctx).Where("source_template_id = ?", templateID).Find(&definitions).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_template", err)
	}
	return definitions, nil
}

// CountByTemplateIDs returns a map of template ID to definition count for the
// given template IDs in a single query, eliminating N+1 lookups. IDs are
// processed in chunks of 500 to stay within MySQL's IN clause limits.
func (r *GORMStackDefinitionRepository) CountByTemplateIDs(ctx context.Context, templateIDs []string) (map[string]int, error) {
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
		if err := r.db.WithContext(ctx).Model(&models.StackDefinition{}).
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
func (r *GORMStackDefinitionRepository) ListIDsByTemplateIDs(ctx context.Context, templateIDs []string) (map[string][]string, error) {
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
		if err := r.db.WithContext(ctx).Model(&models.StackDefinition{}).
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
