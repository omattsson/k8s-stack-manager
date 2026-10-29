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
var _ models.StackInstanceRepository = (*GORMStackInstanceRepository)(nil)
var _ models.InstanceErrorMarker = (*GORMStackInstanceRepository)(nil)

// GORMStackInstanceRepository implements models.StackInstanceRepository using GORM.
type GORMStackInstanceRepository struct {
	db *gorm.DB
}

// NewGORMStackInstanceRepository creates a new GORM-backed stack instance repository.
func NewGORMStackInstanceRepository(db *gorm.DB) *GORMStackInstanceRepository {
	return &GORMStackInstanceRepository{db: db}
}

// Create inserts a new stack instance record.
func (r *GORMStackInstanceRepository) Create(ctx context.Context, instance *models.StackInstance) error {
	if instance.ID == "" {
		instance.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	instance.CreatedAt = now
	instance.UpdatedAt = now
	if err := r.db.WithContext(ctx).Create(instance).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns a stack instance by its ID.
func (r *GORMStackInstanceRepository) FindByID(ctx context.Context, id string) (*models.StackInstance, error) {
	var instance models.StackInstance
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&instance).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &instance, nil
}

// instanceIDsPerQuery bounds the IN list of one FindByIDs query.
const instanceIDsPerQuery = 500

// FindByIDs returns the instances with these IDs that exist, keyed by ID. It
// selects only id, owner_id, status, namespace and cluster_id.
func (r *GORMStackInstanceRepository) FindByIDs(ctx context.Context, ids []string) (map[string]*models.StackInstance, error) {
	out := make(map[string]*models.StackInstance, len(ids))
	for start := 0; start < len(ids); start += instanceIDsPerQuery {
		end := min(start+instanceIDsPerQuery, len(ids))
		var rows []models.StackInstance
		if err := r.db.WithContext(ctx).Select("id, owner_id, status, namespace, cluster_id").
			Where("id IN ?", ids[start:end]).Find(&rows).Error; err != nil {
			return nil, dberrors.NewDatabaseError("find_by_ids", err)
		}
		for i := range rows {
			out[rows[i].ID] = &rows[i]
		}
	}
	return out, nil
}

// FindByNamespace returns the stack instance occupying the given namespace.
func (r *GORMStackInstanceRepository) FindByNamespace(ctx context.Context, namespace string) (*models.StackInstance, error) {
	var instance models.StackInstance
	if err := r.db.WithContext(ctx).Where("namespace = ?", namespace).First(&instance).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_namespace", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_namespace", err)
	}
	return &instance, nil
}

// columnExpiryWarnedAt is written only by MarkExpiryWarned and by the clear
// step in Update.
const columnExpiryWarnedAt = "expiry_warned_at"

// columnDeleteAfterClean is written only by MarkCleanStart and
// ClearDeleteAfterClean, never by Update.
const columnDeleteAfterClean = "delete_after_clean"

// expiryMatchTolerance is the tolerance for "the same expiry time". The
// database can store expires_at with less precision than time.Time.
const expiryMatchTolerance = time.Second

