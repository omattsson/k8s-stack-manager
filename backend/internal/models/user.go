package models

import (
	"errors"
	"time"
)

// User roles, lowest to highest permission.
const (
	RoleUser   = "user"
	RoleDevOps = "devops"
	RoleAdmin  = "admin"
)

// IsValidRole reports whether role is one of RoleUser, RoleDevOps or RoleAdmin.
func IsValidRole(role string) bool {
	switch role {
	case RoleUser, RoleDevOps, RoleAdmin:
		return true
	}
	return false
}

// Errors of the guarded user changes (UpdateRole, SetDisabled, DeleteGuarded).
var (
	// ErrLastAdmin: the change would leave no enabled admin.
	ErrLastAdmin = errors.New("last enabled admin")
	// ErrCallerNotAdmin: the caller is not (or no longer) an enabled admin.
	ErrCallerNotAdmin = errors.New("caller is not an enabled admin")
)

// User represents an authenticated user of the system.
type User struct {
	ID             string    `json:"id" gorm:"primaryKey;size:36"`
	Username       string    `json:"username" gorm:"size:100;uniqueIndex"`
	PasswordHash   string    `json:"-" gorm:"size:255"`
	DisplayName    string    `json:"display_name" gorm:"size:255"`
	Role           string    `json:"role" gorm:"size:50;index:idx_users_role"`
	AuthProvider   string    `json:"auth_provider" gorm:"size:50;default:local"`
	ExternalID     *string   `json:"external_id" gorm:"size:255"`
	Email          string    `json:"email" gorm:"size:255"`
	Disabled       bool      `json:"disabled" gorm:"default:false"`
	ServiceAccount bool      `json:"service_account" gorm:"default:false"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// UserProfileUpdate lists the columns that UserRepository.UpdateProfile
// sets. A nil field keeps the stored value.
type UserProfileUpdate struct {
	Email       *string
	DisplayName *string
	// Role is the role from the identity provider. It is written only when
	// the user is (or, with LinkProvider, becomes) an SSO user; the role of a
	// local user changes only through UpdateRole.
	Role *string
	// LinkProvider links a local user to an identity provider: it sets
	// auth_provider to this value and external_id to ExternalID, and clears
	// the password hash.
	LinkProvider *string
	ExternalID   *string
}

// UserRepository defines data access operations for users. There is no
// full-row update: each change writes only its own columns, so concurrent
// changes (role, disabled, password, profile) cannot overwrite each other.
type UserRepository interface {
	Create(user *User) error
	FindByID(id string) (*User, error)
	FindByIDs(ids []string) (map[string]*User, error)
	FindByUsername(username string) (*User, error)
	FindByExternalID(provider, externalID string) (*User, error)
	// UpdatePassword sets only the password hash of a user.
	UpdatePassword(id, passwordHash string) error
	// UpdateProfile sets only the columns listed in upd. It never writes
	// disabled, and it writes the role only for an SSO user (see
	// UserProfileUpdate.Role). Use it instead of a full-row save, which would
	// overwrite a concurrent role or disabled change.
	UpdateProfile(id string, upd UserProfileUpdate) error
	Delete(id string) error
	List() ([]User, error)
	ListByRoles(roles []string) ([]User, error)
	Count() (int64, error)
	// Guarded admin changes. Each runs in one transaction that locks the
	// admin rows (ordered by ID) and then the target row, so concurrent
	// changes serialize. Each returns ErrCallerNotAdmin when callerID is not
	// an enabled admin at that moment, and ErrLastAdmin (changing nothing)
	// when the change would leave no enabled admin.

	// UpdateRole sets the role of a user and returns the previous role. An
	// unchanged role is a no-op.
	UpdateRole(callerID, id, role string) (oldRole string, err error)
	// SetDisabled disables or enables a user.
	SetDisabled(callerID, id string, disabled bool) error
	// DeleteGuarded deletes a user.
	DeleteGuarded(callerID, id string) error
}
