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
	UserID       string `json:"user_id"`
	Username     string `json:"username"`
	Role         string `json:"role"`
	AuthProvider string `json:"auth_provider,omitempty"`
	Email        string `json:"email,omitempty"`
	jwt.RegisteredClaims
}

// ValidateJWT parses and validates a JWT token string, returning the claims if valid.
func ValidateJWT(tokenStr string, jwtSecret string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid or expired token")
	}
	claims, ok := token.Claims.(*Claims)
	if !ok {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
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

		claims, err := ValidateJWT(parts[1], jwtSecret)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			return
		}

		c.Set(contextKeyUserID, claims.UserID)
		c.Set(contextKeyUsername, claims.Username)
		c.Set(contextKeyRole, claims.Role)
		c.Next()
	}
}

// GenerateTokenOptions holds all parameters for JWT generation.
type GenerateTokenOptions struct {
	UserID       string
	Username     string
	Role         string
	Secret       string
	Expiration   time.Duration
	AuthProvider string // optional, included in claims when non-empty
	Email        string // optional, included in claims when non-empty
}

// GenerateTokenWithOpts creates a signed JWT token using the provided options.
func GenerateTokenWithOpts(opts GenerateTokenOptions) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:       opts.UserID,
		Username:     opts.Username,
		Role:         opts.Role,
		AuthProvider: opts.AuthProvider,
		Email:        opts.Email,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(opts.Expiration)),
			IssuedAt:  jwt.NewNumericDate(now),
			Subject:   opts.UserID,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(opts.Secret))
}

// GenerateToken creates a signed JWT token for the given user.
// Deprecated: Use GenerateTokenWithOpts for new code that needs auth_provider or email in claims.
func GenerateToken(userID, username, role, secret string, expiration time.Duration) (string, error) {
	return GenerateTokenWithOpts(GenerateTokenOptions{
		UserID:     userID,
		Username:   username,
		Role:       role,
		Secret:     secret,
		Expiration: expiration,
	})
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
