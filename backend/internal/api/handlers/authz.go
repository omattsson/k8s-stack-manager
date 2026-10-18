package handlers

import (
	"log/slog"
	"net/http"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// Role names as stored in models.User.Role and injected into the gin context
// by the auth middleware.
const (
	roleAdmin  = "admin"
	roleDevOps = "devops"
)

// msgInstanceModifyForbidden is the 403 message for callers that are not
// allowed to modify a stack instance.
const msgInstanceModifyForbidden = "You are not allowed to modify this stack instance"

// canModifyInstance reports whether the caller may modify the stack instance
// or run a lifecycle operation on it (deploy, stop, clean, delete, rollback,
// actions, overrides, update, extend, deploy preview).
//
// Rule: the instance owner, an admin and a devops user are allowed. Every
// other authenticated user may only read the instance, its status, pods,
// deploy log and access URLs.
func canModifyInstance(c *gin.Context, inst *models.StackInstance) bool {
	if inst == nil {
		return false
	}
	if isPrivilegedRole(c) {
		return true
	}
	userID := middleware.GetUserIDFromContext(c)
	return userID != "" && inst.OwnerID == userID
}

// requireInstanceModify writes a 403 response and returns false when the
// caller may not modify the instance. Call it right after the instance lookup
// and before any side effect (hooks, status change, deploy log, Helm call).
func requireInstanceModify(c *gin.Context, inst *models.StackInstance) bool {
	if canModifyInstance(c, inst) {
		return true
	}
	instanceID := ""
	if inst != nil {
		instanceID = inst.ID
	}
	slog.Warn("Instance modify denied",
		"user_id", middleware.GetUserIDFromContext(c),
		"role", middleware.GetRoleFromContext(c),
		"instance_id", instanceID,
		"method", c.Request.Method,
		"path", c.FullPath(),
	)
	c.JSON(http.StatusForbidden, gin.H{"error": msgInstanceModifyForbidden})
	return false
}
