package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/models"
	"backend/internal/sessionstore"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// UserHandler handles user management endpoints.
// For creating users, reuse the existing POST /api/v1/auth/register endpoint
// (AuthHandler.Register), which already handles user creation with role assignment.
type UserHandler struct {
	userRepo              models.UserRepository
	sessionStore          sessionstore.SessionStore
	refreshTokenRepo      models.RefreshTokenRepository
	apiKeyRepo            models.APIKeyRepository
	wsRevoker             websocket.ClientRevoker
	accessTokenExpiration time.Duration
	jwtExpiration         time.Duration
}

// NewUserHandler creates a new UserHandler. refreshTokenRepo and apiKeyRepo are
// used by revokeUserAccess; pass nil to skip that revocation step (tests only).
func NewUserHandler(
	userRepo models.UserRepository,
	refreshTokenRepo models.RefreshTokenRepository,
	apiKeyRepo models.APIKeyRepository,
) *UserHandler {
	return &UserHandler{
		userRepo:         userRepo,
		refreshTokenRepo: refreshTokenRepo,
		apiKeyRepo:       apiKeyRepo,
	}
}

func (h *UserHandler) SetSessionStore(store sessionstore.SessionStore) { h.sessionStore = store }
func (h *UserHandler) SetAccessTokenExpiration(d time.Duration)        { h.accessTokenExpiration = d }
func (h *UserHandler) SetJWTExpiration(d time.Duration)                { h.jwtExpiration = d }

// SetWebSocketRevoker sets the hub that closes the open WebSocket connections
// of a revoked user. nil: sockets close at access-token expiry only.
func (h *UserHandler) SetWebSocketRevoker(r websocket.ClientRevoker) { h.wsRevoker = r }

// blockIssuedTokens writes a user block that revokes every access token of
// the user issued at or before now. It lives until the longest access-token
// lifetime has passed. Tokens issued later pass. Logs and continues on error.
func (h *UserHandler) blockIssuedTokens(ctx context.Context, userID string) {
	if h.sessionStore == nil {
		return
	}
	ttl := h.accessTokenExpiration
	if h.jwtExpiration > ttl {
		ttl = h.jwtExpiration
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if err := h.sessionStore.BlockUser(ctx, userID, time.Now().Add(ttl)); err != nil {
		slog.Warn("Failed to block user in session store", "user_id", userID, "error", err)
	}
}

// revokeOptions selects the optional steps of revokeUserAccess.
type revokeOptions struct {
	// deleteAPIKeys deletes all API keys of the user. Only DeleteUser sets it.
	// Disable keeps the keys: API-key auth already rejects a disabled user
	// (User.Disabled check in CombinedAuth), and enabling the user again
	// restores them. A password reset changes only the password, so the keys
	// stay valid.
	deleteAPIKeys bool
}

// revokeUserAccess ends the sessions of a user. DeleteUser, DisableUser and
// ResetUserPassword call it after their own change succeeds. Steps:
//  1. Block the user in the session store until the longest access-token
//     lifetime has passed. Access tokens issued at or before the block get
//     401; tokens issued later (for example after a password reset) work.
//  2. Revoke all refresh tokens of the user (explicit; no reliance on a
//     foreign-key cascade).
//  3. If opts.deleteAPIKeys is set, delete all API keys of the user in one
//     statement.
//  4. Close the open WebSocket connections of the user on this replica.
//     The block from step 1 refuses a reconnect.
//
// Error policy: log and continue. Each step runs even when an earlier step
// fails, and the request still succeeds. The caller has already committed its
// change (row deleted, user disabled, password changed), so a failure status
// would report a change that took effect. Other guards stay in place on a
// partial failure: refresh and API-key auth reload the user and reject a
// deleted or disabled user.
//
// Callers pass context.WithoutCancel(c.Request.Context()) so a client that
// disconnects after the commit cannot cancel the revocation.
func (h *UserHandler) revokeUserAccess(ctx context.Context, userID string, opts revokeOptions) {
	h.blockIssuedTokens(ctx, userID)

	if h.refreshTokenRepo != nil {
		if err := h.refreshTokenRepo.RevokeAllForUser(userID); err != nil {
			slog.Warn("Failed to revoke refresh tokens", "user_id", userID, "error", err)
		}
	}

	if opts.deleteAPIKeys && h.apiKeyRepo != nil {
		if _, err := h.apiKeyRepo.DeleteAllForUser(userID); err != nil {
			slog.Warn("Failed to delete API keys", "user_id", userID, "error", err)
		}
	}

	if h.wsRevoker != nil {
		if n := h.wsRevoker.DisconnectUser(userID); n > 0 {
			slog.Info("Closed WebSocket connections of revoked user", "user_id", userID, "connections", n)
		}
	}
}

// ListUsers godoc
// @Summary      List all users
// @Description  Returns all registered users. Admin only. PasswordHash is never included.
// @Tags         users
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   models.User
// @Failure      401  {object}  map[string]string
// @Failure      403  {object}  map[string]string
// @Router       /api/v1/users [get]
func (h *UserHandler) ListUsers(c *gin.Context) {
	users, err := h.userRepo.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	// models.User.PasswordHash is tagged json:"-" so it is never serialised.
	c.JSON(http.StatusOK, users)
}

// DeleteUser godoc
// @Summary      Delete a user
// @Description  Permanently deletes a user account. Admin only. Cannot delete own account. Revokes the user's current access tokens and all refresh tokens, and deletes all API keys of the user.
// @Tags         users
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "User ID"
// @Success      204
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Router       /api/v1/users/{id} [delete]
func (h *UserHandler) DeleteUser(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "User ID is required"})
		return
	}

	// Prevent admins from deleting their own account.
	callerID := middleware.GetUserIDFromContext(c)
	if id == callerID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete your own account"})
		return
	}

	if err := h.userRepo.Delete(id); err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}

	h.revokeUserAccess(context.WithoutCancel(c.Request.Context()), id, revokeOptions{deleteAPIKeys: true})

	c.Status(http.StatusNoContent)
}

