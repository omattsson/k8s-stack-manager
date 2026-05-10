package handlers

import (
	"fmt"
	"net/http"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/config"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// API key handler message constants.
const (
	msgForbidden = "Forbidden"
)

// APIKeyHandler handles API key management endpoints.
type APIKeyHandler struct {
	apiKeyRepo            models.APIKeyRepository
	userRepo              models.UserRepository
	apiKeyMaxLifetimeDays int
}

// NewAPIKeyHandler creates a new APIKeyHandler.
func NewAPIKeyHandler(apiKeyRepo models.APIKeyRepository, userRepo models.UserRepository, authCfg *config.AuthConfig) *APIKeyHandler {
	maxDays := 0
	if authCfg != nil {
		maxDays = authCfg.APIKeyMaxLifetimeDays
	}
	return &APIKeyHandler{apiKeyRepo: apiKeyRepo, userRepo: userRepo, apiKeyMaxLifetimeDays: maxDays}
}

// CreateAPIKeyRequest is the request body for creating an API key.
type CreateAPIKeyRequest struct {
	Name          string  `json:"name" binding:"required"`
	ExpiresAt     *string `json:"expires_at,omitempty"`
	ExpiresInDays *int    `json:"expires_in_days,omitempty"`
}

// CreateAPIKeyResponse is returned once at key creation time.
// It includes the raw key which is never stored and cannot be retrieved again.
type CreateAPIKeyResponse struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	RawKey    string     `json:"raw_key"` // sk_<hex> — shown once, store securely
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// CreateAPIKey godoc
// @Summary      Create an API key for a user
// @Description  Generates a new API key. An expiration is required: set expires_at (YYYY-MM-DD or RFC3339) or expires_in_days (positive int), but not both. If API_KEY_MAX_LIFETIME_DAYS is configured, the expiry must not exceed the limit. The raw key is returned once in raw_key and cannot be retrieved again.
// @Tags         api-keys
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      string               true  "User ID"
// @Param        request  body      CreateAPIKeyRequest  true  "API key details"
// @Success      201  {object}  CreateAPIKeyResponse
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/users/{id}/api-keys [post]
func (h *APIKeyHandler) CreateAPIKey(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "User ID is required"})
		return
	}

	if !canAccessUserKeys(c, userID) {
		c.JSON(http.StatusForbidden, gin.H{"error": msgForbidden})
		return
	}

	// Verify the target user exists before generating a key for them.
	if _, err := h.userRepo.FindByID(userID); err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}

	var req CreateAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	// Normalize empty expires_at to nil so it doesn't conflict with expires_in_days.
	if req.ExpiresAt != nil && *req.ExpiresAt == "" {
		req.ExpiresAt = nil
	}

	var expiresAt *time.Time
	if req.ExpiresAt != nil && req.ExpiresInDays != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot specify both expires_at and expires_in_days"})
		return
	}

	now := time.Now().UTC()

	if req.ExpiresInDays != nil {
		if *req.ExpiresInDays <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expires_in_days must be positive"})
			return
		}
		t := now.AddDate(0, 0, *req.ExpiresInDays)
		expiresAt = &t
	} else if req.ExpiresAt != nil {
		parsed, perr := time.Parse(time.RFC3339, *req.ExpiresAt)
		if perr != nil {
			// Try date-only format: treat as end-of-day UTC.
			parsed, perr = time.Parse("2006-01-02", *req.ExpiresAt)
			if perr != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid expires_at format; use YYYY-MM-DD or RFC3339"})
				return
			}
			parsed = parsed.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
		}
		parsed = parsed.UTC()
		expiresAt = &parsed
	}

	// Reject expiry dates that are not strictly in the future.
	if expiresAt != nil && !expiresAt.After(now) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Expiry date must be in the future"})
		return
	}

	if expiresAt == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Expiration is required; set expires_at or expires_in_days"})
		return
	}

	// Enforce max lifetime policy.
	if h.apiKeyMaxLifetimeDays > 0 {
		maxDate := now.AddDate(0, 0, h.apiKeyMaxLifetimeDays)
		maxExpiry := time.Date(maxDate.Year(), maxDate.Month(), maxDate.Day(), 23, 59, 59, 999999999, time.UTC)
		if expiresAt.After(maxExpiry) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Expiry exceeds maximum allowed lifetime of %d days", h.apiKeyMaxLifetimeDays)})
			return
		}
	}

	// Generate key only after all validation passes to avoid wasted crypto work.
	rawKey, prefix, hash, err := models.GenerateAPIKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	key := &models.APIKey{
		UserID:    userID,
		Name:      req.Name,
		KeyHash:   hash,
		Prefix:    prefix,
		ExpiresAt: expiresAt,
	}

	if err := h.apiKeyRepo.Create(key); err != nil {
		status, message := mapError(err, "API key")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusCreated, CreateAPIKeyResponse{
		ID:        key.ID,
		Name:      key.Name,
		Prefix:    prefix,
		RawKey:    "sk_" + rawKey,
		CreatedAt: key.CreatedAt,
		ExpiresAt: key.ExpiresAt,
	})
}

// ListAPIKeys godoc
// @Summary      List API keys for a user
// @Description  Returns all API keys for the given user. Admin or own user only.
// @Tags         api-keys
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "User ID"
// @Success      200  {array}   models.APIKey
// @Failure      401  {object}  map[string]string
// @Failure      403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Router       /api/v1/users/{id}/api-keys [get]
func (h *APIKeyHandler) ListAPIKeys(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "User ID is required"})
		return
	}

	if !canAccessUserKeys(c, userID) {
		c.JSON(http.StatusForbidden, gin.H{"error": msgForbidden})
		return
	}

	// Verify the target user exists.
	if _, err := h.userRepo.FindByID(userID); err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}

	keys, err := h.apiKeyRepo.ListByUser(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	c.JSON(http.StatusOK, keys)
}

// DeleteAPIKey godoc
// @Summary      Delete an API key
// @Description  Deletes the specified API key. Admin or own user only.
// @Tags         api-keys
// @Produce      json
// @Security     BearerAuth
// @Param        id     path      string  true  "User ID"
// @Param        keyId  path      string  true  "API Key ID"
// @Success      204
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Router       /api/v1/users/{id}/api-keys/{keyId} [delete]
func (h *APIKeyHandler) DeleteAPIKey(c *gin.Context) {
	userID := c.Param("id")
	keyID := c.Param("keyId")
	if userID == "" || keyID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "User ID and key ID are required"})
		return
	}

	if !canAccessUserKeys(c, userID) {
		c.JSON(http.StatusForbidden, gin.H{"error": msgForbidden})
		return
	}

	if err := h.apiKeyRepo.Delete(userID, keyID); err != nil {
		status, message := mapError(err, "API key")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}

// canAccessUserKeys returns true when the caller is allowed to manage API keys
// for the given userID. Admins may manage any user; others may only manage their own.
func canAccessUserKeys(c *gin.Context, userID string) bool {
	callerRole := middleware.GetRoleFromContext(c)
	callerID := middleware.GetUserIDFromContext(c)
	return callerRole == "admin" || callerID == userID
}
