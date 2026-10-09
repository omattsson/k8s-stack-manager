package middleware

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"backend/internal/cache"
	"backend/internal/sessionstore"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	contextKeyUserID   = "userID"
	contextKeyUsername = "username"
	contextKeyRole     = "role"
	contextKeyJTI      = "jti"
	contextKeyExpiry   = "tokenExpiry"
	contextKeySID      = "sessionID"
)

// Claims represents the JWT claims payload.
type Claims struct {
	UserID       string `json:"user_id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name,omitempty"`
	Role         string `json:"role"`
	AuthProvider string `json:"auth_provider,omitempty"`
	Email        string `json:"email,omitempty"`
	// SessionID is the refresh-token family of the login session ("sid").
	// Empty for tokens outside a refresh session (for example CLI tokens).
	SessionID string `json:"sid,omitempty"`
	jwt.RegisteredClaims
}

// ValidateJWT parses and validates a JWT token string, returning the claims if
// valid. The token must have an exp claim: every token the app issues has one
// (GenerateTokenWithOpts), and a token without exp would never expire.
func ValidateJWT(tokenStr string, jwtSecret string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jwtSecret), nil
	}, jwt.WithExpirationRequired())
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
	return AuthRequiredWithSessionStore(jwtSecret, nil)
}

// AuthRequiredWithSessionStore returns middleware that validates JWT tokens and checks
// the provided session store for revoked tokens. If store is nil, no revocation check is performed.
func AuthRequiredWithSessionStore(jwtSecret string, store sessionstore.SessionStore) gin.HandlerFunc {
	return AuthRequiredWithOptions(JWTAuthOptions{JWTSecret: jwtSecret, SessionStore: store})
}

// SessionActivityFunc records activity of a login session. sessionID is the
// "sid" claim (refresh-token family ID). The middleware calls it outside the
// request goroutine, so it must be safe for concurrent use.
type SessionActivityFunc func(ctx context.Context, sessionID string, at time.Time) error

// sessionActivityInterval is the minimum time between two activity writes for
// one session. It keeps the DB write rate at one per minute per session.
const sessionActivityInterval = time.Minute

// sessionActivityTimeout bounds one activity write, so a slow database cannot
// pile up goroutines.
const sessionActivityTimeout = 5 * time.Second

// JWTAuthOptions configures AuthRequiredWithOptions.
type JWTAuthOptions struct {
	JWTSecret    string
	SessionStore sessionstore.SessionStore // nil: no revocation checks
	// OnSessionActivity is called (throttled, async) for each authenticated
	// request with a "sid" claim. nil: no activity tracking.
	OnSessionActivity SessionActivityFunc
}

// sessionActivityTracker throttles OnSessionActivity calls per session.
type sessionActivityTracker struct {
	fn       SessionActivityFunc
	lastSeen *cache.TTLCache[time.Time]
}

func newSessionActivityTracker(fn SessionActivityFunc) *sessionActivityTracker {
	if fn == nil {
		return nil
	}
	return &sessionActivityTracker{
		fn:       fn,
		lastSeen: cache.New[time.Time](2*sessionActivityInterval, sessionActivityInterval),
	}
}

// touch records activity for sessionID at most once per interval. The write
// runs in a goroutine with a context that the client cannot cancel and that
// expires after sessionActivityTimeout.
func (t *sessionActivityTracker) touch(ctx context.Context, sessionID string) {
	if t == nil || sessionID == "" {
		return
	}
	now := time.Now().UTC()
	if prev, ok := t.lastSeen.Get(sessionID); ok && now.Sub(prev) < sessionActivityInterval {
		return
	}
	t.lastSeen.Set(sessionID, now)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionActivityTimeout)
	go func() {
		defer cancel()
		if err := t.fn(ctx, sessionID, now); err != nil {
			slog.Warn("Failed to record session activity", "error", err)
		}
	}()
}

// AuthRequiredWithOptions returns JWT middleware with revocation checks and
// optional session activity tracking.
func AuthRequiredWithOptions(opts JWTAuthOptions) gin.HandlerFunc {
	jwtSecret := opts.JWTSecret
	store := opts.SessionStore
	activity := newSessionActivityTracker(opts.OnSessionActivity)
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

		if revokeErr := CheckRevocation(c.Request.Context(), store, claims); revokeErr != nil {
			// 401 (not 403) so the frontend sends the user back to login.
			// Login itself still answers 403 "Account disabled" for a
			// disabled user.
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": RevocationMessage(revokeErr)})
			return
		}

		c.Set(contextKeyUserID, claims.UserID)
		c.Set(contextKeyUsername, claims.Username)
		c.Set(contextKeyRole, claims.Role)
		if claims.ID != "" {
			c.Set(contextKeyJTI, claims.ID)
		}
		if claims.ExpiresAt != nil {
			c.Set(contextKeyExpiry, claims.ExpiresAt.Time)
		}
		if claims.SessionID != "" {
			c.Set(contextKeySID, claims.SessionID)
			activity.touch(c.Request.Context(), claims.SessionID)
		}
		c.Next()
	}
}

