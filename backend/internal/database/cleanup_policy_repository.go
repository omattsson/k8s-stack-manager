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
var _ models.CleanupPolicyRepository = (*GORMCleanupPolicyRepository)(nil)

// GORMCleanupPolicyRepository implements models.CleanupPolicyRepository using GORM.
type GORMCleanupPolicyRepository struct {
	db *gorm.DB
}

// NewGORMCleanupPolicyRepository creates a new GORM-backed cleanup policy repository.
func NewGORMCleanupPolicyRepository(db *gorm.DB) *GORMCleanupPolicyRepository {
	return &GORMCleanupPolicyRepository{db: db}
}

// Create inserts a new cleanup policy record.
func (r *GORMCleanupPolicyRepository) Create(policy *models.CleanupPolicy) error {
	if err := policy.Validate(); err != nil {
		return dberrors.NewDatabaseError("create", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
	}

	if policy.ID == "" {
		policy.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	policy.CreatedAt = now
	policy.UpdatedAt = now

	if err := r.db.Create(policy).Error; err != nil {
		return dberrors.NewDatabaseError("create", err)
	}
	return nil
}

// FindByID returns a cleanup policy by its ID.
func (r *GORMCleanupPolicyRepository) FindByID(id string) (*models.CleanupPolicy, error) {
	var policy models.CleanupPolicy
	if err := r.db.Where("id = ?", id).First(&policy).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
		}
		return nil, dberrors.NewDatabaseError("find_by_id", err)
	}
	return &policy, nil
}

// Update persists changes to an existing cleanup policy record.
func (r *GORMCleanupPolicyRepository) Update(policy *models.CleanupPolicy) error {
	if err := policy.Validate(); err != nil {
		return dberrors.NewDatabaseError("update", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
	}

	policy.UpdatedAt = time.Now().UTC()
	if err := r.db.Save(policy).Error; err != nil {
		return dberrors.NewDatabaseError("update", err)
	}
	return nil
}

// Delete removes a cleanup policy by ID.
func (r *GORMCleanupPolicyRepository) Delete(id string) error {
	result := r.db.Where("id = ?", id).Delete(&models.CleanupPolicy{})
	if result.Error != nil {
		return dberrors.NewDatabaseError("delete", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
	}
	return nil
}

// List returns all cleanup policies.
func (r *GORMCleanupPolicyRepository) List() ([]models.CleanupPolicy, error) {
	var policies []models.CleanupPolicy
	if err := r.db.Find(&policies).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}
	return policies, nil
}

// ListEnabled returns all cleanup policies where enabled is true.
func (r *GORMCleanupPolicyRepository) ListEnabled() ([]models.CleanupPolicy, error) {
	var policies []models.CleanupPolicy
	if err := r.db.Where("enabled = ?", true).Find(&policies).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_enabled", err)
	}
	return policies, nil
}
