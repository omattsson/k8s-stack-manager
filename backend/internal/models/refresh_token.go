package models

import (
	"context"
	"time"
)

// RefreshToken represents a server-side refresh token for JWT token rotation.
// The raw token is never stored — only a SHA-256 hash is persisted.
//
// Tokens of one login session form a family: rotation copies FamilyID and
// SessionStartedAt to the new token. FamilyID is the ID of the first token of
// the session and is also the "sid" claim of the access tokens of the session.
// Rows written before the family columns existed have an empty FamilyID and a
// zero SessionStartedAt; use SessionFamily and SessionStart to read them.
//
//nolint:govet // Struct field alignment optimized for readability over padding
type RefreshToken struct {
	// 8-byte aligned fields first
	ExpiresAt    time.Time `json:"expires_at" gorm:"not null;index"`
	LastActivity time.Time `json:"last_activity" gorm:"not null"`
	CreatedAt    time.Time `json:"created_at"`
	// SessionStartedAt is the login time of the session (absolute lifetime).
	SessionStartedAt time.Time `json:"session_started_at"`
	// RotatedAt is set only when a normal rotation consumes the token. Logout
	// and revoke-all leave it nil, so such a token never gets the reuse grace.
	RotatedAt *time.Time `json:"rotated_at,omitempty"`
	// String fields (8-byte on 64-bit)
	ID        string `json:"id" gorm:"primaryKey;size:36"`
	UserID    string `json:"user_id" gorm:"size:36;not null;index"`
	TokenHash string `json:"-" gorm:"size:64;not null;uniqueIndex"`
	FamilyID  string `json:"family_id" gorm:"size:36;index"`
	UserAgent string `json:"user_agent" gorm:"size:500"`
	IPAddress string `json:"ip_address" gorm:"size:45"`
	// 1-byte fields
	Revoked bool `json:"revoked" gorm:"not null;default:false"`
}

// SessionFamily returns the family ID of the token. A legacy row without a
// family is its own family.
func (t *RefreshToken) SessionFamily() string {
	if t.FamilyID != "" {
		return t.FamilyID
	}
	return t.ID
}

// SessionStart returns the start of the session. A legacy row without a
// session start uses its creation time.
func (t *RefreshToken) SessionStart() time.Time {
	if !t.SessionStartedAt.IsZero() {
		return t.SessionStartedAt
	}
	return t.CreatedAt
}

// RefreshTokenRepository defines data access operations for refresh tokens.
type RefreshTokenRepository interface {
	Create(token *RefreshToken) error
	FindByTokenHash(hash string) (*RefreshToken, error)
	RevokeByID(id string) error
	// MarkRotatedIfActive revokes the token and sets RotatedAt, but only when
	// the token is still active. It returns the number of rows changed (0 when
	// another request already consumed or revoked the token).
	MarkRotatedIfActive(id string, rotatedAt time.Time) (int64, error)
	// RevokeFamily revokes all active tokens of a session family (including a
	// legacy token whose own ID is the family ID).
	RevokeFamily(familyID string) error
	// CountActiveInFamily counts the tokens of a family that are not revoked
	// and not expired.
	CountActiveInFamily(familyID string) (int64, error)
	// TouchFamily sets LastActivity to at for the active tokens of a family.
	// It never moves LastActivity backwards. ctx bounds the database call.
	TouchFamily(ctx context.Context, familyID string, at time.Time) error
	RevokeAllForUser(userID string) error
	RevokeAllForUserExcept(userID string, excludeID string) error
	DeleteExpired() (int64, error)
	CountActiveForUser(userID string) (int64, error)
	// WithTx executes fn within a database transaction. The repository passed
	// to fn shares the same transaction; if fn returns an error the transaction
	// is rolled back.
	WithTx(fn func(txRepo RefreshTokenRepository) error) error
}
