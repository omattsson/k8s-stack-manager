package models

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"backend/pkg/dberrors"

	"gorm.io/gorm"
)

// Base model with common fields
type Base struct {
	ID        uint       `gorm:"primarykey" json:"id" example:"1" description:"@Description Unique identifier"`
	CreatedAt time.Time  `json:"created_at" example:"2025-06-02T10:00:00Z" description:"@Description Creation timestamp"`
	UpdatedAt time.Time  `json:"updated_at" example:"2025-06-02T10:00:00Z" description:"@Description Last update timestamp"`
	DeletedAt *time.Time `gorm:"index" json:"deleted_at,omitempty" format:"date-time" description:"@Description Soft delete timestamp"`
}

// Item represents a basic item in the system
type Item struct {
	Base
	Name    string  `gorm:"size:255;not null" json:"name"`
	Price   float64 `json:"price"`
	Version uint    `gorm:"not null;default:1" json:"version"` // For optimistic locking (1 = initial; 0 = not provided)
}

// Validator is an interface for model validation
type Validator interface {
	Validate() error
}

// Versionable is an interface for models that support optimistic locking.
type Versionable interface {
	GetVersion() uint
	SetVersion(v uint)
}

// GetVersion implements Versionable for Item.
func (i *Item) GetVersion() uint { return i.Version }

// SetVersion implements Versionable for Item.
func (i *Item) SetVersion(v uint) { i.Version = v }

// Repository defines the interface for database operations.
type Repository interface {
	Create(ctx context.Context, entity interface{}) error
	FindByID(ctx context.Context, id uint, dest interface{}) error
	Update(ctx context.Context, entity interface{}) error
	Delete(ctx context.Context, entity interface{}) error
	List(ctx context.Context, dest interface{}, conditions ...interface{}) error
	Ping(ctx context.Context) error
	Close() error
}

// GenericRepository implements the Repository interface
type GenericRepository struct {
	db                  *gorm.DB
	allowedFilterFields map[string]bool
}

// NewRepository creates a new GenericRepository with filter fields for the Item
// entity ("name", "price"). For other entity types, use NewRepositoryWithFilterFields.
func NewRepository(db *gorm.DB) Repository {
	return &GenericRepository{
		db: db,
		allowedFilterFields: map[string]bool{
			"name":  true,
			"price": true,
		},
	}
}

// NewRepositoryWithFilterFields creates a GenericRepository with a custom filter field whitelist.
func NewRepositoryWithFilterFields(db *gorm.DB, fields []string) Repository {
	allowed := make(map[string]bool, len(fields))
	for _, f := range fields {
		allowed[f] = true
	}
	return &GenericRepository{db: db, allowedFilterFields: allowed}
}

