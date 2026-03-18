package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// roleHierarchy maps each role to its level in the permission hierarchy.
// Higher level = more permissions. admin > devops > user.
var roleHierarchy = map[string]int{
	"user":   1,
	"devops": 2,
	"admin":  3,
}

// RequireRole returns middleware that checks whether the authenticated user's role
// is at least as powerful as one of the given roles (using the role hierarchy).
func RequireRole(roles ...string) gin.HandlerFunc {
	// Precompute the minimum required level.
	minLevel := 0
	for _, r := range roles {
		if l, ok := roleHierarchy[r]; ok {
			if minLevel == 0 || l < minLevel {
				minLevel = l
			}
		}
	}

	return func(c *gin.Context) {
		userRole := GetRoleFromContext(c)
		if userRole == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
			return
		}

		userLevel, ok := roleHierarchy[userRole]
		if !ok || userLevel < minLevel {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Insufficient permissions"})
			return
		}

		c.Next()
	}
}

// RequireAdmin is a convenience wrapper that requires admin role.
func RequireAdmin() gin.HandlerFunc {
	return RequireRole("admin")
}

// RequireDevOps is a convenience wrapper that requires devops role (or admin).
func RequireDevOps() gin.HandlerFunc {
	return RequireRole("devops")
}

// RequireUser is a convenience wrapper that requires at least user role.
func RequireUser() gin.HandlerFunc {
	return RequireRole("user")
}
