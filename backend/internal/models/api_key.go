package models

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
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
//   - hash    — SHA-256 hex hash of rawKey, stored in DB
func GenerateAPIKey() (rawKey, prefix, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("generate api key: %w", err)
	}
	rawKey = hex.EncodeToString(b)
	prefix = rawKey[:16]
	hash = hashAPIKeySHA256(rawKey)
	return rawKey, prefix, hash, nil
}

// hashAPIKeySHA256 hashes a raw API key with SHA-256.
// Suitable for high-entropy random keys (32 bytes) where KDF overhead is unnecessary.
func hashAPIKeySHA256(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(sum[:])
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

// VerifyAPIKeyHash verifies a raw API key against a stored hash.
// Supports both SHA-256 hex (legacy/new format) and Argon2id encoded format.
func VerifyAPIKeyHash(rawKey, encodedHash string) bool {
	// SHA-256 hex format: 64 lowercase hex chars with no "$"
	if len(encodedHash) == 64 && !strings.Contains(encodedHash, "$") {
		expected := hashAPIKeySHA256(rawKey)
		return subtleConstantTimeCompare([]byte(expected), []byte(encodedHash))
	}
	// Argon2id format: $argon2id$v=19$m=65536,t=1,p=4$<salt_b64>$<hash_b64>
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
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