// Ping checks if the database is reachable
func (r *GenericRepository) Ping(ctx context.Context) error {
	sqlDB, err := r.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// Close releases the underlying database connection pool.
func (r *GenericRepository) Close() error {
	sqlDB, err := r.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func (r *GenericRepository) Create(ctx context.Context, entity interface{}) error {
	if v, ok := entity.(Validator); ok {
		if err := v.Validate(); err != nil {
			return dberrors.NewDatabaseError("validate",
				fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
		}
	}

	if err := r.db.WithContext(ctx).Create(entity).Error; err != nil {
		return r.handleError("create", err)
	}
	return nil
}

func (r *GenericRepository) FindByID(ctx context.Context, id uint, dest interface{}) error {
	if err := r.db.WithContext(ctx).First(dest, id).Error; err != nil {
		return r.handleError("find", err)
	}
	return nil
}

func (r *GenericRepository) Update(ctx context.Context, entity interface{}) error {
	if v, ok := entity.(Validator); ok {
		if err := v.Validate(); err != nil {
			return dberrors.NewDatabaseError("validate",
				fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
		}
	}

	// Optimistic locking for Versionable entities.
	// We increment the version optimistically, then issue an UPDATE with a
	// WHERE version=old clause. If no rows are affected, it means another
	// transaction modified this entity — we roll back the in-memory version
	// and return a version-mismatch error.
	// We use Model().Where().Select("*").Updates() instead of Where().Save()
	// because Save() may generate an upsert (INSERT … ON CONFLICT) on some
	// dialects (e.g. SQLite), which bypasses the WHERE version clause.
	// Select("*") ensures all columns are written, matching Save() semantics.
	if ver, ok := entity.(Versionable); ok {
		currentVersion := ver.GetVersion()
		ver.SetVersion(currentVersion + 1)
		result := r.db.WithContext(ctx).
			Model(entity).
			Where("version = ?", currentVersion).
			Select("*").
			Updates(entity)
		if result.Error != nil {
			ver.SetVersion(currentVersion) // Roll back version on error
			return r.handleError("update", result.Error)
		}
		if result.RowsAffected == 0 {
			ver.SetVersion(currentVersion) // Roll back version on mismatch
			return dberrors.NewDatabaseError("update", errors.New("version mismatch"))
		}
		return nil
	}

	if err := r.db.WithContext(ctx).Save(entity).Error; err != nil {
		return r.handleError("update", err)
	}
	return nil
}

func (r *GenericRepository) Delete(ctx context.Context, entity interface{}) error {
	result := r.db.WithContext(ctx).Delete(entity)
	if result.Error != nil {
		return r.handleError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

func (r *GenericRepository) List(ctx context.Context, dest interface{}, conditions ...interface{}) error {
	query := r.db.WithContext(ctx)
	for _, cond := range conditions {
		switch c := cond.(type) {
		case Filter:
			if !r.allowedFilterFields[c.Field] {
				return dberrors.NewDatabaseError("list",
					fmt.Errorf("invalid filter field: %q", c.Field))
			}
			// SAFETY: c.Field is interpolated into fmt.Sprintf below, but it is
			// guaranteed to be one of the whitelisted column names checked above,
			// so SQL injection via field names is not possible.
			switch c.Op {
			case "exact":
				query = query.Where(fmt.Sprintf("%s = ?", c.Field), c.Value)
			case ">=":
				query = query.Where(fmt.Sprintf("%s >= ?", c.Field), c.Value)
			case "<=":
				query = query.Where(fmt.Sprintf("%s <= ?", c.Field), c.Value)
			default:
				// Default to LIKE for substring matching.
				// Escape SQL wildcards (% and _) so they are treated as literals.
				escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(fmt.Sprint(c.Value))
				query = query.Where(fmt.Sprintf("%s LIKE ?", c.Field), "%"+escaped+"%")
			}
		case Pagination:
			if c.Limit > 0 {
				query = query.Limit(c.Limit)
			}
			if c.Offset > 0 {
				query = query.Offset(c.Offset)
			}
		}
	}
	if err := query.Find(dest).Error; err != nil {
		return r.handleError("list", err)
	}
	return nil
}

// handleError translates database errors into our custom error types
func (r *GenericRepository) handleError(op string, err error) error {
	if err == nil {
		return nil
	}

	switch {
	case err == gorm.ErrRecordNotFound:
		return dberrors.NewDatabaseError(op, dberrors.ErrNotFound)
	case strings.Contains(err.Error(), "Duplicate entry"):
		return dberrors.NewDatabaseError(op, dberrors.ErrDuplicateKey)
	case strings.Contains(err.Error(), "validation failed"):
		return dberrors.NewDatabaseError(op, dberrors.ErrValidation)
	default:
		return dberrors.NewDatabaseError(op, err)
	}
}

// Filter represents a filter condition for queries
type Filter struct {
	Field string      `json:"field"`
	Op    string      `json:"op,omitempty"`
	Value interface{} `json:"value"`
}

// Pagination represents pagination parameters for queries
type Pagination struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}
