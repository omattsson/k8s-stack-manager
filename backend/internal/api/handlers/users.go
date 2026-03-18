package handlers

import (
	"net/http"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// UserHandler handles user management endpoints.
// For creating users, reuse the existing POST /api/v1/auth/register endpoint
// (AuthHandler.Register), which already handles user creation with role assignment.
type UserHandler struct {
	userRepo models.UserRepository
}

// NewUserHandler creates a new UserHandler.
func NewUserHandler(userRepo models.UserRepository) *UserHandler {
	return &UserHandler{userRepo: userRepo}
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
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
