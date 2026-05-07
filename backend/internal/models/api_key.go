package models

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"golang.org/x/crypto/argon2"
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
//   - hash    — Argon2id hash of rawKey, stored in DB (with salt and params)
func GenerateAPIKey() (rawKey, prefix, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("generate api key: %w", err)
	}
	rawKey = hex.EncodeToString(b)
	prefix = rawKey[:16]
	hash, err = HashAPIKeyWithSalt(rawKey)
	if err != nil {
		return "", "", "", err
	}
	return rawKey, prefix, hash, nil
}

// HashAPIKeyWithSalt hashes the API key using Argon2id with a random salt and returns the encoded hash string.
func HashAPIKeyWithSalt(rawKey string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}
	hash := argon2.IDKey([]byte(rawKey), salt, 1, 64*1024, 4, 32)
	// Format: $argon2id$v=19$m=65536,t=1,p=4$<salt_b64>$<hash_b64>
	encoded := "$argon2id$v=19$m=65536,t=1,p=4$" +
		base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(hash)
	return encoded, nil
}

// HashAPIKey verifies a raw API key against a stored Argon2id hash.
// Returns true if the hash matches, false otherwise.
func VerifyAPIKeyHash(rawKey, encodedHash string) bool {
	// Parse encoded hash
	var saltB64, hashB64 string
	_, err := fmt.Sscanf(encodedHash, "$argon2id$v=19$m=65536,t=1,p=4$%s$%s", &saltB64, &hashB64)
	if err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(saltB64)
	if err != nil {
		return false
	}
	expectedHash, err := base64.RawStdEncoding.DecodeString(hashB64)
	if err != nil {
		return false
	}
	hash := argon2.IDKey([]byte(rawKey), salt, 1, 64*1024, 4, 32)
	return subtleConstantTimeCompare(hash, expectedHash)
}

// subtleConstantTimeCompare compares two byte slices in constant time.
func subtleConstantTimeCompare(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var result byte
	for i := 0; i < len(a); i++ {
		result |= a[i] ^ b[i]
	}
	return result == 0
}
