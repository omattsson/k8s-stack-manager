package sessionstore

import (
	"context"
	"errors"
	"strconv"
	"strings"
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

// userBlockApplies is the shared rule for IsUserBlocked. blockedAtMs is the
// block time in Unix milliseconds; 0 means unknown (a row written before the
// block time was stored), which blocks every token. A token is blocked when
// it was issued at or before the block (inclusive). A zero issuedAt (token
// without iat) is always blocked.
//
// issuedAt has millisecond precision for tokens with the iat_ms claim. A
// token without that claim has a whole-second issuedAt (the JWT iat). For
// such a token the millisecond rule gives the old second rule: it is blocked
// when it was issued in the block second or before it, because the token may
// be older than the block.
// Note: blockedAtMs and iat come from the clocks of different replicas; the
// rule relies on NTP-synchronised clocks (clock skew shifts the cut).
func userBlockApplies(blockedAtMs int64, issuedAt time.Time) bool {
	if blockedAtMs <= 0 || issuedAt.IsZero() {
		return true
	}
	return issuedAt.UnixMilli() <= blockedAtMs
}

// msTimestampMin is the smallest value that parseBlockedAt reads as Unix
// milliseconds (2001-09-09). Smaller values are Unix seconds (the user_block
// entry).
const msTimestampMin = 1_000_000_000_000

// parseBlockedAt reads the block time of a user block entry and returns Unix
// milliseconds. A value in seconds gives the last millisecond of that second,
// so the rule "issued at or before the block second" stays the same. An empty
// or malformed value gives 0 (block every token).
func parseBlockedAt(data string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(data), 10, 64)
	if err != nil || v <= 0 {
		return 0
	}
	if v < msTimestampMin {
		return v*1000 + 999
	}
	return v
}

// userBlockTime returns the block time in Unix milliseconds from the two
// block entries: the seconds entry (secData; older versions write and read
// only this one) and the milliseconds entry (msData; written by this
// version). has* reports whether the entry exists.
//
//   - Only the seconds entry (block of an older version, or a store without
//     the milliseconds entry): the second rule (parseBlockedAt).
//   - Both entries: the milliseconds entry when it is from the same second
//     as the seconds entry or later. When the seconds entry is newer (an
//     older version blocked the user again after this version), the seconds
//     entry wins with the second rule.
//   - Only the milliseconds entry: the milliseconds entry.
//
// Limit during a mixed-version rollout: when replicas of different versions
// block the same user twice in the same second, the seconds entry cannot
// tell the blocks apart, and the (earlier) milliseconds block applies.
func userBlockTime(secData string, hasSec bool, msData string, hasMs bool) int64 {
	var secMs, ms int64
	if hasSec {
		secMs = parseBlockedAt(secData)
	}
	if hasMs {
		ms = parseBlockedAt(msData)
	}
	switch {
	case !hasMs || ms <= 0:
		return secMs
	case !hasSec:
		return ms
	case secMs <= 0:
		return 0 // seconds entry without a block time: block every token
	case ms/1000 >= secMs/1000:
		return ms
	default:
		return secMs
	}
}

type SessionStore interface {
	BlockToken(ctx context.Context, jti string, expiresAt time.Time) error
	IsTokenBlocked(ctx context.Context, jti string) (bool, error)
	// BlockUser revokes every access token of the user issued at or before
	// now (millisecond precision). The block entry lives until `until` (the longest token lifetime).
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
