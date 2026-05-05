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
var _ models.StackInstanceRepository = (*GORMStackInstanceRepository)(nil)

// GORMStackInstanceRepository implements models.StackInstanceRepository using GORM.
type GORMStackInstanceRepository struct {
	db *gorm.DB
}

// NewGORMStackInstanceRepository creates a new GORM-backed stack instance repository.
func NewGORMStackInstanceRepository(db *gorm.DB) *GORMStackInstanceRepository {
	return &GORMStackInstanceRepository{db: db}
}

// Create inserts a new stack instance record.
func (r *GORMStackInstanceRepository) Create(instance *models.StackInstance) error {
	if instance.ID == "" {
		instance.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	instance.CreatedAt = now
	instance.UpdatedAt = now
	if err := r.db.Create(instance).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns a stack instance by its ID.
func (r *GORMStackInstanceRepository) FindByID(id string) (*models.StackInstance, error) {
	var instance models.StackInstance
	if err := r.db.Where("id = ?", id).First(&instance).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &instance, nil
}

// FindByNamespace returns the stack instance occupying the given namespace.
func (r *GORMStackInstanceRepository) FindByNamespace(namespace string) (*models.StackInstance, error) {
	var instance models.StackInstance
	if err := r.db.Where("namespace = ?", namespace).First(&instance).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_namespace", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_namespace", err)
	}
	return &instance, nil
}

// Update persists changes to an existing stack instance.
func (r *GORMStackInstanceRepository) Update(instance *models.StackInstance) error {
	instance.UpdatedAt = time.Now().UTC()
	if err := r.db.Save(instance).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("update", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("update", err)
	}
	return nil
}

