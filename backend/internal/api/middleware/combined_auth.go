package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"backend/internal/cache"
	"backend/internal/models"
	"backend/internal/sessionstore"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
)

// apiKeyLookupOutcome classifies a repository lookup error for metrics: a
// not-found error is a bad client credential ("invalid"), while any other
// error is an operational/dependency failure ("failure") that must be
// alertable separately.
func apiKeyLookupOutcome(err error) string {
	if errors.Is(err, dberrors.ErrNotFound) {
		return "invalid"
	}
	return "failure"
}

// lastUsedCache prevents UpdateLastUsed from firing more than once per
// API key per minute, reducing connection pool pressure under high load.
// Entries auto-expire after 2 minutes so the cache stays bounded.
var lastUsedCache = cache.New[time.Time](2*time.Minute, 1*time.Minute)

// Limits of the concurrent Argon2id verifies of API keys (HTTP and /ws).
const (
	// maxConcurrentArgon2Verifies caps the verifies at once in the process.
	// Each one uses 64 MiB of memory.
	maxConcurrentArgon2Verifies = 4
	// argon2VerifyWait is how long a request waits for a free verify slot
	// before it gets 503.
	argon2VerifyWait = 100 * time.Millisecond
)

// verifyLimiter caps the concurrent Argon2id API key verifies. A request
// that finds no free slot within the wait time gets 503 at once, so a burst
// of requests cannot queue without limit or use unbounded memory.
type verifyLimiter struct {
	slots chan struct{}
	wait  time.Duration
}

func newVerifyLimiter(n int, wait time.Duration) *verifyLimiter {
	return &verifyLimiter{slots: make(chan struct{}, n), wait: wait}
}

// acquire takes a slot. It returns false when no slot is free within the
// wait time.
func (l *verifyLimiter) acquire() bool {
	select {
	case l.slots <- struct{}{}:
		return true
	default:
	}
	timer := time.NewTimer(l.wait)
	defer timer.Stop()
	select {
	case l.slots <- struct{}{}:
		return true
	case <-timer.C:
		return false
	}
}

func (l *verifyLimiter) release() { <-l.slots }

// apiKeyVerifyLimiter is the process-wide limiter of AuthenticateAPIKey.
var apiKeyVerifyLimiter = newVerifyLimiter(maxConcurrentArgon2Verifies, argon2VerifyWait)

// APIKeyAuthDeps holds the dependencies for combined JWT + API-key auth.
type APIKeyAuthDeps struct {
	JWTSecret    string
	APIKeyRepo   models.APIKeyRepository
	UserRepo     models.UserRepository
	SessionStore sessionstore.SessionStore
	// OnSessionActivity records activity of JWT sessions (idle timeout). It is
	// optional; API-key requests never call it.
	OnSessionActivity SessionActivityFunc
}

// CombinedAuth returns middleware that accepts either:
//  1. Authorization: Bearer <jwt>  — existing JWT path
//  2. X-API-Key: sk_<rawKey>       — API key path
//
// When APIKeyRepo or UserRepo is nil the middleware falls back to JWT-only auth.
func CombinedAuth(deps APIKeyAuthDeps) gin.HandlerFunc {
	jwtMW := AuthRequiredWithOptions(JWTAuthOptions{
		JWTSecret:         deps.JWTSecret,
		SessionStore:      deps.SessionStore,
		OnSessionActivity: deps.OnSessionActivity,
	})
	if deps.APIKeyRepo == nil || deps.UserRepo == nil {
		return jwtMW
	}

	return func(c *gin.Context) {
		// Prefer JWT Bearer when present.
		authHeader := c.GetHeader("Authorization")
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			jwtMW(c)
			return
		}

		// Fall back to X-API-Key header.
		apiKeyHeader := c.GetHeader("X-API-Key")
		if apiKeyHeader == "" {
			RecordAPIKeyAuth("missing")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization required"})
			return
		}

		_, user, failure := AuthenticateAPIKey(deps.APIKeyRepo, deps.UserRepo, apiKeyHeader)
		if failure != nil {
			c.AbortWithStatusJSON(failure.Status, gin.H{"error": failure.Message})
			return
		}

		// Inject the same context keys that JWT middleware sets.
		c.Set(contextKeyUserID, user.ID)
		c.Set(contextKeyUsername, user.Username)
		c.Set(contextKeyRole, user.Role)
	}
}

