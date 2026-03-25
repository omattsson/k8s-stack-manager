package models

import "time"

// User represents an authenticated user of the system.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	DisplayName  string    `json:"display_name"`
	Role         string    `json:"role"`
	AuthProvider string    `json:"auth_provider"`
	ExternalID   *string   `json:"external_id"`
	Email        string    `json:"email"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// UserRepository defines data access operations for users.
type UserRepository interface {
	Create(user *User) error
	FindByID(id string) (*User, error)
	FindByUsername(username string) (*User, error)
	FindByExternalID(provider, externalID string) (*User, error)
	Update(user *User) error
	Delete(id string) error
	List() ([]User, error)
}
