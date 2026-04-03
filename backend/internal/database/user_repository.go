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
var _ models.UserRepository = (*GORMUserRepository)(nil)

// GORMUserRepository implements models.UserRepository using GORM.
type GORMUserRepository struct {
	db *gorm.DB
}

// NewGORMUserRepository creates a new GORM-backed user repository.
func NewGORMUserRepository(db *gorm.DB) *GORMUserRepository {
	return &GORMUserRepository{db: db}
}

// Create inserts a new user record.
func (r *GORMUserRepository) Create(user *models.User) error {
	if user.Username == "" {
		return dberrors.NewDatabaseError("create", dberrors.ErrValidation)
	}
	if user.ID == "" {
		user.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	user.CreatedAt = now
	user.UpdatedAt = now
	if err := r.db.Create(user).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("create", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns a user by their ID.
func (r *GORMUserRepository) FindByID(id string) (*models.User, error) {
	var user models.User
	if err := r.db.Where("id = ?", id).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &user, nil
}

// FindByIDs returns a map of user ID to User for the given IDs in a single query.
func (r *GORMUserRepository) FindByIDs(ids []string) (map[string]*models.User, error) {
	if len(ids) == 0 {
		return make(map[string]*models.User), nil
	}
	var users []models.User
	if err := r.db.Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, dberrors.NewDatabaseError("find_by_ids", err)
	}
	result := make(map[string]*models.User, len(users))
	for i := range users {
		result[users[i].ID] = &users[i]
	}
	return result, nil
}

// FindByUsername returns a user by their username.
func (r *GORMUserRepository) FindByUsername(username string) (*models.User, error) {
	var user models.User
	if err := r.db.Where("username = ?", username).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_username", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_username", err)
	}
	return &user, nil
}

// FindByExternalID returns a user by auth provider and external ID.
func (r *GORMUserRepository) FindByExternalID(provider, externalID string) (*models.User, error) {
	var user models.User
	if err := r.db.Where("auth_provider = ? AND external_id = ?", provider, externalID).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_external_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_external_id", err)
	}
	return &user, nil
}

// Update persists changes to an existing user record.
func (r *GORMUserRepository) Update(user *models.User) error {
	user.UpdatedAt = time.Now().UTC()
	if err := r.db.Save(user).Error; err != nil {
		if isDuplicateKeyError(err) {
			return dberrors.NewDatabaseError("update", dberrors.ErrDuplicateKey)
		}
		return dberrors.NewDatabaseError("update", err)
	}
	return nil
}

// Delete removes a user by ID.
func (r *GORMUserRepository) Delete(id string) error {
	result := r.db.Where("id = ?", id).Delete(&models.User{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// Count returns the total number of users.
func (r *GORMUserRepository) Count() (int64, error) {
	var count int64
	if err := r.db.Model(&models.User{}).Count(&count).Error; err != nil {
		return 0, dberrors.NewDatabaseError("count", err)
	}
	return count, nil
}

// List returns all users.
func (r *GORMUserRepository) List() ([]models.User, error) {
	var users []models.User
	if err := r.db.Find(&users).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}
	return users, nil
}
