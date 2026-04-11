package models

import "time"

// RefreshToken represents a server-side refresh token for JWT token rotation.
// The raw token is never stored — only a SHA-256 hash is persisted.
//
//nolint:govet // Struct field alignment optimized for readability over padding
type RefreshToken struct {
	// 8-byte aligned fields first
	ExpiresAt    time.Time `json:"expires_at" gorm:"not null;index"`
	LastActivity time.Time `json:"last_activity" gorm:"not null"`
	CreatedAt    time.Time `json:"created_at"`
	// String fields (8-byte on 64-bit)
	ID        string `json:"id" gorm:"primaryKey;size:36"`
	UserID    string `json:"user_id" gorm:"size:36;not null;index"`
	TokenHash string `json:"-" gorm:"size:64;not null;uniqueIndex"`
	UserAgent string `json:"user_agent" gorm:"size:500"`
	IPAddress string `json:"ip_address" gorm:"size:45"`
	// 1-byte fields
	Revoked bool `json:"revoked" gorm:"not null;default:false"`
}

// RefreshTokenRepository defines data access operations for refresh tokens.
type RefreshTokenRepository interface {
	Create(token *RefreshToken) error
	FindByTokenHash(hash string) (*RefreshToken, error)
	RevokeByID(id string) error
	RevokeByIDIfActive(id string) (int64, error)
	RevokeAllForUser(userID string) error
	DeleteExpired() (int64, error)
	CountActiveForUser(userID string) (int64, error)
}
