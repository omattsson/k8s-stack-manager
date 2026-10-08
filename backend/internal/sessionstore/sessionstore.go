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
	// LoopbackURL, when set, is a validated loopback http URL supplied by a
	// CLI client running a local HTTP listener (RFC 8252 native-app loopback
	// flow). Accepted forms: any IP for which net.IP.IsLoopback() is true
	// (e.g. http://127.0.0.1:<port>, http://[::1]:<port>) or the literal
	// hostname http://localhost:<port>, with port in 1..65535. See
	// handlers.isLoopbackRedirect for the authoritative validator. When set
	// on a CLI session, the callback handler 302-redirects the browser to
	// this URL with tokens as query params instead of the usual HTML
	// success page.
	LoopbackURL string `json:"loopback_url,omitempty"`
}

type CLIAuthData struct {
	Token    string `json:"token,omitempty"`
	UserID   string `json:"user_id,omitempty"`
	Username string `json:"username,omitempty"`
	Status   string `json:"status"` // "pending", "completed", "consumed"
}

// userBlockApplies is the shared rule for IsUserBlocked. blockedAt is the
// block time in Unix seconds; 0 means unknown (a row written before the block
// time was stored), which blocks every token. A token is blocked when it was
// issued at or before the block second. The comparison is inclusive because a
// JWT iat has one-second precision: a token issued in the same second as the
// block may be older than the block, so it is revoked. A zero issuedAt (token
// without iat) is always blocked.
// Note: blockedAt and iat come from the clocks of different replicas; the rule
// relies on NTP-synchronised clocks (skew of a second or more shifts the cut).
func userBlockApplies(blockedAt int64, issuedAt time.Time) bool {
	if blockedAt <= 0 || issuedAt.IsZero() {
		return true
	}
	return issuedAt.Unix() <= blockedAt
}

type SessionStore interface {
	BlockToken(ctx context.Context, jti string, expiresAt time.Time) error
	IsTokenBlocked(ctx context.Context, jti string) (bool, error)
	// BlockUser revokes every access token of the user issued at or before
	// now. The block entry lives until `until` (the longest token lifetime).
	// Tokens issued after the block (for example after a password reset)
	// stay valid.
	BlockUser(ctx context.Context, userID string, until time.Time) error
	// IsUserBlocked reports whether a token of the user issued at issuedAt is
	// revoked. See userBlockApplies for the rule.
	IsUserBlocked(ctx context.Context, userID string, issuedAt time.Time) (bool, error)
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
