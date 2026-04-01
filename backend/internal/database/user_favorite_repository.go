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
var _ models.UserFavoriteRepository = (*GORMUserFavoriteRepository)(nil)

// GORMUserFavoriteRepository implements models.UserFavoriteRepository using GORM.
type GORMUserFavoriteRepository struct {
	db *gorm.DB
}

// NewGORMUserFavoriteRepository creates a new GORM-backed user favorite repository.
func NewGORMUserFavoriteRepository(db *gorm.DB) *GORMUserFavoriteRepository {
	return &GORMUserFavoriteRepository{db: db}
}

// List returns all favorites for a user.
func (r *GORMUserFavoriteRepository) List(userID string) ([]*models.UserFavorite, error) {
	var favorites []*models.UserFavorite
	if err := r.db.Where("user_id = ?", userID).Order("created_at DESC").Find(&favorites).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}
	return favorites, nil
}

// Add creates a new favorite. Duplicates are handled gracefully by upserting.
func (r *GORMUserFavoriteRepository) Add(fav *models.UserFavorite) error {
	if err := fav.Validate(); err != nil {
		return dberrors.NewDatabaseError("add", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
	}

	if fav.ID == "" {
		fav.ID = uuid.New().String()
	}
	fav.CreatedAt = time.Now().UTC()

	if err := r.db.Create(fav).Error; err != nil {
		// Handle duplicate gracefully — treat as success.
		if isDuplicateKeyError(err) {
			return nil
		}
		return dberrors.NewDatabaseError("add", err)
	}
	return nil
}

// Remove deletes a favorite by user_id, entity_type, and entity_id.
func (r *GORMUserFavoriteRepository) Remove(userID, entityType, entityID string) error {
	result := r.db.Where("user_id = ? AND entity_type = ? AND entity_id = ?", userID, entityType, entityID).
		Delete(&models.UserFavorite{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("remove", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("remove", dberrors.ErrNotFound)
	}
	return nil
}

// IsFavorite checks whether a specific entity is favorited by a user.
func (r *GORMUserFavoriteRepository) IsFavorite(userID, entityType, entityID string) (bool, error) {
	var count int64
	if err := r.db.Model(&models.UserFavorite{}).
		Where("user_id = ? AND entity_type = ? AND entity_id = ?", userID, entityType, entityID).
		Count(&count).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, dberrors.NewDatabaseError("is_favorite", err)
	}
	return count > 0, nil
}