// DisableUser godoc
// @Summary      Disable a user
// @Description  Disables a user account. Admin only. Revokes the user's current access tokens and all refresh tokens. API keys stop working while the user is disabled and work again after enable.
// @Tags         users
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "User ID"
// @Success      200  {object}  map[string]string
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/users/{id}/disable [put]
func (h *UserHandler) DisableUser(c *gin.Context) {
	h.setDisabled(c, true)
}

// EnableUser godoc
// @Summary      Enable a user
// @Description  Re-enables a previously disabled user account. Admin only. Access tokens issued before the enable stay revoked; the user must log in again.
// @Tags         users
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "User ID"
// @Success      200  {object}  map[string]string
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/users/{id}/enable [put]
func (h *UserHandler) EnableUser(c *gin.Context) {
	h.setDisabled(c, false)
}

func (h *UserHandler) setDisabled(c *gin.Context, disabled bool) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "User ID is required"})
		return
	}

	callerID := middleware.GetUserIDFromContext(c)
	if id == callerID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot change your own account status"})
		return
	}

	user, err := h.userRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}

	user.Disabled = disabled
	if err := h.userRepo.Update(user); err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if disabled {
		h.revokeUserAccess(context.WithoutCancel(c.Request.Context()), id, revokeOptions{})
	} else {
		// Do not unblock: that would make tokens issued before the disable
		// valid again. A fresh block (block time = now) lets only new logins
		// through and also replaces a legacy block row without a block time.
		h.blockIssuedTokens(context.WithoutCancel(c.Request.Context()), id)
	}

	action := "enabled"
	if disabled {
		action = "disabled"
	}
	c.JSON(http.StatusOK, gin.H{"message": "User " + action + " successfully"})
}

// ResetUserPassword godoc
// @Summary      Reset user password
// @Description  Resets the password for a local/service account user. Admin only. Revokes the user's current access tokens and all refresh tokens. API keys stay valid.
// @Tags         users
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      string                 true  "User ID"
// @Param        request  body      ResetPasswordRequest   true  "New password"
// @Success      200  {object}  map[string]string
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/users/{id}/password [put]
func (h *UserHandler) ResetUserPassword(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "User ID is required"})
		return
	}

	var req ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	if len(req.Password) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Password must be at least 8 characters"})
		return
	}

	user, err := h.userRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}

	if user.AuthProvider != "" && user.AuthProvider != "local" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot reset password for non-local user"})
		return
	}

	bcryptSem <- struct{}{}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	<-bcryptSem
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	user.PasswordHash = string(hash)
	if err := h.userRepo.Update(user); err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}

	h.revokeUserAccess(context.WithoutCancel(c.Request.Context()), id, revokeOptions{})

	c.JSON(http.StatusOK, gin.H{"message": "Password reset successfully"})
}

// ResetPasswordRequest is the request body for password reset.
type ResetPasswordRequest struct {
	Password string `json:"password" binding:"required"`
}