// Update persists changes to an existing stack instance.
//
// Update never writes expiry_warned_at: a copy of the instance that was read
// before the expiry warning must not reset the mark. When Update changes
// expires_at (deploy, extend, TTL change), it clears the mark in the same
// transaction, so the new expiry time gets a new warning.
func (r *GORMStackInstanceRepository) Update(ctx context.Context, instance *models.StackInstance) error {
	instance.UpdatedAt = time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := clearExpiryWarningIfExpiryChanged(tx, instance); err != nil {
			return err
		}
		return tx.Omit(columnExpiryWarnedAt, columnDeleteAfterClean).Save(instance).Error
	})
	if err != nil {
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

// clearExpiryWarningIfExpiryChanged clears expiry_warned_at when the stored
// expires_at differs from instance.ExpiresAt by at least
// expiryMatchTolerance (or one of them is NULL and the other is not).
func clearExpiryWarningIfExpiryChanged(tx *gorm.DB, instance *models.StackInstance) error {
	q := tx.Model(&models.StackInstance{}).
		Where("id = ? AND expiry_warned_at IS NOT NULL", instance.ID)
	if instance.ExpiresAt == nil {
		q = q.Where("expires_at IS NOT NULL")
	} else {
		exp := instance.ExpiresAt.UTC()
		q = q.Where("(expires_at IS NULL OR expires_at <= ? OR expires_at >= ?)",
			exp.Add(-expiryMatchTolerance), exp.Add(expiryMatchTolerance))
	}
	return q.UpdateColumn(columnExpiryWarnedAt, nil).Error
}

// MarkCleanStart starts a clean with one conditional update: status
// cleaning and an empty error message, only when the status is one of
// fromStatuses. With deleteAfterClean it also sets delete_after_clean: of
// two concurrent deletes only one matches, the other gets
// models.ErrDeleteConflict. Without it the row must not have
// delete_after_clean (a plain clean never overwrites a delete); else
// models.ErrCleanConflict.
func (r *GORMStackInstanceRepository) MarkCleanStart(ctx context.Context, id string, fromStatuses []string, deleteAfterClean bool) error {
	updates := map[string]any{
		"status":        models.StackStatusCleaning,
		"error_message": "",
		"updated_at":    time.Now().UTC(),
	}
	q := r.db.WithContext(ctx).Model(&models.StackInstance{}).Where("id = ? AND status IN ?", id, fromStatuses)
	conflict := models.ErrDeleteConflict
	if deleteAfterClean {
		updates[columnDeleteAfterClean] = true
	} else {
		q = q.Where(columnDeleteAfterClean+" = ?", false)
		conflict = models.ErrCleanConflict
	}
	res := q.Updates(updates)
	if res.Error != nil {
		return dberrors.NewDatabaseError("mark_clean_start", res.Error)
	}
	if res.RowsAffected == 0 {
		return conflict
	}
	return nil
}

// MarkNamespaceError implements models.InstanceErrorMarker: one conditional
// update, so a deploy, stop or clean that started after the read of the
// status watcher keeps its status.
func (r *GORMStackInstanceRepository) MarkNamespaceError(ctx context.Context, id, message string, now time.Time) (bool, error) {
	res := r.db.WithContext(ctx).Model(&models.StackInstance{}).
		Where("id = ?", id).
		Where("status IN ? OR (status = ? AND (post_deploy_hook_until IS NULL OR post_deploy_hook_until < ?))",
			[]string{models.StackStatusRunning, models.StackStatusPartial}, models.StackStatusStabilizing, now.UTC()).
		Updates(map[string]any{
			"status":        models.StackStatusError,
			"error_message": message,
			"updated_at":    time.Now().UTC(),
		})
	if res.Error != nil {
		return false, dberrors.NewDatabaseError("mark_namespace_error", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// ClearDeleteAfterClean clears delete_after_clean of the instance.
func (r *GORMStackInstanceRepository) ClearDeleteAfterClean(ctx context.Context, id string) error {
	if err := r.db.WithContext(ctx).Model(&models.StackInstance{}).Where("id = ?", id).
		UpdateColumn(columnDeleteAfterClean, false).Error; err != nil {
		return dberrors.NewDatabaseError("clear_delete_after_clean", err)
	}
	return nil
}

// MarkExpiryWarned sets expiry_warned_at when the instance has no expiry
// warning and its expires_at still matches expiresAt. The conditional update
// makes sure that only one caller gets true, also when two replicas run the
// expiry warner at the same time.
func (r *GORMStackInstanceRepository) MarkExpiryWarned(ctx context.Context, id string, expiresAt, warnedAt time.Time) (bool, error) {
	exp := expiresAt.UTC()
	res := r.db.WithContext(ctx).Model(&models.StackInstance{}).
		Where("id = ? AND expiry_warned_at IS NULL AND expires_at > ? AND expires_at < ?",
			id, exp.Add(-expiryMatchTolerance), exp.Add(expiryMatchTolerance)).
		UpdateColumn(columnExpiryWarnedAt, warnedAt.UTC())
	if res.Error != nil {
		return false, dberrors.NewDatabaseError("mark_expiry_warned", res.Error)
	}
	return res.RowsAffected == 1, nil
}

// Delete removes a stack instance by ID.
func (r *GORMStackInstanceRepository) Delete(ctx context.Context, id string) error {
	result := r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.StackInstance{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// List returns all stack instances.
func (r *GORMStackInstanceRepository) List(ctx context.Context) ([]models.StackInstance, error) {
	var instances []models.StackInstance
	if err := r.db.WithContext(ctx).Find(&instances).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}
	return instances, nil
}

// listColumns are the columns fetched by ListPaged. The heavy TEXT columns
// error_message and last_deployed_values are omitted because they are only needed in the detail view.
var listColumns = []string{
	"id", "name", "namespace", "owner_id", "stack_definition_id",
	"branch", "cluster_id", "status", "ttl_minutes",
	"created_at", "updated_at", "last_deployed_at", "expires_at", "stopped_at", "cleaned_at",
}

// applyInstanceFilter adds a WHERE condition for each non-empty field of f.
// ListPaged uses it for the count query and for the page query, so the total
// always matches the filtered rows. Each filtered column has an index.
func applyInstanceFilter(q *gorm.DB, f models.StackInstanceFilter) *gorm.DB {
	if f.Name != "" {
		q = q.Where("name = ?", f.Name)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.ClusterID != "" {
		q = q.Where("cluster_id = ?", f.ClusterID)
	}
	if f.DefinitionID != "" {
		q = q.Where("stack_definition_id = ?", f.DefinitionID)
	}
	if f.OwnerID != "" {
		q = q.Where("owner_id = ?", f.OwnerID)
	}
	return q
}

// ListPaged returns the stack instances that match filter with limit/offset
// pagination, and the total count of matching instances. It selects only
// list-view columns, omitting large TEXT fields like error_message.
func (r *GORMStackInstanceRepository) ListPaged(ctx context.Context, filter models.StackInstanceFilter, limit, offset int) ([]models.StackInstance, int, error) {
	var total int64
	if err := applyInstanceFilter(r.db.WithContext(ctx).Model(&models.StackInstance{}), filter).Count(&total).Error; err != nil {
		return nil, 0, dberrors.NewDatabaseError("count", err)
	}

	instances := []models.StackInstance{}
	if err := applyInstanceFilter(r.db.WithContext(ctx).Select(listColumns), filter).
		Order("created_at DESC").Limit(limit).Offset(offset).Find(&instances).Error; err != nil {
		return nil, 0, dberrors.NewDatabaseError("list_paged", err)
	}
	return instances, int(total), nil
}

// ListByOwner returns all stack instances owned by the given user.
func (r *GORMStackInstanceRepository) ListByOwner(ctx context.Context, ownerID string) ([]models.StackInstance, error) {
	var instances []models.StackInstance
	if err := r.db.WithContext(ctx).Where("owner_id = ?", ownerID).Find(&instances).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_owner", err)
	}
	return instances, nil
}

// FindByName returns all stack instances with the given exact name.
func (r *GORMStackInstanceRepository) FindByName(ctx context.Context, name string) ([]models.StackInstance, error) {
	var instances []models.StackInstance
	if err := r.db.WithContext(ctx).Select(listColumns).Where("name = ?", name).Find(&instances).Error; err != nil {
		return nil, dberrors.NewDatabaseError("find_by_name", err)
	}
	return instances, nil
}

// GORMStackInstanceRepository implements the lookup of UNIQUE_INSTANCE_NAMES.
var _ models.InstanceNameLookup = (*GORMStackInstanceRepository)(nil)

// FindByNameOnCluster returns the stack instances that use name on clusterID.
// It compares the names without case (LOWER on both sides, so the result does
// not depend on the column collation). With
// includeUnassigned it also returns the instances without a cluster.
func (r *GORMStackInstanceRepository) FindByNameOnCluster(ctx context.Context, name, clusterID string, includeUnassigned bool) ([]models.StackInstance, error) {
	var instances []models.StackInstance
	q := r.db.WithContext(ctx).Select(listColumns).Where("LOWER(name) = LOWER(?)", name)
	if includeUnassigned {
		q = q.Where("(cluster_id = ? OR cluster_id = '' OR cluster_id IS NULL)", clusterID)
	} else {
		q = q.Where("cluster_id = ?", clusterID)
	}
	if err := q.Order("created_at ASC").Find(&instances).Error; err != nil {
		return nil, dberrors.NewDatabaseError("find_by_name_on_cluster", err)
	}
	return instances, nil
}

// DuplicateInstanceNames returns the names that more than one instance uses on
// the same cluster, compared without case. Instances without a cluster count
// as defaultClusterID.
func (r *GORMStackInstanceRepository) DuplicateInstanceNames(ctx context.Context, defaultClusterID string) ([]models.DuplicateInstanceName, error) {
	var rows []models.DuplicateInstanceName
	err := r.db.WithContext(ctx).Raw(`SELECT cid AS cluster_id, lname AS name, COUNT(*) AS count
		FROM (
			SELECT CASE WHEN cluster_id IS NULL OR cluster_id = '' THEN ? ELSE cluster_id END AS cid,
				LOWER(name) AS lname
			FROM stack_instances
		) AS named
		GROUP BY cid, lname
		HAVING COUNT(*) > 1
		ORDER BY cid, lname`, defaultClusterID).Scan(&rows).Error
	if err != nil {
		return nil, dberrors.NewDatabaseError("duplicate_instance_names", err)
	}
	return rows, nil
}

// FindByCluster returns all stack instances targeting the given cluster.
func (r *GORMStackInstanceRepository) FindByCluster(ctx context.Context, clusterID string) ([]models.StackInstance, error) {
	var instances []models.StackInstance
	if err := r.db.WithContext(ctx).Where("cluster_id = ?", clusterID).Find(&instances).Error; err != nil {
		return nil, dberrors.NewDatabaseError("find_by_cluster", err)
	}
	return instances, nil
}

// CountForInstanceLimit locks the user row of ownerID and counts the
// instances of ownerID on clusterID (with includeUnassigned also those without
// a cluster). The lock is on the user row, not on the counted rows: a
// COUNT ... FOR UPDATE takes gap locks and can deadlock concurrent inserts.
// Outside a transaction the lock ends with the statement.
func (r *GORMStackInstanceRepository) CountForInstanceLimit(ctx context.Context, clusterID, ownerID string, includeUnassigned bool) (int, error) {
	db := r.db.WithContext(ctx)
	var locked []string
	if err := db.Model(&models.User{}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", ownerID).Pluck("id", &locked).Error; err != nil {
		return 0, withContextErr(ctx, "count_for_instance_limit", dberrors.NewDatabaseError("count_for_instance_limit", err))
	}
	q := db.Model(&models.StackInstance{}).Where("owner_id = ?", ownerID)
	if includeUnassigned {
		q = q.Where("(cluster_id = ? OR cluster_id = '' OR cluster_id IS NULL)", clusterID)
	} else {
		q = q.Where("cluster_id = ?", clusterID)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return 0, withContextErr(ctx, "count_for_instance_limit", dberrors.NewDatabaseError("count_for_instance_limit", err))
	}
	return int(count), nil
}

// CountAll returns the total number of stack instances.
func (r *GORMStackInstanceRepository) CountAll(ctx context.Context) (int, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.StackInstance{}).Count(&count).Error; err != nil {
		return 0, dberrors.NewDatabaseError("count_all", err)
	}
	return int(count), nil
}

// CountByStatus returns the number of stack instances with the given status.
func (r *GORMStackInstanceRepository) CountByStatus(ctx context.Context, status string) (int, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.StackInstance{}).
		Where("status = ?", status).
		Count(&count).Error; err != nil {
		return 0, dberrors.NewDatabaseError("count_by_status", err)
	}
	return int(count), nil
}

// CountByStatuses returns the number of stack instances whose status is any of
// the given statuses, in a single aggregate query. Using one WHERE status IN
// (...) query keeps the count consistent even if statuses change concurrently.
func (r *GORMStackInstanceRepository) CountByStatuses(ctx context.Context, statuses []string) (int, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.StackInstance{}).
		Where("status IN ?", statuses).
		Count(&count).Error; err != nil {
		return 0, dberrors.NewDatabaseError("count_by_statuses", err)
	}
	return int(count), nil
}

// ExistsByDefinitionAndStatus checks whether any instance exists for a given definition+status.
func (r *GORMStackInstanceRepository) ExistsByDefinitionAndStatus(ctx context.Context, definitionID, status string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.StackInstance{}).
		Where("stack_definition_id = ? AND status = ?", definitionID, status).
		Count(&count).Error; err != nil {
		return false, dberrors.NewDatabaseError("exists_by_definition_and_status", err)
	}
	return count > 0, nil
}

// ListExpired returns running instances whose ExpiresAt is in the past.
func (r *GORMStackInstanceRepository) ListExpired(ctx context.Context) ([]*models.StackInstance, error) {
	var instances []*models.StackInstance
	now := time.Now().UTC()
	if err := r.db.WithContext(ctx).Where("status IN ? AND expires_at IS NOT NULL AND expires_at < ?",
		[]string{models.StackStatusRunning, models.StackStatusPartial}, now).
		Find(&instances).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_expired", err)
	}
	return instances, nil
}

