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
var _ models.APIKeyRepository = (*GORMAPIKeyRepository)(nil)

// GORMAPIKeyRepository implements models.APIKeyRepository using GORM.
type GORMAPIKeyRepository struct {
	db *gorm.DB
}

// NewGORMAPIKeyRepository creates a new GORM-backed API key repository.
func NewGORMAPIKeyRepository(db *gorm.DB) *GORMAPIKeyRepository {
	return &GORMAPIKeyRepository{db: db}
}

// Create inserts a new API key record.
func (r *GORMAPIKeyRepository) Create(key *models.APIKey) error {
	if key.UserID == "" || key.Name == "" {
		return dberrors.NewDatabaseError("create", dberrors.ErrValidation)
	}
	if key.ID == "" {
		key.ID = uuid.New().String()
	}
	key.CreatedAt = time.Now().UTC()
	if err := r.db.Create(key).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns an API key by user ID and key ID.
func (r *GORMAPIKeyRepository) FindByID(userID, keyID string) (*models.APIKey, error) {
	var key models.APIKey
	if err := r.db.Where("user_id = ? AND id = ?", userID, keyID).First(&key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &key, nil
}

// FindByPrefix returns all API keys matching the given prefix.
func (r *GORMAPIKeyRepository) FindByPrefix(prefix string) ([]*models.APIKey, error) {
	var keys []*models.APIKey
	if err := r.db.Where("prefix = ?", prefix).Find(&keys).Error; err != nil {
		return nil, dberrors.NewDatabaseError("find_by_prefix", err)
	}
	if len(keys) == 0 {
		return nil, dberrors.NewDatabaseError("find_by_prefix", dberrors.ErrNotFound)
	}
	return keys, nil
}

// ListByUser returns all API keys belonging to a user.
func (r *GORMAPIKeyRepository) ListByUser(userID string) ([]*models.APIKey, error) {
	var keys []*models.APIKey
	if err := r.db.Where("user_id = ?", userID).Order("created_at DESC").Find(&keys).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_user", err)
	}
	return keys, nil
}

// UpdateLastUsed sets the last_used_at timestamp for a specific API key.
func (r *GORMAPIKeyRepository) UpdateLastUsed(userID, keyID string, t time.Time) error {
	result := r.db.Model(&models.APIKey{}).
		Where("user_id = ? AND id = ?", userID, keyID).
		Update("last_used_at", t.UTC())
	if result.Error != nil {
		return dberrors.NewDatabaseError("update_last_used", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("update_last_used", dberrors.ErrNotFound)
	}
	return nil
}

// Delete removes an API key by user ID and key ID.
func (r *GORMAPIKeyRepository) Delete(userID, keyID string) error {
	result := r.db.Where("user_id = ? AND id = ?", userID, keyID).Delete(&models.APIKey{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}
