package sessionstore

import (
	"context"
	"errors"
	"time"
)

var ErrSessionNotFound = errors.New("session not found or expired")

type OIDCStateData struct {
	CodeVerifier string `json:"code_verifier"`
	RedirectURL  string `json:"redirect_url"`
}

type CLIAuthData struct {
	Token    string `json:"token,omitempty"`
	UserID   string `json:"user_id,omitempty"`
	Username string `json:"username,omitempty"`
	Status   string `json:"status"` // "pending", "completed"
}

type SessionStore interface {
	BlockToken(ctx context.Context, jti string, expiresAt time.Time) error
	IsTokenBlocked(ctx context.Context, jti string) (bool, error)
	BlockUser(ctx context.Context, userID string, until time.Time) error
	IsUserBlocked(ctx context.Context, userID string) (bool, error)
	UnblockUser(ctx context.Context, userID string) error
	SaveOIDCState(ctx context.Context, state string, data OIDCStateData, ttl time.Duration) error
	ConsumeOIDCState(ctx context.Context, state string) (*OIDCStateData, error)
	SaveCLIAuth(ctx context.Context, sessionID string, data CLIAuthData, ttl time.Duration) error
	GetCLIAuth(ctx context.Context, sessionID string) (*CLIAuthData, error)
	UpdateCLIAuth(ctx context.Context, sessionID string, data CLIAuthData) error
	Cleanup(ctx context.Context) error
	Stop()
}
