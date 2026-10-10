package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
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
	// stay valid. A role change keeps the keys too: API-key auth reads the
	// current role from the database on each request.
	deleteAPIKeys bool
}

// revokeUserAccess ends the sessions of a user. DeleteUser, DisableUser,
// ResetUserPassword and ChangeUserRole call it after their own change
// succeeds. Steps:
//  1. Block the user in the session store until the longest access-token
//     lifetime has passed. Access tokens issued at or before the block get
//     401; tokens issued later (for example after a password reset) work.
//  2. Revoke all refresh tokens of the user (explicit; no reliance on a
//     foreign-key cascade).
//  3. If opts.deleteAPIKeys is set, delete all API keys of the user in one
//     statement.
//  4. Close the open WebSocket connections of the user on this replica,
//     and with WebSocket fan-out also on the other replicas. The block from
//     step 1 refuses a reconnect.
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
// @Description  Permanently deletes a user account. Admin only. Cannot delete own account (400). The last enabled admin cannot be deleted (409). The caller must still be an enabled admin in the database (403). Revokes the user's current access tokens and all refresh tokens, and deletes all API keys of the user.
// @Tags         users
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "User ID"
// @Success      204
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      409  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/users/{id} [delete]
func (h *UserHandler) DeleteUser(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "User ID is required"})
		return
	}

	// Prevent admins from deleting their own account.
	callerID := middleware.GetUserIDFromContext(c)
	if isSameUserID(id, callerID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete your own account"})
		return
	}

	user, err := h.userRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}
	// From here on use the stored (canonical) ID: MySQL compares IDs without
	// case, so the path ID can differ from it in case only.
	if isSameUserID(user.ID, callerID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete your own account"})
		return
	}

	if err := h.userRepo.DeleteGuarded(callerID, user.ID); err != nil {
		writeGuardedChangeError(c, err)
		return
	}

	h.revokeUserAccess(context.WithoutCancel(c.Request.Context()), user.ID, revokeOptions{deleteAPIKeys: true})

	c.Status(http.StatusNoContent)
}

// DisableUser godoc
// @Summary      Disable a user
// @Description  Disables a user account. Admin only. The last enabled admin cannot be disabled (409). The caller must still be an enabled admin in the database (403). Revokes the user's current access tokens and all refresh tokens. API keys stop working while the user is disabled and work again after enable.
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
// @Failure      409  {object}  map[string]string
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
	if isSameUserID(id, callerID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot change your own account status"})
		return
	}

	user, err := h.userRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}
	// Use the stored (canonical) ID from here on (see DeleteUser).
	if isSameUserID(user.ID, callerID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot change your own account status"})
		return
	}

	if err := h.userRepo.SetDisabled(callerID, user.ID, disabled); err != nil {
		writeGuardedChangeError(c, err)
		return
	}

	if disabled {
		h.revokeUserAccess(context.WithoutCancel(c.Request.Context()), user.ID, revokeOptions{})
	} else {
		// Do not unblock: that would make tokens issued before the disable
		// valid again. A fresh block (block time = now) lets only new logins
		// through and also replaces a legacy block row without a block time.
		h.blockIssuedTokens(context.WithoutCancel(c.Request.Context()), user.ID)
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

	// Write only the password hash: a full-row save would overwrite a
	// concurrent role or disabled change. Use the stored (canonical) ID (see
	// DeleteUser).
	if err := h.userRepo.UpdatePassword(user.ID, string(hash)); err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}

	h.revokeUserAccess(context.WithoutCancel(c.Request.Context()), user.ID, revokeOptions{})

	c.JSON(http.StatusOK, gin.H{"message": "Password reset successfully"})
}

// ResetPasswordRequest is the request body for password reset.
type ResetPasswordRequest struct {
	Password string `json:"password" binding:"required"`
}

// ChangeRoleRequest is the request body for a role change.
type ChangeRoleRequest struct {
	// Role is the new role: "user", "devops" or "admin".
	Role string `json:"role" binding:"required" enums:"user,devops,admin" example:"devops"`
}

