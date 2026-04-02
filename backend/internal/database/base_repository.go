package database

import (
	"errors"

	"backend/pkg/dberrors"

	"gorm.io/gorm"
)

// BaseRepository provides common CRUD operations for GORM models with string
// ID primary keys. Embed it in concrete repository structs to eliminate
// duplicated FindByID / Delete / Create / Save boilerplate.
//
// Methods that vary between repositories (List with filters, domain-specific
// queries, validation, encryption, etc.) should remain on the concrete type.
type BaseRepository[T any] struct {
	DB *gorm.DB
}

// FindByID returns a single record by primary key.
// Returns a DatabaseError wrapping ErrNotFound when no row matches.
func (r *BaseRepository[T]) FindByID(id string) (*T, error) {
	var entity T
	if err := r.DB.Where("id = ?", id).First(&entity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &entity, nil
}

// Delete deletes a record by primary key. GORM will soft-delete if the model
// embeds gorm.DeletedAt; otherwise it performs a hard delete.
// Returns a DatabaseError wrapping ErrNotFound when no row was affected.
func (r *BaseRepository[T]) Delete(id string) error {
	var zero T
	result := r.DB.Where("id = ?", id).Delete(&zero)
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// Create inserts a new record. Duplicate key constraint violations are
// mapped to ErrDuplicateKey.
func (r *BaseRepository[T]) Create(entity *T) error {
	if err := r.DB.Create(entity).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// Save persists all fields of the record (upsert-style via GORM Save).
// Duplicate key constraint violations are mapped to ErrDuplicateKey.
func (r *BaseRepository[T]) Save(entity *T) error {
	if err := r.DB.Save(entity).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("update", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("update", err)
	}
	return nil
}