// Revocation errors returned by CheckRevocation.
var (
	// ErrTokenRevoked means the jti of the token is on the token blocklist
	// (logout).
	ErrTokenRevoked = errors.New("token revoked")
	// ErrSessionRevoked means a user block covers the token: the token was
	// issued at or before the block (user deleted, disabled or password reset).
	ErrSessionRevoked = errors.New("session revoked")
)

// CheckRevocation checks validated claims against the session store. The JWT
// middleware and the WebSocket upgrade both use it. It checks:
//  1. The token blocklist (jti). A match returns ErrTokenRevoked.
//  2. The user blocklist (user ID and iat). A block revokes tokens issued at
//     or before the block. A token without iat gets a zero time and counts as
//     blocked. A match returns ErrSessionRevoked.
//
// A nil store or nil claims pass. On a store error the function logs the
// error and continues (fail open): access tokens are short-lived, and a
// database blip must not lock out every user.
func CheckRevocation(ctx context.Context, store sessionstore.SessionStore, claims *Claims) error {
	revoked, lookupErr := RevocationStatus(ctx, store, claims)
	if lookupErr != nil {
		slog.Error("Failed to check token or user blocklist",
			"jti", claims.ID, "user_id", claims.UserID, "error", lookupErr)
	}
	return revoked
}

// RevocationStatus runs the checks of CheckRevocation without logging. It
// returns the revocation (ErrTokenRevoked, ErrSessionRevoked or nil) and the
// store errors (nil when both checks ran). A failed check counts as not
// revoked (fail open); the other check still runs. Callers that check many
// tokens (the WebSocket revalidation) use lookupErr to stop early.
func RevocationStatus(ctx context.Context, store sessionstore.SessionStore, claims *Claims) (revoked error, lookupErr error) {
	if store == nil || claims == nil {
		return nil, nil
	}
	var errs []error
	if claims.ID != "" {
		blocked, err := store.IsTokenBlocked(ctx, claims.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("token blocklist: %w", err))
		} else if blocked {
			return ErrTokenRevoked, errors.Join(errs...)
		}
	}
	if claims.UserID != "" {
		var issuedAt time.Time
		if claims.IssuedAt != nil {
			issuedAt = claims.IssuedAt.Time
		}
		blocked, err := store.IsUserBlocked(ctx, claims.UserID, issuedAt)
		if err != nil {
			errs = append(errs, fmt.Errorf("user blocklist: %w", err))
		} else if blocked {
			return ErrSessionRevoked, errors.Join(errs...)
		}
	}
	return nil, errors.Join(errs...)
}

// RevocationMessage returns the client message for an error from
// CheckRevocation.
func RevocationMessage(err error) string {
	if errors.Is(err, ErrTokenRevoked) {
		return "Token has been revoked"
	}
	return "Session revoked"
}

// GenerateTokenOptions holds all parameters for JWT generation.
type GenerateTokenOptions struct {
	UserID       string
	Username     string
	DisplayName  string
	Role         string
	Secret       string
	Expiration   time.Duration
	AuthProvider string // optional, included in claims when non-empty
	Email        string // optional, included in claims when non-empty
	SessionID    string // optional "sid" claim: refresh-token family of the session
}

// GenerateTokenWithOpts creates a signed JWT token using the provided options.
func GenerateTokenWithOpts(opts GenerateTokenOptions) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:       opts.UserID,
		Username:     opts.Username,
		DisplayName:  opts.DisplayName,
		Role:         opts.Role,
		AuthProvider: opts.AuthProvider,
		Email:        opts.Email,
		SessionID:    opts.SessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.New().String(),
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

// GetTokenExpiryFromContext extracts the token expiry time set by AuthRequired middleware.
func GetTokenExpiryFromContext(c *gin.Context) (time.Time, bool) {
	if v, exists := c.Get(contextKeyExpiry); exists {
		if t, ok := v.(time.Time); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

// GetSessionIDFromContext returns the "sid" claim set by the JWT middleware,
// or "" for API-key auth and tokens without a session.
func GetSessionIDFromContext(c *gin.Context) string {
	if v, exists := c.Get(contextKeySID); exists {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetJTIFromContext extracts the JWT ID (jti) set by AuthRequired middleware.
func GetJTIFromContext(c *gin.Context) string {
	if v, exists := c.Get(contextKeyJTI); exists {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