// ChangeRoleResponse is the response of a role change.
type ChangeRoleResponse struct {
	ID      string `json:"id"`
	OldRole string `json:"old_role"`
	NewRole string `json:"new_role"`
	Message string `json:"message"`
	// Changed is false when the user already had the role (no-op: the
	// sessions of the user stay valid).
	Changed bool `json:"changed"`
}

// Messages of the role change rules.
const (
	msgRoleManagedByIdP = "Role is managed by the identity provider"
	msgLastAdmin        = "The last enabled admin cannot be demoted, disabled or deleted"
	msgCallerNotAdmin   = "Admin role required"
	msgOwnRole          = "Cannot change your own role"
	msgInvalidRole      = "Role must be one of: user, devops, admin"
)

// ChangeUserRole godoc
// @Summary      Change the role of a user
// @Description  Sets the role of a local user. Admin only. The role of an SSO user comes from the identity provider and cannot be changed here (409). An admin cannot change their own role (403), the caller must still be an enabled admin in the database (403), and the last enabled admin cannot lose the admin role (409). An unchanged role is a no-op (changed=false). A change revokes the user's current access tokens, all refresh tokens and open WebSocket connections, so the next request needs a new login with the new role. API keys stay valid and use the new role at once.
// @Tags         users
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      string             true  "User ID"
// @Param        request  body      ChangeRoleRequest  true  "New role"
// @Success      200  {object}  ChangeRoleResponse
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      403  {object}  map[string]string
// @Failure      404  {object}  map[string]string
// @Failure      409  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/users/{id}/role [put]
func (h *UserHandler) ChangeUserRole(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "User ID is required"})
		return
	}

	var req ChangeRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}
	if !models.IsValidRole(req.Role) {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRole})
		return
	}

	// An admin cannot change their own role: a demotion would lock the
	// caller out of user management.
	callerID := middleware.GetUserIDFromContext(c)
	if isSameUserID(id, callerID) {
		c.JSON(http.StatusForbidden, gin.H{"error": msgOwnRole})
		return
	}

	user, err := h.userRepo.FindByID(id)
	if err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}
	// Use the stored (canonical) ID from here on (see DeleteUser).
	if isSameUserID(user.ID, callerID) {
		c.JSON(http.StatusForbidden, gin.H{"error": msgOwnRole})
		return
	}

	// The OIDC login sets the role of an SSO user from the roles claim at
	// each login, so a manual change would not last.
	if user.AuthProvider != "" && user.AuthProvider != "local" {
		c.JSON(http.StatusConflict, gin.H{"error": msgRoleManagedByIdP})
		return
	}

	oldRole, err := h.userRepo.UpdateRole(callerID, user.ID, req.Role)
	if err != nil {
		writeGuardedChangeError(c, err)
		return
	}

	changed := oldRole != req.Role
	if changed {
		// Tokens carry the role claim. End the sessions so the next request
		// needs a new login, which issues a token with the new role.
		h.revokeUserAccess(context.WithoutCancel(c.Request.Context()), user.ID, revokeOptions{})
	}

	message := "Role changed"
	if !changed {
		message = "Role unchanged"
	}
	c.JSON(http.StatusOK, ChangeRoleResponse{
		ID:      user.ID,
		OldRole: oldRole,
		NewRole: req.Role,
		Changed: changed,
		Message: message,
	})
}

// isSameUserID compares user IDs without case, as the MySQL collation does.
func isSameUserID(a, b string) bool {
	return a != "" && strings.EqualFold(a, b)
}

// writeGuardedChangeError maps an error of a guarded admin change
// (UpdateRole, SetDisabled, DeleteGuarded).
func writeGuardedChangeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, models.ErrCallerNotAdmin):
		// The caller lost the admin role or was disabled after the token was
		// issued (for example by a concurrent change).
		c.JSON(http.StatusForbidden, gin.H{"error": msgCallerNotAdmin})
	case errors.Is(err, models.ErrLastAdmin):
		c.JSON(http.StatusConflict, gin.H{"error": msgLastAdmin})
	default:
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
	}
}