// APIKeyAuthFailure describes a rejected API key. Message is safe to return
// to the client; it never contains an internal error.
type APIKeyAuthFailure struct {
	Message string
	Outcome string // metrics outcome: invalid, failure, expired, disabled, busy
	Status  int    // HTTP status of the HTTP API-key auth (401, 403 for a disabled user, 503 when busy)
}

// AuthenticateAPIKey validates the value of an X-API-Key header
// ("sk_<raw key>" or "<raw key>"). It is the API-key check of CombinedAuth;
// the WebSocket upgrade uses it too. It checks that:
//   - a stored key matches the raw key (prefix lookup + hash compare),
//   - the key has not expired,
//   - the user of the key exists and is not disabled.
//
// On success it returns the key and its user (role and username come from
// the database), records the "success" outcome and updates the last-used
// time of the key (throttled to once per key per minute). On failure it
// records the outcome and returns a failure. A lookup error counts as an
// invalid key (401) and is logged; the message never contains the error.
// An Argon2id hash verify needs a slot of the process-wide verify limiter;
// without a free slot the result is 503 ("busy").
func AuthenticateAPIKey(apiKeyRepo models.APIKeyRepository, userRepo models.UserRepository, header string) (*models.APIKey, *models.User, *APIKeyAuthFailure) {
	return authenticateAPIKey(apiKeyRepo, userRepo, header, apiKeyVerifyLimiter)
}

// authenticateAPIKey is AuthenticateAPIKey with the verify limiter (tests).
func authenticateAPIKey(apiKeyRepo models.APIKeyRepository, userRepo models.UserRepository, header string, limiter *verifyLimiter) (*models.APIKey, *models.User, *APIKeyAuthFailure) {
	invalid := func(outcome string) *APIKeyAuthFailure {
		RecordAPIKeyAuth(outcome)
		return &APIKeyAuthFailure{Status: http.StatusUnauthorized, Message: "Invalid API key", Outcome: outcome}
	}

	// Strip the sk_ prefix.
	raw := strings.TrimPrefix(header, "sk_")
	if len(raw) < 16 {
		return nil, nil, invalid("invalid")
	}

	records, err := apiKeyRepo.FindByPrefix(raw[:16])
	if err != nil {
		outcome := apiKeyLookupOutcome(err)
		if outcome == "failure" {
			slog.Error("API key lookup failed", "error", err)
		}
		return nil, nil, invalid(outcome)
	}

	// Find the record whose hash matches.
	var record *models.APIKey
	for _, r := range records {
		if !models.APIKeyHashIsArgon2(r.KeyHash) {
			if models.VerifyAPIKeyHash(raw, r.KeyHash) {
				record = r
				break
			}
			continue
		}
		if !limiter.acquire() {
			RecordAPIKeyAuth("busy")
			return nil, nil, &APIKeyAuthFailure{Status: http.StatusServiceUnavailable, Message: "Server busy, retry later", Outcome: "busy"}
		}
		ok := models.VerifyAPIKeyHash(raw, r.KeyHash)
		limiter.release()
		if ok {
			record = r
			break
		}
	}
	if record == nil {
		return nil, nil, invalid("invalid")
	}

	// Reject expired keys.
	if record.ExpiresAt != nil && record.ExpiresAt.Before(time.Now()) {
		RecordAPIKeyAuth("expired")
		return nil, nil, &APIKeyAuthFailure{Status: http.StatusUnauthorized, Message: "API key expired", Outcome: "expired"}
	}

	// Load the associated user to get role and current username.
	user, err := userRepo.FindByID(record.UserID)
	if err != nil {
		outcome := apiKeyLookupOutcome(err)
		if outcome == "failure" {
			slog.Error("API key user lookup failed", "user_id", record.UserID, "error", err)
		}
		return nil, nil, invalid(outcome)
	}

	if user.Disabled {
		RecordAPIKeyAuth("disabled")
		return nil, nil, &APIKeyAuthFailure{Status: http.StatusForbidden, Message: "Account disabled", Outcome: "disabled"}
	}

	// Throttled async update of last-used timestamp — at most once per key
	// per minute to avoid unnecessary DB writes and connection pool pressure.
	now := time.Now().UTC()
	prev, exists := lastUsedCache.Get(record.ID)
	if !exists || now.Sub(prev) > time.Minute {
		lastUsedCache.Set(record.ID, now)
		go func(userID, keyID string, ts time.Time) {
			_ = apiKeyRepo.UpdateLastUsed(userID, keyID, ts)
		}(record.UserID, record.ID, now)
	}
	RecordAPIKeyAuth("success")
	return record, user, nil
}
