package database

import (
	"errors"
	"fmt"
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
	if err := config.Validate(); err != nil {
		return dberrors.NewDatabaseError("create", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
	}

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

// CountByDefinitionIDs returns the number of chart configs per definition in
// one grouped query. Definitions without charts are not in the map.
func (r *GORMChartConfigRepository) CountByDefinitionIDs(definitionIDs []string) (map[string]int, error) {
	result := make(map[string]int, len(definitionIDs))
	if len(definitionIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		StackDefinitionID string
		Count             int
	}
	if err := r.db.Model(&models.ChartConfig{}).
		Select("stack_definition_id, COUNT(*) as count").
		Where("stack_definition_id IN ?", definitionIDs).
		Group("stack_definition_id").
		Find(&rows).Error; err != nil {
		return nil, dberrors.NewDatabaseError("count_by_definition_ids", err)
	}
	for _, row := range rows {
		result[row.StackDefinitionID] = row.Count
	}
	return result, nil
}

// DeleteByDefinition deletes all chart configs of a definition in one statement.
func (r *GORMChartConfigRepository) DeleteByDefinition(definitionID string) error {
	if err := r.db.Where("stack_definition_id = ?", definitionID).Delete(&models.ChartConfig{}).Error; err != nil {
		return dberrors.NewDatabaseError("delete_by_definition", err)
	}
	return nil
}
