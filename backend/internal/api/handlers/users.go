package handlers

import (
	"log/slog"
	"net/http"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/models"
	"backend/internal/sessionstore"

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
	accessTokenExpiration time.Duration
	jwtExpiration         time.Duration
}

// NewUserHandler creates a new UserHandler.
func NewUserHandler(userRepo models.UserRepository) *UserHandler {
	return &UserHandler{userRepo: userRepo}
}

func (h *UserHandler) SetSessionStore(store sessionstore.SessionStore) { h.sessionStore = store }
func (h *UserHandler) SetRefreshTokenRepo(repo models.RefreshTokenRepository) {
	h.refreshTokenRepo = repo
}
func (h *UserHandler) SetAccessTokenExpiration(d time.Duration) { h.accessTokenExpiration = d }
func (h *UserHandler) SetJWTExpiration(d time.Duration)         { h.jwtExpiration = d }

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
// @Description  Permanently deletes a user account. Admin only. Cannot delete own account.
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

	c.Status(http.StatusNoContent)
}

// DisableUser godoc
// @Summary      Disable a user
// @Description  Disables a user account. All API keys for this user immediately stop working. Admin only.
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
// @Description  Re-enables a previously disabled user account. Admin only.
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
		if h.refreshTokenRepo != nil {
			if err := h.refreshTokenRepo.RevokeAllForUser(id); err != nil {
				slog.Warn("Failed to revoke refresh tokens on disable", "user_id", id, "error", err)
			}
		}
		if h.sessionStore != nil {
			ttl := h.accessTokenExpiration
			if h.jwtExpiration > ttl {
				ttl = h.jwtExpiration
			}
			if ttl <= 0 {
				ttl = 24 * time.Hour
			}
			if err := h.sessionStore.BlockUser(c.Request.Context(), id, time.Now().Add(ttl)); err != nil {
				slog.Warn("Failed to block user in session store", "user_id", id, "error", err)
			}
		}
	} else {
		if h.sessionStore != nil {
			if err := h.sessionStore.UnblockUser(c.Request.Context(), id); err != nil {
				slog.Warn("Failed to unblock user in session store", "user_id", id, "error", err)
			}
		}
	}

	action := "enabled"
	if disabled {
		action = "disabled"
	}
	c.JSON(http.StatusOK, gin.H{"message": "User " + action + " successfully"})
}

// ResetUserPassword godoc
// @Summary      Reset user password
// @Description  Resets the password for a local/service account user. Admin only. Revokes all sessions.
// @Tags         users
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      string                 true  "User ID"
// @Param        request  body      resetPasswordRequest   true  "New password"
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

	var req resetPasswordRequest
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

	if h.refreshTokenRepo != nil {
		if err := h.refreshTokenRepo.RevokeAllForUser(id); err != nil {
			slog.Warn("Failed to revoke refresh tokens after password reset", "user_id", id, "error", err)
		}
	}
	if h.sessionStore != nil {
		ttl := h.accessTokenExpiration
		if h.jwtExpiration > ttl {
			ttl = h.jwtExpiration
		}
		if ttl <= 0 {
			ttl = 24 * time.Hour
		}
		if err := h.sessionStore.BlockUser(c.Request.Context(), id, time.Now().Add(ttl)); err != nil {
			slog.Warn("Failed to block user in session store after password reset", "user_id", id, "error", err)
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "Password reset successfully"})
}

type resetPasswordRequest struct {
	Password string `json:"password" binding:"required"`
}