// ListExpiringSoon returns running/partial instances whose ExpiresAt is within the given
// threshold from now (i.e., will expire soon but haven't expired yet) and
// that have no expiry warning yet (expiry_warned_at IS NULL).
func (r *GORMStackInstanceRepository) ListExpiringSoon(ctx context.Context, threshold time.Duration) ([]*models.StackInstance, error) {
	now := time.Now().UTC()
	deadline := now.Add(threshold)
	var instances []*models.StackInstance
	if err := r.db.WithContext(ctx).Where("status IN ? AND expires_at IS NOT NULL AND expires_at > ? AND expires_at <= ? AND expiry_warned_at IS NULL",
		[]string{models.StackStatusRunning, models.StackStatusPartial}, now, deadline).
		Order("expires_at ASC").
		Find(&instances).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_expiring_soon", err)
	}
	return instances, nil
}

// ListByStatus returns instances with the given status, up to limit rows.
// A limit <= 0 returns all matching rows.
func (r *GORMStackInstanceRepository) ListByStatus(ctx context.Context, status string, limit int) ([]*models.StackInstance, error) {
	var instances []*models.StackInstance
	q := r.db.WithContext(ctx).Where("status = ?", status).Order("updated_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&instances).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_status", err)
	}
	return instances, nil
}

// CountByDefinitionIDs returns a map of definition ID to instance count for the
// given definition IDs in a single GROUP BY query. IDs are processed in chunks
// of 500 to stay within MySQL's IN clause limits.
func (r *GORMStackInstanceRepository) CountByDefinitionIDs(ctx context.Context, definitionIDs []string) (map[string]int, error) {
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
		if err := r.db.WithContext(ctx).Model(&models.StackInstance{}).
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
func (r *GORMStackInstanceRepository) CountByOwnerIDs(ctx context.Context, ownerIDs []string) (map[string]int, error) {
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
		if err := r.db.WithContext(ctx).Model(&models.StackInstance{}).
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
func (r *GORMStackInstanceRepository) ListIDsByDefinitionIDs(ctx context.Context, definitionIDs []string) (map[string][]string, error) {
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
		if err := r.db.WithContext(ctx).Model(&models.StackInstance{}).
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
func (r *GORMStackInstanceRepository) ListIDsByOwnerIDs(ctx context.Context, ownerIDs []string) (map[string][]string, error) {
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
		if err := r.db.WithContext(ctx).Model(&models.StackInstance{}).
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
