package database

import (
	"errors"
	"strings"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// Update saves the full user row. It is not part of models.UserRepository and
// only test setup uses it: a full-row save overwrites concurrent changes to
// other columns (role, disabled). Handlers use the targeted updates
// (UpdatePassword, UpdateProfile, UpdateRole, SetDisabled).
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
	return r.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Where("id = ?", id).Delete(&models.User{})
		if result.Error != nil {
			return dberrors.NewDatabaseError("delete", result.Error)
		}
		if result.RowsAffected == 0 {
			return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
		}
		// The user follows no instance any more.
		if err := tx.Where("user_id = ?", id).Delete(&models.InstanceFollower{}).Error; err != nil {
			return dberrors.NewDatabaseError("delete_followers", err)
		}
		return nil
	})
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

// ListByRoles returns users whose role matches any of the given roles.
func (r *GORMUserRepository) ListByRoles(roles []string) ([]models.User, error) {
	if len(roles) == 0 {
		return nil, nil
	}
	var users []models.User
	if err := r.db.Where("role IN ?", roles).Find(&users).Error; err != nil {
		return nil, dberrors.NewDatabaseError("list_by_roles", err)
	}
	return users, nil
}

// guardTarget is the locked target row of a guarded admin change.
type guardTarget struct {
	user models.User
	// otherEnabledAdmins counts the enabled admins other than the target.
	otherEnabledAdmins int
}

// wouldRemoveLastAdmin reports whether removing the target from the enabled
// admins (demote, disable, delete) leaves none.
func (g guardTarget) wouldRemoveLastAdmin() bool {
	return g.user.Role == models.RoleAdmin && !g.user.Disabled && g.otherEnabledAdmins == 0
}

// guardedAdminChange runs fn in a transaction for a change that an admin
// makes to a user.
//
// Locking: the transaction first locks all admin rows (ordered by ID), then
// the target row. Every guarded change takes the admin locks in the same
// order, so two admins who demote, disable or delete each other at the same
// time are serialized: the second one sees the result of the first one.
// Inside the lock, the caller must still be an enabled admin
// (ErrCallerNotAdmin). IDs compare without case, as the MySQL collation does.
func (r *GORMUserRepository) guardedAdminChange(op, callerID, id string, fn func(tx *gorm.DB, t guardTarget) error) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var admins []models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "disabled").
			Where("role = ?", models.RoleAdmin).
			Order("id").
			Find(&admins).Error; err != nil {
			return dberrors.NewDatabaseError(op, err)
		}

		callerOK := false
		for _, a := range admins {
			if strings.EqualFold(a.ID, callerID) && !a.Disabled {
				callerOK = true
				break
			}
		}
		if !callerOK {
			return models.ErrCallerNotAdmin
		}

		var target models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", id).
			Take(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return dberrors.NewDatabaseError(op, dberrors.ErrNotFound)
			}
			return dberrors.NewDatabaseError(op, err)
		}

		g := guardTarget{user: target}
		for _, a := range admins {
			if !strings.EqualFold(a.ID, target.ID) && !a.Disabled {
				g.otherEnabledAdmins++
			}
		}
		return fn(tx, g)
	})
}

// UpdateRole sets the role of a user and returns the previous role. See
// models.UserRepository.
func (r *GORMUserRepository) UpdateRole(callerID, id, role string) (string, error) {
	var oldRole string
	err := r.guardedAdminChange("update_role", callerID, id, func(tx *gorm.DB, g guardTarget) error {
		oldRole = g.user.Role
		if oldRole == role {
			return nil
		}
		if role != models.RoleAdmin && g.wouldRemoveLastAdmin() {
			return models.ErrLastAdmin
		}
		return updateUserColumns(tx, "update_role", g.user.ID, map[string]interface{}{"role": role})
	})
	if err != nil {
		return "", err
	}
	return oldRole, nil
}

// SetDisabled disables or enables a user. See models.UserRepository.
func (r *GORMUserRepository) SetDisabled(callerID, id string, disabled bool) error {
	return r.guardedAdminChange("set_disabled", callerID, id, func(tx *gorm.DB, g guardTarget) error {
		if disabled && g.wouldRemoveLastAdmin() {
			return models.ErrLastAdmin
		}
		return updateUserColumns(tx, "set_disabled", g.user.ID, map[string]interface{}{"disabled": disabled})
	})
}

// DeleteGuarded deletes a user. See models.UserRepository.
func (r *GORMUserRepository) DeleteGuarded(callerID, id string) error {
	return r.guardedAdminChange("delete", callerID, id, func(tx *gorm.DB, g guardTarget) error {
		if g.wouldRemoveLastAdmin() {
			return models.ErrLastAdmin
		}
		result := tx.Where("id = ?", g.user.ID).Delete(&models.User{})
		if result.Error != nil {
			return dberrors.NewDatabaseError("delete", result.Error)
		}
		if result.RowsAffected == 0 {
			return dberrors.NewDatabaseError("delete", dberrors.ErrNotFound)
		}
		// The user follows no instance any more.
		if err := tx.Where("user_id = ?", g.user.ID).Delete(&models.InstanceFollower{}).Error; err != nil {
			return dberrors.NewDatabaseError("delete_followers", err)
		}
		return nil
	})
}

func updateUserColumns(tx *gorm.DB, op, id string, cols map[string]interface{}) error {
	cols["updated_at"] = time.Now().UTC()
	if err := tx.Model(&models.User{}).Where("id = ?", id).Updates(cols).Error; err != nil {
		return dberrors.NewDatabaseError(op, err)
	}
	return nil
}

// UpdatePassword sets only the password hash. See models.UserRepository.
func (r *GORMUserRepository) UpdatePassword(id, passwordHash string) error {
	result := r.db.Model(&models.User{}).Where("id = ?", id).Updates(map[string]interface{}{
		"password_hash": passwordHash,
		"updated_at":    time.Now().UTC(),
	})
	if result.Error != nil {
		return dberrors.NewDatabaseError("update_password", result.Error)
	}
	if result.RowsAffected == 0 {
		return dberrors.NewDatabaseError("update_password", dberrors.ErrNotFound)
	}
	return nil
}

// UpdateProfile sets only the columns listed in upd. See
// models.UserRepository.
func (r *GORMUserRepository) UpdateProfile(id string, upd models.UserProfileUpdate) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		cols := map[string]interface{}{"updated_at": time.Now().UTC()}
		if upd.Email != nil {
			cols["email"] = *upd.Email
		}
		if upd.DisplayName != nil {
			cols["display_name"] = *upd.DisplayName
		}
		if upd.LinkProvider != nil {
			cols["auth_provider"] = *upd.LinkProvider
			cols["external_id"] = upd.ExternalID
			cols["password_hash"] = ""
		}
		result := tx.Model(&models.User{}).Where("id = ?", id).Updates(cols)
		if result.Error != nil {
			if isDuplicateKeyError(result.Error) {
				return dberrors.NewDatabaseError("update_profile", dberrors.ErrDuplicateKey)
			}
			return dberrors.NewDatabaseError("update_profile", result.Error)
		}
		if result.RowsAffected == 0 {
			return dberrors.NewDatabaseError("update_profile", dberrors.ErrNotFound)
		}
		if upd.Role != nil {
			// Only an SSO user takes the role from the identity provider.
			if err := tx.Model(&models.User{}).
				Where("id = ? AND auth_provider <> ? AND auth_provider <> ?", id, "", "local").
				Updates(map[string]interface{}{"role": *upd.Role}).Error; err != nil {
				return dberrors.NewDatabaseError("update_profile", err)
			}
		}
		return nil
	})
}
