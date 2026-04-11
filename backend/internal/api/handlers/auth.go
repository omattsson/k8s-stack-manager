package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/cache"
	"backend/internal/config"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	refreshTokenCookieName = "refresh_token"
	refreshTokenLength     = 64 // bytes of randomness for the raw token
)

// bcryptSem limits concurrent bcrypt operations to the number of CPU cores.
// bcrypt is intentionally CPU-expensive (~200ms). Without a limit, 100 concurrent
// logins would spawn 100 goroutines all competing for CPU, starving other requests.
var bcryptSem = make(chan struct{}, runtime.NumCPU())

// AuthHandler handles authentication and user management endpoints.
type AuthHandler struct {
	userRepo         models.UserRepository
	refreshTokenRepo models.RefreshTokenRepository
	cfg              *config.AuthConfig
	blocklist        *middleware.TokenBlocklist
	loginCache       *cache.TTLCache[*models.User]
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(userRepo models.UserRepository, cfg *config.AuthConfig) *AuthHandler {
	h := &AuthHandler{userRepo: userRepo, cfg: cfg}
	if cfg.LoginCacheTTL > 0 {
		h.loginCache = cache.New[*models.User](cfg.LoginCacheTTL, cfg.LoginCacheTTL)
	}
	return h
}

// SetRefreshTokenRepo sets the refresh token repository for refresh token support.
func (h *AuthHandler) SetRefreshTokenRepo(repo models.RefreshTokenRepository) {
	h.refreshTokenRepo = repo
}

// SetTokenBlocklist sets the token blocklist for immediate token revocation on logout.
func (h *AuthHandler) SetTokenBlocklist(bl *middleware.TokenBlocklist) {
	h.blocklist = bl
}

// loginCacheKey derives a cache key from the username, stored password hash,
// and the submitted password. Including the submitted password ensures that
// only the correct password produces a cache hit.
func loginCacheKey(username, passwordHash, submittedPassword string) string {
	h := sha256.Sum256([]byte(username + ":" + passwordHash + ":" + submittedPassword))
	return hex.EncodeToString(h[:])
}

// LoginRequest represents the login request body.
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResponse represents the login response.
type LoginResponse struct {
	Token string      `json:"token"`
	User  models.User `json:"user"`
}

// RegisterRequest represents the register request body.
type RegisterRequest struct {
	Username    string `json:"username" binding:"required"`
	Password    string `json:"password" binding:"required"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// Login godoc
// @Summary     User login
// @Description Authenticate with username and password, returns a JWT access token and sets a refresh token cookie
// @Tags        auth
// @Accept      json
// @Produce     json
// @Param       credentials body     LoginRequest true "Login credentials"
// @Success     200         {object} LoginResponse
// @Failure     400         {object} map[string]string
// @Failure     401         {object} map[string]string
// @Router      /api/v1/auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	user, err := h.userRepo.FindByUsername(req.Username)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
		return
	}

	// Check login cache to skip expensive bcrypt comparison on repeated logins.
	cacheKey := loginCacheKey(req.Username, user.PasswordHash, req.Password)
	cacheHit := false
	if h.loginCache != nil {
		if cached, ok := h.loginCache.Get(cacheKey); ok {
			user = cached
			cacheHit = true
		}
	}

	if !cacheHit {
		// Limit concurrent bcrypt to NumCPU to prevent CPU starvation under spike.
		bcryptSem <- struct{}{}
		bcryptErr := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password))
		<-bcryptSem

		if bcryptErr != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
			return
		}
		if h.loginCache != nil {
			h.loginCache.Set(cacheKey, user)
		}
	}

	expiration := h.cfg.AccessTokenExpiration
	if h.refreshTokenRepo == nil {
		expiration = h.cfg.JWTExpiration
	}

	token, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
		UserID:     user.ID,
		Username:   user.Username,
		Role:       user.Role,
		Secret:     h.cfg.JWTSecret,
		Expiration: expiration,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Issue refresh token if repository is configured.
	if h.refreshTokenRepo != nil {
		if err := h.issueRefreshToken(c, user.ID); err != nil {
			slog.Error("Failed to issue refresh token", "user_id", user.ID, "error", err)
			// Continue without refresh token — access token still works.
		}
	}

	c.JSON(http.StatusOK, LoginResponse{Token: token, User: *user})
}

// Register godoc
// @Summary     Register a new user
// @Description Create a new user account (admin only, or when self-registration is enabled)
// @Tags        auth
// @Accept      json
// @Produce     json
// @Param       user body     RegisterRequest true "User registration data"
// @Success     201  {object} models.User
// @Failure     400  {object} map[string]string
// @Failure     403  {object} map[string]string
// @Failure     409  {object} map[string]string
// @Router      /api/v1/auth/register [post]
func (h *AuthHandler) Register(c *gin.Context) {
	// Check if registration is allowed.
	callerRole := middleware.GetRoleFromContext(c)
	if !h.cfg.SelfRegistration && callerRole != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Registration is disabled"})
		return
	}

	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username and password are required"})
		return
	}

	// Only admins can set a role other than "user".
	role := "user"
	if req.Role != "" && callerRole == "admin" {
		role = req.Role
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	user := &models.User{
		ID:           uuid.New().String(),
		Username:     req.Username,
		PasswordHash: string(hash),
		DisplayName:  req.DisplayName,
		Role:         role,
		AuthProvider: "local",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}

	if user.DisplayName == "" {
		user.DisplayName = user.Username
	}

	if err := h.userRepo.Create(user); err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusCreated, user)
}

// GetCurrentUser godoc
// @Summary     Get current user
// @Description Returns the authenticated user's information
// @Tags        auth
// @Produce     json
// @Success     200 {object} models.User
// @Failure     401 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /api/v1/auth/me [get]
func (h *AuthHandler) GetCurrentUser(c *gin.Context) {
	userID := middleware.GetUserIDFromContext(c)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}

	user, err := h.userRepo.FindByID(userID)
	if err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, user)
}

// EnsureAdminUser creates the initial admin user if configured and not already present.
func (h *AuthHandler) EnsureAdminUser() {
	if h.cfg.AdminUsername == "" || h.cfg.AdminPassword == "" {
		return
	}

	_, err := h.userRepo.FindByUsername(h.cfg.AdminUsername)
	if err == nil {
		return // Admin already exists.
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(h.cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("Failed to hash admin password", "error", err)
		return
	}

	admin := &models.User{
		ID:           uuid.New().String(),
		Username:     h.cfg.AdminUsername,
		PasswordHash: string(hash),
		DisplayName:  "Administrator",
		Role:         "admin",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}

	if err := h.userRepo.Create(admin); err != nil {
		slog.Error("Failed to create admin user", "error", err)
		return
	}

	slog.Info("Admin user created", "username", h.cfg.AdminUsername)
}

// RefreshResponse represents the response from the refresh endpoint.
type RefreshResponse struct {
	Token string `json:"token"`
}

// Refresh godoc
// @Summary     Refresh access token
// @Description Issues a new access token using the refresh token cookie. Rotates the refresh token (old one invalidated, new one issued).
// @Tags        auth
// @Produce     json
// @Success     200 {object} RefreshResponse
// @Failure     401 {object} map[string]string "Invalid, expired, or revoked refresh token"
// @Failure     500 {object} map[string]string
// @Failure     501 {object} map[string]string "Refresh tokens not enabled"
// @Router      /api/v1/auth/refresh [post]
func (h *AuthHandler) Refresh(c *gin.Context) {
	if h.refreshTokenRepo == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "Refresh tokens are not enabled"})
		return
	}

	rawToken, err := c.Cookie(refreshTokenCookieName)
	if err != nil || rawToken == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Refresh token required"})
		return
	}

	tokenHash := hashRefreshToken(rawToken)
	stored, err := h.refreshTokenRepo.FindByTokenHash(tokenHash)
	if err != nil {
		if isNotFoundError(err) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid refresh token"})
		} else {
			slog.Error("Failed to look up refresh token", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		}
		return
	}

	if stored.Revoked {
		// Possible token replay — revoke all tokens for the user as a precaution.
		slog.Warn("Revoked refresh token reuse detected", "user_id", stored.UserID, "token_id", stored.ID)
		_ = h.refreshTokenRepo.RevokeAllForUser(stored.UserID)
		h.clearRefreshCookie(c)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid refresh token"})
		return
	}

	now := time.Now().UTC()
	if now.After(stored.ExpiresAt) {
		h.clearRefreshCookie(c)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Refresh token expired"})
		return
	}

	// Check idle timeout.
	if now.Sub(stored.LastActivity) > h.cfg.SessionIdleTimeout {
		_ = h.refreshTokenRepo.RevokeByID(stored.ID)
		h.clearRefreshCookie(c)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Session idle timeout exceeded"})
		return
	}

	// Look up user to get current role/username.
	user, err := h.userRepo.FindByID(stored.UserID)
	if err != nil {
		slog.Error("Failed to find user for refresh", "user_id", stored.UserID, "error", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid refresh token"})
		return
	}

	// Issue replacement token BEFORE revoking the old one.
	// If this fails, the old token is still active and the client can retry.
	// Pass stored.ID so the max-limit cleanup doesn't revoke the token we're consuming.
	if err := h.issueRefreshToken(c, user.ID, stored.ID); err != nil {
		slog.Error("Failed to rotate refresh token", "user_id", user.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Revoke the consumed token. If another request already consumed it (replay),
	// revoke everything — including the token we just issued — for safety.
	affected, err := h.refreshTokenRepo.RevokeByIDIfActive(stored.ID)
	if err != nil || affected == 0 {
		slog.Warn("Concurrent refresh token consumption detected", "user_id", stored.UserID, "token_id", stored.ID)
		_ = h.refreshTokenRepo.RevokeAllForUser(stored.UserID)
		h.clearRefreshCookie(c)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid refresh token"})
		return
	}

	accessToken, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
		UserID:     user.ID,
		Username:   user.Username,
		Role:       user.Role,
		Secret:     h.cfg.JWTSecret,
		Expiration: h.cfg.AccessTokenExpiration,
	})
	if err != nil {
		slog.Error("Failed to generate access token during refresh", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	c.JSON(http.StatusOK, RefreshResponse{Token: accessToken})
}

// Logout godoc
// @Summary     Logout
// @Description Revokes the current refresh token and blocklists the access token
// @Tags        auth
// @Accept      json
// @Produce     json
// @Success     200 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Router      /api/v1/auth/logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	// Blocklist the current access token so it can't be reused.
	if h.blocklist != nil {
		jti := middleware.GetJTIFromContext(c)
		if jti != "" {
			expiry, ok := middleware.GetTokenExpiryFromContext(c)
			if !ok {
				expiry = time.Now().Add(h.cfg.AccessTokenExpiration)
			}
			h.blocklist.Add(jti, expiry)
		}
	}

	// Revoke the refresh token if present.
	if h.refreshTokenRepo != nil {
		if rawToken, err := c.Cookie(refreshTokenCookieName); err == nil && rawToken != "" {
			tokenHash := hashRefreshToken(rawToken)
			if stored, err := h.refreshTokenRepo.FindByTokenHash(tokenHash); err == nil {
				_ = h.refreshTokenRepo.RevokeByID(stored.ID)
			}
		}
	}

	h.clearRefreshCookie(c)
	c.JSON(http.StatusOK, gin.H{"message": "Logged out successfully"})
}

// LogoutAll godoc
// @Summary     Logout from all sessions
// @Description Revokes all refresh tokens for the current user
// @Tags        auth
// @Accept      json
// @Produce     json
// @Success     200 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/auth/logout-all [post]
func (h *AuthHandler) LogoutAll(c *gin.Context) {
	userID := middleware.GetUserIDFromContext(c)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}

	// Blocklist the current access token.
	if h.blocklist != nil {
		jti := middleware.GetJTIFromContext(c)
		if jti != "" {
			expiry, ok := middleware.GetTokenExpiryFromContext(c)
			if !ok {
				expiry = time.Now().Add(h.cfg.AccessTokenExpiration)
			}
			h.blocklist.Add(jti, expiry)
		}
	}

	if h.refreshTokenRepo != nil {
		if err := h.refreshTokenRepo.RevokeAllForUser(userID); err != nil {
			slog.Error("Failed to revoke all refresh tokens", "user_id", userID, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}
	}

	h.clearRefreshCookie(c)
	c.JSON(http.StatusOK, gin.H{"message": "Logged out from all sessions"})
}

// CleanupExpiredTokens deletes expired refresh tokens from the store.
// Intended to be called periodically (e.g., by a background goroutine).
func (h *AuthHandler) CleanupExpiredTokens() {
	if h.refreshTokenRepo == nil {
		return
	}
	deleted, err := h.refreshTokenRepo.DeleteExpired()
	if err != nil {
		slog.Error("Failed to clean up expired refresh tokens", "error", err)
		return
	}
	if deleted > 0 {
		slog.Info("Cleaned up expired refresh tokens", "count", deleted)
	}
}

// issueRefreshToken generates a new refresh token, stores it, and sets the cookie.
// excludeTokenID is an optional token ID to skip when enforcing the max token limit
// (used during rotation to avoid revoking the token currently being consumed).
func (h *AuthHandler) issueRefreshToken(c *gin.Context, userID string, excludeTokenID ...string) error {
	// Clean up excess tokens if over limit.
	activeCount, err := h.refreshTokenRepo.CountActiveForUser(userID)
	if err != nil {
		return err
	}
	if h.cfg.MaxRefreshTokensPerUser > 0 && int(activeCount) >= h.cfg.MaxRefreshTokensPerUser {
		// Revoke all and start fresh to stay within bounds,
		// but skip the token currently being consumed (if any)
		// so RevokeByIDIfActive can still detect replays.
		if len(excludeTokenID) > 0 && excludeTokenID[0] != "" {
			if err := h.refreshTokenRepo.RevokeAllForUserExcept(userID, excludeTokenID[0]); err != nil {
				return err
			}
		} else {
			if err := h.refreshTokenRepo.RevokeAllForUser(userID); err != nil {
				return err
			}
		}
	}

	rawToken, err := generateRefreshToken()
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	rt := &models.RefreshToken{
		ID:           uuid.New().String(),
		UserID:       userID,
		TokenHash:    hashRefreshToken(rawToken),
		ExpiresAt:    now.Add(h.cfg.RefreshTokenExpiration),
		LastActivity: now,
		CreatedAt:    now,
		UserAgent:    truncate(c.GetHeader("User-Agent"), 500),
		IPAddress:    c.ClientIP(),
	}

	if err := h.refreshTokenRepo.Create(rt); err != nil {
		return err
	}

	h.setRefreshCookie(c, rawToken)
	return nil
}

// generateRefreshToken produces a cryptographically random token string.
func generateRefreshToken() (string, error) {
	b := make([]byte, refreshTokenLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hashRefreshToken returns the SHA-256 hex digest of the raw token.
func hashRefreshToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

func (h *AuthHandler) setRefreshCookie(c *gin.Context, rawToken string) {
	maxAge := int(h.cfg.RefreshTokenExpiration.Seconds())
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(
		refreshTokenCookieName,
		rawToken,
		maxAge,
		"/api/v1/auth",
		"",
		h.cfg.SecureCookies,
		true, // httpOnly
	)
}

func (h *AuthHandler) clearRefreshCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(
		refreshTokenCookieName,
		"",
		-1,
		"/api/v1/auth",
		"",
		h.cfg.SecureCookies,
		true,
	)
}
