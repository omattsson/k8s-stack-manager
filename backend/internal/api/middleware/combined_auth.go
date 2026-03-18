package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// APIKeyAuthDeps holds the dependencies for combined JWT + API-key auth.
type APIKeyAuthDeps struct {
	JWTSecret  string
	APIKeyRepo models.APIKeyRepository
	UserRepo   models.UserRepository
}

// CombinedAuth returns middleware that accepts either:
//  1. Authorization: Bearer <jwt>  — existing JWT path
//  2. X-API-Key: sk_<rawKey>       — API key path
//
// When APIKeyRepo or UserRepo is nil the middleware falls back to JWT-only auth.
func CombinedAuth(deps APIKeyAuthDeps) gin.HandlerFunc {
	if deps.APIKeyRepo == nil || deps.UserRepo == nil {
		return AuthRequired(deps.JWTSecret)
	}

	jwtMW := AuthRequired(deps.JWTSecret)

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
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization required"})
			return
		}

		// Strip the sk_ prefix.
		raw := strings.TrimPrefix(apiKeyHeader, "sk_")
		if len(raw) < 16 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			return
		}

		prefix := raw[:16]
		hash := models.HashAPIKey(raw)

		records, err := deps.APIKeyRepo.FindByPrefix(prefix)
		if err != nil || len(records) == 0 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			return
		}

		// Find the record whose hash matches.
		var record *models.APIKey
		for _, r := range records {
			if subtle.ConstantTimeCompare([]byte(hash), []byte(r.KeyHash)) == 1 {
				record = r
				break
			}
		}
		if record == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			return
		}

		// Reject expired keys.
		if record.ExpiresAt != nil && record.ExpiresAt.Before(time.Now()) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "API key expired"})
			return
		}

		// Load the associated user to get role and current username.
		user, err := deps.UserRepo.FindByID(record.UserID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			return
		}

		// Inject the same context keys that JWT middleware sets.
		c.Set(contextKeyUserID, user.ID)
		c.Set(contextKeyUsername, user.Username)
		c.Set(contextKeyRole, user.Role)

		// Synchronous best-effort update of last-used timestamp.
		_ = deps.APIKeyRepo.UpdateLastUsed(record.UserID, record.ID, time.Now().UTC())
	}
}
