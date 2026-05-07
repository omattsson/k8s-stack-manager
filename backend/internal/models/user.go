package models

import "time"

// User represents an authenticated user of the system.
type User struct {
	ID           string    `json:"id" gorm:"primaryKey;size:36"`
	Username     string    `json:"username" gorm:"size:100;uniqueIndex"`
	PasswordHash string    `json:"-" gorm:"size:255"`
	DisplayName  string    `json:"display_name" gorm:"size:255"`
	Role         string    `json:"role" gorm:"size:50"`
	AuthProvider string    `json:"auth_provider" gorm:"size:50;default:local"`
	ExternalID   *string   `json:"external_id" gorm:"size:255"`
	Email        string    `json:"email" gorm:"size:255"`
	Disabled     bool      `json:"disabled" gorm:"default:false"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// UserRepository defines data access operations for users.
type UserRepository interface {
	Create(user *User) error
	FindByID(id string) (*User, error)
	FindByIDs(ids []string) (map[string]*User, error)
	FindByUsername(username string) (*User, error)
	FindByExternalID(provider, externalID string) (*User, error)
	Update(user *User) error
	Delete(id string) error
	List() ([]User, error)
	ListByRoles(roles []string) ([]User, error)
	Count() (int64, error)
}
