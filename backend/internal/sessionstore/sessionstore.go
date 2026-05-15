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
	// LoopbackURL, when set, is a validated `http://127.0.0.1:<port>` /
	// `http://localhost:<port>` URL supplied by a CLI client running a local
	// HTTP listener (RFC 8252 native-app loopback flow). When set on a CLI
	// session, the callback handler 302-redirects the browser to this URL
	// with tokens as query params instead of the usual HTML success page.
	LoopbackURL string `json:"loopback_url,omitempty"`
}

type CLIAuthData struct {
	Token    string `json:"token,omitempty"`
	UserID   string `json:"user_id,omitempty"`
	Username string `json:"username,omitempty"`
	Status   string `json:"status"` // "pending", "completed", "consumed"
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
	ConsumeCLIAuth(ctx context.Context, sessionID string) (*CLIAuthData, error)
	Cleanup(ctx context.Context) error
	Stop()
}