// Delete removes a stack instance by ID.
func (r *GORMStackInstanceRepository) Delete(id string) error {
	result := r.db.Where("id = ?", id).Delete(&models.StackInstance{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// List returns all stack instances.
func (r *GORMStackInstanceRepository) List() ([]models.StackInstance, error) {
	var instances []models.StackInstance
	if err := r.db.Find(&instances).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}
	return instances, nil
}

// listColumns are the columns fetched by ListPaged. The heavy TEXT columns
// error_message and last_deployed_values are omitted because they are only needed in the detail view.
var listColumns = []string{
	"id", "name", "namespace", "owner_id", "stack_definition_id",
	"branch", "cluster_id", "status", "ttl_minutes",
	"created_at", "updated_at", "last_deployed_at", "expires_at",
}

// ListPaged returns stack instances with limit/offset pagination and total count.
// It selects only list-view columns, omitting large TEXT fields like error_message.
func (r *GORMStackInstanceRepository) ListPaged(limit, offset int) ([]models.StackInstance, int, error) {
	var total int64
	if err := r.db.Model(&models.StackInstance{}).Count(&total).Error; err != nil {
		return nil, 0, dberrors.NewDatabaseError("count", err)
	}

	var instances []models.StackInstance
	if err := r.db.Select(listColumns).
		Order("created_at DESC").Limit(limit).Offset(offset).Find(&instances).Error; err != nil {
		return nil, 0, dberrors.NewDatabaseError("list_paged", err)
	}
	return instances, int(total), nil
}

// ListByOwner returns all stack instances owned by the given user.
func (r *GORMStackInstanceRepository) ListByOwner(ownerID string) ([]models.StackInstance, error) {
	var instances []models.StackInstance
	if err := r.db.Where("owner_id = ?", ownerID).Find(&instances).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_owner", err)
	}
	return instances, nil
}

// FindByName returns all stack instances with the given exact name.
func (r *GORMStackInstanceRepository) FindByName(name string) ([]models.StackInstance, error) {
	var instances []models.StackInstance
	if err := r.db.Select(listColumns).Where("name = ?", name).Find(&instances).Error; err != nil {
		return nil, dberrors.NewDatabaseError("find_by_name", err)
	}
	return instances, nil
}

// FindByCluster returns all stack instances targeting the given cluster.
func (r *GORMStackInstanceRepository) FindByCluster(clusterID string) ([]models.StackInstance, error) {
	var instances []models.StackInstance
	if err := r.db.Where("cluster_id = ?", clusterID).Find(&instances).Error; err != nil {
		return nil, dberrors.NewDatabaseError("find_by_cluster", err)
	}
	return instances, nil
}

// CountByClusterAndOwner returns the number of instances for a cluster+owner combination.
func (r *GORMStackInstanceRepository) CountByClusterAndOwner(clusterID, ownerID string) (int, error) {
	var count int64
	if err := r.db.Model(&models.StackInstance{}).
		Where("cluster_id = ? AND owner_id = ?", clusterID, ownerID).
		Count(&count).Error; err != nil {
		return 0, dberrors.NewDatabaseError("count_by_cluster_and_owner", err)
	}
	return int(count), nil
}

// CountAll returns the total number of stack instances.
func (r *GORMStackInstanceRepository) CountAll() (int, error) {
	var count int64
	if err := r.db.Model(&models.StackInstance{}).Count(&count).Error; err != nil {
		return 0, dberrors.NewDatabaseError("count_all", err)
	}
	return int(count), nil
}

// CountByStatus returns the number of stack instances with the given status.
func (r *GORMStackInstanceRepository) CountByStatus(status string) (int, error) {
	var count int64
	if err := r.db.Model(&models.StackInstance{}).
		Where("status = ?", status).
		Count(&count).Error; err != nil {
		return 0, dberrors.NewDatabaseError("count_by_status", err)
	}
	return int(count), nil
}

// ExistsByDefinitionAndStatus checks whether any instance exists for a given definition+status.
func (r *GORMStackInstanceRepository) ExistsByDefinitionAndStatus(definitionID, status string) (bool, error) {
	var count int64
	if err := r.db.Model(&models.StackInstance{}).
		Where("stack_definition_id = ? AND status = ?", definitionID, status).
		Count(&count).Error; err != nil {
		return false, dberrors.NewDatabaseError("exists_by_definition_and_status", err)
	}
	return count > 0, nil
}

// ListExpired returns running instances whose ExpiresAt is in the past.
func (r *GORMStackInstanceRepository) ListExpired() ([]*models.StackInstance, error) {
	var instances []*models.StackInstance
	now := time.Now().UTC()
	if err := r.db.Where("status IN ? AND expires_at IS NOT NULL AND expires_at < ?",
		[]string{models.StackStatusRunning, models.StackStatusPartial}, now).
		Find(&instances).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_expired", err)
	}
	return instances, nil
}

// ListExpiringSoon returns running/partial instances whose ExpiresAt is within the given
// threshold from now (i.e., will expire soon but haven't expired yet).
func (r *GORMStackInstanceRepository) ListExpiringSoon(threshold time.Duration) ([]*models.StackInstance, error) {
	now := time.Now().UTC()
	deadline := now.Add(threshold)
	var instances []*models.StackInstance
	if err := r.db.Where("status IN ? AND expires_at IS NOT NULL AND expires_at > ? AND expires_at <= ?",
		[]string{models.StackStatusRunning, models.StackStatusPartial}, now, deadline).
		Find(&instances).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_expiring_soon", err)
	}
	return instances, nil
}

// CountByDefinitionIDs returns a map of definition ID to instance count for the
// given definition IDs in a single GROUP BY query. IDs are processed in chunks
// of 500 to stay within MySQL's IN clause limits.
func (r *GORMStackInstanceRepository) CountByDefinitionIDs(definitionIDs []string) (map[string]int, error) {
	if len(definitionIDs) == 0 {
		return make(map[string]int), nil
	}
	result := make(map[string]int, len(definitionIDs))
	const chunkSize = 500
	for start := 0; start < len(definitionIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(definitionIDs) {
			end = len(definitionIDs)
		}
		chunk := definitionIDs[start:end]

		type countRow struct {
			StackDefinitionID string
			Count             int
		}
		var rows []countRow
		if err := r.db.Model(&models.StackInstance{}).
			Select("stack_definition_id, COUNT(*) as count").
			Where("stack_definition_id IN ?", chunk).
			Group("stack_definition_id").
			Find(&rows).Error; err != nil {
			return nil, dberrors.NewDatabaseError("count_by_definition_ids", err)
		}
		for _, row := range rows {
			result[row.StackDefinitionID] = row.Count
		}
	}
	return result, nil
}

