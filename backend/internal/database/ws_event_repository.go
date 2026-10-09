package database

import (
	"context"
	"time"

	"backend/internal/models"

	"gorm.io/gorm"
)

// wsEventColumns selects the event columns. The payload of the rows of one
// origin (the caller's own rows) is replaced by an empty string, so a poll
// does not read back the large payloads (deploy log lines) the replica wrote
// itself. CASE works in MySQL and SQLite.
const wsEventColumns = "id, created_at, target, origin, CASE WHEN origin = ? THEN '' ELSE payload END AS payload"

// GORMWSEventRepository implements models.WSEventRepository using GORM.
type GORMWSEventRepository struct {
	db *gorm.DB
}

var _ models.WSEventRepository = (*GORMWSEventRepository)(nil)

// NewGORMWSEventRepository creates a GORM-backed WebSocket event repository.
func NewGORMWSEventRepository(db *gorm.DB) *GORMWSEventRepository {
	return &GORMWSEventRepository{db: db}
}

// Insert stores the events in one INSERT statement. An empty slice does
// nothing.
func (r *GORMWSEventRepository) Insert(ctx context.Context, events []*models.WSEvent) error {
	if len(events) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&events).Error
}

// MaxID returns the highest event ID, or 0 for an empty table.
func (r *GORMWSEventRepository) MaxID(ctx context.Context) (int64, error) {
	var maxID *int64
	if err := r.db.WithContext(ctx).Model(&models.WSEvent{}).Select("MAX(id)").Scan(&maxID).Error; err != nil {
		return 0, err
	}
	if maxID == nil {
		return 0, nil
	}
	return *maxID, nil
}

// ListAfter returns at most limit events with id > afterID in ID order.
func (r *GORMWSEventRepository) ListAfter(ctx context.Context, afterID int64, limit int, skipPayloadOrigin string) ([]models.WSEvent, error) {
	var events []models.WSEvent
	err := r.db.WithContext(ctx).Model(&models.WSEvent{}).
		Select(wsEventColumns, skipPayloadOrigin).
		Where("id > ?", afterID).
		Order("id ASC").
		Limit(limit).
		Find(&events).Error
	return events, err
}

// IDsAfter returns at most limit IDs greater than afterID in ID order.
func (r *GORMWSEventRepository) IDsAfter(ctx context.Context, afterID int64, limit int) ([]int64, error) {
	var ids []int64
	err := r.db.WithContext(ctx).Model(&models.WSEvent{}).
		Where("id > ?", afterID).
		Order("id ASC").
		Limit(limit).
		Pluck("id", &ids).Error
	return ids, err
}

// ListByIDs returns the existing events with these IDs in ID order.
func (r *GORMWSEventRepository) ListByIDs(ctx context.Context, ids []int64, skipPayloadOrigin string) ([]models.WSEvent, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var events []models.WSEvent
	err := r.db.WithContext(ctx).Model(&models.WSEvent{}).
		Select(wsEventColumns, skipPayloadOrigin).
		Where("id IN ?", ids).
		Order("id ASC").
		Find(&events).Error
	return events, err
}

// defaultWSEventDeleteBatch is the batch size when DeleteOlderThan gets a
// batch size <= 0.
const defaultWSEventDeleteBatch = 5000

// DeleteOlderThan deletes the events created before t in batches. Each batch
// reads the IDs of up to batchSize old rows in ID order, then deletes the
// old rows up to the highest of these IDs with a primary key range. This
// works on MySQL and SQLite (no DELETE ... LIMIT) and keeps each statement
// short. The created_at condition stays in the DELETE, because created_at is
// not strictly in ID order across replicas.
func (r *GORMWSEventRepository) DeleteOlderThan(ctx context.Context, t time.Time, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = defaultWSEventDeleteBatch
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var ids []int64
		if err := r.db.WithContext(ctx).Model(&models.WSEvent{}).
			Where("created_at < ?", t).
			Order("id ASC").
			Limit(batchSize).
			Pluck("id", &ids).Error; err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		res := r.db.WithContext(ctx).
			Where("id <= ? AND created_at < ?", ids[len(ids)-1], t).
			Delete(&models.WSEvent{})
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if len(ids) < batchSize {
			return total, nil
		}
	}
}
