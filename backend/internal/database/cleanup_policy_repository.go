package database

import (
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
	BaseRepository[models.CleanupPolicy]
}

// NewGORMCleanupPolicyRepository creates a new GORM-backed cleanup policy repository.
func NewGORMCleanupPolicyRepository(db *gorm.DB) *GORMCleanupPolicyRepository {
	return &GORMCleanupPolicyRepository{
		BaseRepository: BaseRepository[models.CleanupPolicy]{DB: db},
	}
}

// Create inserts a new cleanup policy record with validation.
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

	return r.BaseRepository.Create(policy)
}

// Update persists changes to an existing cleanup policy record with validation.
func (r *GORMCleanupPolicyRepository) Update(policy *models.CleanupPolicy) error {
	if err := policy.Validate(); err != nil {
		return dberrors.NewDatabaseError("update", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
	}

	policy.UpdatedAt = time.Now().UTC()
	return r.Save(policy)
}

// List returns all cleanup policies.
func (r *GORMCleanupPolicyRepository) List() ([]models.CleanupPolicy, error) {
	var policies []models.CleanupPolicy
	if err := r.DB.Find(&policies).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list", err)
	}
	return policies, nil
}

// ListEnabled returns all cleanup policies where enabled is true.
func (r *GORMCleanupPolicyRepository) ListEnabled() ([]models.CleanupPolicy, error) {
	var policies []models.CleanupPolicy
	if err := r.DB.Where("enabled = ?", true).Find(&policies).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_enabled", err)
	}
	return policies, nil
}