// CountByOwnerIDs returns a map of owner ID to instance count for the given
// owner IDs in a single GROUP BY query. IDs are processed in chunks of 500
// to stay within MySQL's IN clause limits.
func (r *GORMStackInstanceRepository) CountByOwnerIDs(ownerIDs []string) (map[string]int, error) {
	if len(ownerIDs) == 0 {
		return make(map[string]int), nil
	}
	result := make(map[string]int, len(ownerIDs))
	const chunkSize = 500
	for start := 0; start < len(ownerIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(ownerIDs) {
			end = len(ownerIDs)
		}
		chunk := ownerIDs[start:end]

		type countRow struct {
			OwnerID string
			Count   int
		}
		var rows []countRow
		if err := r.db.Model(&models.StackInstance{}).
			Select("owner_id, COUNT(*) as count").
			Where("owner_id IN ?", chunk).
			Group("owner_id").
			Find(&rows).Error; err != nil {
			return nil, dberrors.NewDatabaseError("count_by_owner_ids", err)
		}
		for _, row := range rows {
			result[row.OwnerID] = row.Count
		}
	}
	return result, nil
}

// ListIDsByDefinitionIDs returns a map of definition ID to instance IDs,
// selecting only the id and stack_definition_id columns for efficiency.
// IDs are processed in chunks of 500 to stay within MySQL's IN clause limits.
func (r *GORMStackInstanceRepository) ListIDsByDefinitionIDs(definitionIDs []string) (map[string][]string, error) {
	if len(definitionIDs) == 0 {
		return make(map[string][]string), nil
	}
	result := make(map[string][]string, len(definitionIDs))
	const chunkSize = 500
	for start := 0; start < len(definitionIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(definitionIDs) {
			end = len(definitionIDs)
		}
		chunk := definitionIDs[start:end]

		type idRow struct {
			ID                string
			StackDefinitionID string
		}
		var rows []idRow
		if err := r.db.Model(&models.StackInstance{}).
			Select("id, stack_definition_id").
			Where("stack_definition_id IN ?", chunk).
			Find(&rows).Error; err != nil {
			return nil, dberrors.NewDatabaseError("list_ids_by_definition_ids", err)
		}
		for _, row := range rows {
			result[row.StackDefinitionID] = append(result[row.StackDefinitionID], row.ID)
		}
	}
	return result, nil
}

// ListIDsByOwnerIDs returns a map of owner ID to instance IDs, selecting only
// the id and owner_id columns for efficiency. IDs are processed in chunks of
// 500 to stay within MySQL's IN clause limits.
func (r *GORMStackInstanceRepository) ListIDsByOwnerIDs(ownerIDs []string) (map[string][]string, error) {
	if len(ownerIDs) == 0 {
		return make(map[string][]string), nil
	}
	result := make(map[string][]string, len(ownerIDs))
	const chunkSize = 500
	for start := 0; start < len(ownerIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(ownerIDs) {
			end = len(ownerIDs)
		}
		chunk := ownerIDs[start:end]

		type idRow struct {
			ID      string
			OwnerID string
		}
		var rows []idRow
		if err := r.db.Model(&models.StackInstance{}).
			Select("id, owner_id").
			Where("owner_id IN ?", chunk).
			Find(&rows).Error; err != nil {
			return nil, dberrors.NewDatabaseError("list_ids_by_owner_ids", err)
		}
		for _, row := range rows {
			result[row.OwnerID] = append(result[row.OwnerID], row.ID)
		}
	}
	return result, nil
}
