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

// APIKeyAuthDeps holds the dependencies for combined JWT + API-key auth.
type APIKeyAuthDeps struct {
	JWTSecret    string
	APIKeyRepo   models.APIKeyRepository
	UserRepo     models.UserRepository
	SessionStore sessionstore.SessionStore
}

// CombinedAuth returns middleware that accepts either:
//  1. Authorization: Bearer <jwt>  — existing JWT path
//  2. X-API-Key: sk_<rawKey>       — API key path
//
// When APIKeyRepo or UserRepo is nil the middleware falls back to JWT-only auth.
func CombinedAuth(deps APIKeyAuthDeps) gin.HandlerFunc {
	if deps.APIKeyRepo == nil || deps.UserRepo == nil {
		return AuthRequiredWithSessionStore(deps.JWTSecret, deps.SessionStore)
	}

	jwtMW := AuthRequiredWithSessionStore(deps.JWTSecret, deps.SessionStore)

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

		// Strip the sk_ prefix.
		raw := strings.TrimPrefix(apiKeyHeader, "sk_")
		if len(raw) < 16 {
			RecordAPIKeyAuth("invalid")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			return
		}

		prefix := raw[:16]

		records, err := deps.APIKeyRepo.FindByPrefix(prefix)
		if err != nil {
			outcome := apiKeyLookupOutcome(err)
			if outcome == "failure" {
				slog.Error("API key lookup failed", "error", err)
			}
			RecordAPIKeyAuth(outcome)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			return
		}
		if len(records) == 0 {
			RecordAPIKeyAuth("invalid")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			return
		}

		// Find the record whose hash matches.
		var record *models.APIKey
		for _, r := range records {
			if models.VerifyAPIKeyHash(raw, r.KeyHash) {
				record = r
				break
			}
		}
		if record == nil {
			RecordAPIKeyAuth("invalid")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			return
		}

		// Reject expired keys.
		if record.ExpiresAt != nil && record.ExpiresAt.Before(time.Now()) {
			RecordAPIKeyAuth("expired")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "API key expired"})
			return
		}

		// Load the associated user to get role and current username.
		user, err := deps.UserRepo.FindByID(record.UserID)
		if err != nil {
			outcome := apiKeyLookupOutcome(err)
			if outcome == "failure" {
				slog.Error("API key user lookup failed", "user_id", record.UserID, "error", err)
			}
			RecordAPIKeyAuth(outcome)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			return
		}

		if user.Disabled {
			RecordAPIKeyAuth("disabled")
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Account disabled"})
			return
		}

		// Inject the same context keys that JWT middleware sets.
		c.Set(contextKeyUserID, user.ID)
		c.Set(contextKeyUsername, user.Username)
		c.Set(contextKeyRole, user.Role)

		// Throttled async update of last-used timestamp — at most once per key
		// per minute to avoid unnecessary DB writes and connection pool pressure.
		now := time.Now().UTC()
		prev, exists := lastUsedCache.Get(record.ID)
		shouldUpdate := !exists || now.Sub(prev) > time.Minute
		if shouldUpdate {
			lastUsedCache.Set(record.ID, now)
			go func(userID, keyID string, ts time.Time) {
				_ = deps.APIKeyRepo.UpdateLastUsed(userID, keyID, ts)
			}(record.UserID, record.ID, now)
		}
		RecordAPIKeyAuth("success")
	}
}
