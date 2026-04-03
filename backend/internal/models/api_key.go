package models

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// APIKey represents a user-generated API key for programmatic access.
// KeyHash is never serialised to JSON (json:"-").
type APIKey struct {
	ID         string     `json:"id" gorm:"primaryKey;size:36"`
	UserID     string     `json:"user_id" gorm:"size:36"`
	Name       string     `json:"name" gorm:"size:255"`
	KeyHash    string     `json:"-" gorm:"size:255"`     // stored; never returned
	Prefix     string     `json:"prefix" gorm:"size:20"` // first 16 chars of raw key for display
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// APIKeyRepository defines data access operations for API keys.
// Partition key = UserID, Row key = ID.
type APIKeyRepository interface {
	Create(key *APIKey) error
	FindByID(userID, keyID string) (*APIKey, error)
	// FindByPrefix performs a table scan filtered client-side; acceptable at low volume.
	FindByPrefix(prefix string) ([]*APIKey, error)
	ListByUser(userID string) ([]*APIKey, error)
	UpdateLastUsed(userID, keyID string, t time.Time) error
	Delete(userID, keyID string) error
}

// GenerateAPIKey creates a cryptographically random 32-byte key and returns:
//   - rawKey  — 64-char hex string, returned to the user once
//   - prefix  — first 16 chars for display/lookup
//   - hash    — SHA-256 hex of rawKey, stored in DB
func GenerateAPIKey() (rawKey, prefix, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("generate api key: %w", err)
	}
	rawKey = hex.EncodeToString(b)
	prefix = rawKey[:16]
	sum := sha256.Sum256([]byte(rawKey))
	hash = hex.EncodeToString(sum[:])
	return rawKey, prefix, hash, nil
}

// HashAPIKey returns the SHA-256 hex hash of a raw API key string.
func HashAPIKey(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(sum[:])
}
