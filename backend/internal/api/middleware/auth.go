package middleware

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const (
	contextKeyUserID   = "userID"
	contextKeyUsername = "username"
	contextKeyRole     = "role"
)

// Claims represents the JWT claims payload.
type Claims struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// AuthRequired returns middleware that validates JWT tokens from the Authorization header.
func AuthRequired(jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is required"})
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header must be Bearer {token}"})
			return
		}

		tokenString := parts[1]
		claims := &Claims{}

		token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(jwtSecret), nil
		})
		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			return
		}

		c.Set(contextKeyUserID, claims.UserID)
		c.Set(contextKeyUsername, claims.Username)
		c.Set(contextKeyRole, claims.Role)
		c.Next()
	}
}

// GenerateToken creates a signed JWT token for the given user.
func GenerateToken(userID, username, role, secret string, expiration time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(expiration)),
			IssuedAt:  jwt.NewNumericDate(now),
			Subject:   userID,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// GetUserIDFromContext extracts the user ID set by AuthRequired middleware.
func GetUserIDFromContext(c *gin.Context) string {
	if v, exists := c.Get(contextKeyUserID); exists {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetUsernameFromContext extracts the username set by AuthRequired middleware.
func GetUsernameFromContext(c *gin.Context) string {
	if v, exists := c.Get(contextKeyUsername); exists {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetRoleFromContext extracts the role set by AuthRequired middleware.
func GetRoleFromContext(c *gin.Context) string {
	if v, exists := c.Get(contextKeyRole); exists {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
