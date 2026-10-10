package handlers

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/cache"
	"backend/internal/config"
	"backend/internal/models"
	"backend/internal/sessionstore"
	"backend/internal/websocket"

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
	oidcCfg          *config.OIDCConfig
	sessionStore     sessionstore.SessionStore
	wsRevoker        websocket.ClientRevoker
	loginCache       *cache.TTLCache[struct{}] // key: verified credentials; value unused
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(userRepo models.UserRepository, cfg *config.AuthConfig, oidcCfg *config.OIDCConfig) *AuthHandler {
	h := &AuthHandler{userRepo: userRepo, cfg: cfg, oidcCfg: oidcCfg}
	if cfg.LoginCacheTTL > 0 {
		h.loginCache = cache.New[struct{}](cfg.LoginCacheTTL, cfg.LoginCacheTTL)
	}
	return h
}

// SetRefreshTokenRepo sets the refresh token repository for refresh token support.
func (h *AuthHandler) SetRefreshTokenRepo(repo models.RefreshTokenRepository) {
	h.refreshTokenRepo = repo
}

// SetSessionStore sets the session store for token blocklist and OIDC state persistence.
func (h *AuthHandler) SetSessionStore(store sessionstore.SessionStore) {
	h.sessionStore = store
}

// SetWebSocketRevoker sets the hub that closes open WebSocket connections on
// logout. nil: sockets close at access-token expiry only.
func (h *AuthHandler) SetWebSocketRevoker(r websocket.ClientRevoker) {
	h.wsRevoker = r
}

// loginCacheKey derives a cache key from the username, stored password hash,
// and the submitted password. Including the submitted password ensures that
// only the correct password produces a cache hit.
func loginCacheKey(username, passwordHash, submittedPassword string) string {
	mac := hmac.New(sha256.New, []byte(passwordHash))
	mac.Write([]byte(username + ":" + submittedPassword))
	return hex.EncodeToString(mac.Sum(nil))
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
	Username       string `json:"username" binding:"required"`
	Password       string `json:"password" binding:"required"`
	DisplayName    string `json:"display_name"`
	Role           string `json:"role"`
	ServiceAccount bool   `json:"service_account"`
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
		middleware.RecordLogin("local", "invalid")
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	user, err := h.userRepo.FindByUsername(req.Username)
	if err != nil {
		// An unknown username is an invalid credential; only other lookup
		// errors are operational failures.
		if isNotFoundError(err) {
			middleware.RecordLogin("local", "invalid")
		} else {
			slog.Error("Login user lookup failed", "error", err)
			middleware.RecordLogin("local", "failure")
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
		return
	}

	if user.Disabled {
		middleware.RecordLogin("local", "disabled")
		c.JSON(http.StatusForbidden, gin.H{"error": "Account disabled"})
		return
	}

	// When OIDC is enabled and local auth is not explicitly allowed, only service accounts can use local login.
	if h.oidcCfg != nil && h.oidcCfg.Enabled && !h.oidcCfg.LocalAuth && !user.ServiceAccount {
		middleware.RecordLogin("local", "restricted")
		c.JSON(http.StatusForbidden, gin.H{"error": "Local login is restricted to service accounts. Please use SSO."})
		return
	}

	// Check login cache to skip expensive bcrypt comparison on repeated logins.
	cacheKey := loginCacheKey(req.Username, user.PasswordHash, req.Password)
	cacheHit := false
	if h.loginCache != nil {
		// A hit only proves the password. Keep the fresh user record: the
		// token must carry the current role and display name (a role
		// change within the cache TTL must not give a token with the old
		// role).
		if _, ok := h.loginCache.Get(cacheKey); ok {
			cacheHit = true
		}
	}

	if !cacheHit {
		// Limit concurrent bcrypt to NumCPU to prevent CPU starvation under spike.
		bcryptSem <- struct{}{}
		bcryptErr := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password))
		<-bcryptSem

		if bcryptErr != nil {
			// A password mismatch is an invalid credential. Any other bcrypt
			// error (for example a malformed or unsupported stored hash) is a
			// server-side failure, not a bad client credential.
			if errors.Is(bcryptErr, bcrypt.ErrMismatchedHashAndPassword) {
				middleware.RecordLogin("local", "invalid")
			} else {
				slog.Error("bcrypt comparison failed", "error", bcryptErr)
				middleware.RecordLogin("local", "failure")
			}
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
			return
		}
		if h.loginCache != nil {
			h.loginCache.Set(cacheKey, struct{}{})
		}
	}

	// Re-read the user after the password check (bcrypt takes time, and a
	// cache hit can be up to LOGIN_CACHE_TTL old). The token and the checks
	// use this record, so a disable, role change or password reset that
	// commits during the login applies to it.
	fresh, err := h.userRepo.FindByID(user.ID)
	if err != nil {
		if isNotFoundError(err) {
			middleware.RecordLogin("local", "invalid")
		} else {
			slog.Error("Login user re-read failed", "error", err)
			middleware.RecordLogin("local", "failure")
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
		return
	}
	if fresh.PasswordHash != user.PasswordHash {
		// The password changed after the check: the verified password is the old one.
		middleware.RecordLogin("local", "invalid")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
		return
	}
	user = fresh
	if user.Disabled {
		middleware.RecordLogin("local", "disabled")
		c.JSON(http.StatusForbidden, gin.H{"error": "Account disabled"})
		return
	}
	if h.oidcCfg != nil && h.oidcCfg.Enabled && !h.oidcCfg.LocalAuth && !user.ServiceAccount {
		middleware.RecordLogin("local", "restricted")
		c.JSON(http.StatusForbidden, gin.H{"error": "Local login is restricted to service accounts. Please use SSO."})
		return
	}

	// With refresh tokens the login starts a session; the access token carries
	// its ID ("sid"). Without refresh tokens the token is long-lived and has
	// no session.
	var token string
	var sess refreshSession
	if h.refreshTokenRepo != nil {
		sess = newRefreshSession()
		token, err = h.sessionAccessToken(user, sess.familyID)
	} else {
		token, err = middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
			UserID:       user.ID,
			Username:     user.Username,
			DisplayName:  user.DisplayName,
			Role:         user.Role,
			Secret:       h.cfg.JWTSecret,
			Expiration:   h.cfg.JWTExpiration,
			AuthProvider: authProviderOf(user),
			Email:        user.Email,
		})
	}
	if err != nil {
		middleware.RecordLogin("local", "failure")
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Close the disable race: re-read the user after signing and before the
	// token or a refresh token leaves the server.
	if err := recheckUserActive(h.userRepo, user.ID); err != nil {
		switch {
		case errors.Is(err, errAccountDisabled):
			middleware.RecordLogin("local", "disabled")
			c.JSON(http.StatusForbidden, gin.H{"error": "Account disabled"})
		case errors.Is(err, errUserGone):
			middleware.RecordLogin("local", "invalid")
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
		default:
			slog.Error("Login user re-check failed", "user_id", user.ID, "error", err)
			middleware.RecordLogin("local", "failure")
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		}
		return
	}

	// Issue refresh token if repository is configured.
	if h.refreshTokenRepo != nil {
		rawRefresh, refreshExpiry, err := h.issueRefreshTokenWith(c, h.refreshTokenRepo, user.ID, sess, "")
		if err != nil {
			slog.Error("Failed to issue refresh token", "user_id", user.ID, "error", err)
			// Continue without refresh token — access token still works.
		} else {
			// Close the revoke race: a revoke-all between the issue and here
			// must not leave a working session.
			if err := recheckSessionActive(h.refreshTokenRepo, sess.familyID); err != nil {
				abandonSession(h.refreshTokenRepo, sess.familyID)
				h.clearRefreshCookie(c)
				if errors.Is(err, errSessionRevoked) {
					middleware.RecordLogin("local", "revoked")
					c.JSON(http.StatusUnauthorized, gin.H{"error": "Session revoked"})
				} else {
					slog.Error("Login session re-check failed", "user_id", user.ID, "family_id", sess.familyID, "error", err)
					middleware.RecordLogin("local", "failure")
					c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
				}
				return
			}
			h.setRefreshCookie(c, rawRefresh, refreshExpiry)
		}
	}

	middleware.RecordLogin("local", "success")
	c.JSON(http.StatusOK, LoginResponse{Token: token, User: *user})
}

// Register godoc
// @Summary     Register a new user
// @Description Create a new user account (admin only, or when self-registration is enabled) A role outside user, devops and admin gives 400.
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

	// Reject an unknown role for every caller (same rule as the role change).
	if req.Role != "" && !models.IsValidRole(req.Role) {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRole})
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

	// Only admins can create service accounts.
	serviceAccount := req.ServiceAccount && callerRole == "admin"

	user := &models.User{
		ID:             uuid.New().String(),
		Username:       req.Username,
		PasswordHash:   string(hash),
		DisplayName:    req.DisplayName,
		Role:           role,
		AuthProvider:   "local",
		ServiceAccount: serviceAccount,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
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
		ID:             uuid.New().String(),
		Username:       h.cfg.AdminUsername,
		PasswordHash:   string(hash),
		DisplayName:    "Administrator",
		Role:           "admin",
		ServiceAccount: true,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
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

// errRefreshTokenConsumed aborts the rotation transaction when another request
// consumed or revoked the presented refresh token first.
var errRefreshTokenConsumed = errors.New("refresh token already consumed")

// Refresh godoc
// @Summary     Refresh access token
// @Description Issues a new access token using the refresh token cookie. Rotates the refresh token (old one invalidated, new one issued). A session ends SESSION_MAX_LIFETIME after login and after SESSION_IDLE_TIMEOUT without requests; rotation never extends the session. A token rotated less than REFRESH_REUSE_GRACE ago (for example a concurrent refresh from a second browser tab) gets a new access token without a new cookie. Any other reuse of a consumed token revokes the session.
// @Tags        auth
// @Accept      json
// @Produce     json
// @Success     200 {object} RefreshResponse
// @Failure     401 {object} map[string]string "Invalid, expired, or revoked refresh token or session"
// @Failure     403 {object} map[string]string "Account disabled"
// @Failure     500 {object} map[string]string
// @Failure     501 {object} map[string]string "Refresh tokens not enabled"
// @Router      /api/v1/auth/refresh [post]
func (h *AuthHandler) Refresh(c *gin.Context) {
	if h.refreshTokenRepo == nil {
		middleware.RecordRefresh("disabled")
		c.JSON(http.StatusNotImplemented, gin.H{"error": "Refresh tokens are not enabled"})
		return
	}

	rawToken, err := c.Cookie(refreshTokenCookieName)
	if err != nil || rawToken == "" {
		middleware.RecordRefresh("missing")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Refresh token required"})
		return
	}

	tokenHash := hashRefreshToken(rawToken)
	stored, ok := h.findRefreshToken(c, tokenHash)
	if !ok {
		return
	}

	now := time.Now().UTC()
	if stored.Revoked {
		h.refreshConsumedToken(c, stored, now)
		return
	}

	if now.After(stored.ExpiresAt) {
		h.clearRefreshCookie(c)
		middleware.RecordRefresh("expired")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Refresh token expired"})
		return
	}

	// Absolute session lifetime: the user must log in again (an SSO login
	// syncs the role from the identity provider).
	if h.sessionExpired(stored, now) {
		_ = h.refreshTokenRepo.RevokeFamily(stored.SessionFamily())
		h.clearRefreshCookie(c)
		middleware.RecordRefresh("expired")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Session expired"})
		return
	}

	// Check idle timeout. Authenticated requests move LastActivity forward
	// (session activity hook in the JWT middleware), so this is the time
	// since the last request, not since the last refresh.
	if now.Sub(stored.LastActivity) > h.cfg.SessionIdleTimeout {
		_ = h.refreshTokenRepo.RevokeByID(stored.ID)
		h.clearRefreshCookie(c)
		middleware.RecordRefresh("expired")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Session idle timeout exceeded"})
		return
	}

	// Look up user to get current role/username.
	user, err := h.userRepo.FindByID(stored.UserID)
	if err != nil {
		// A deleted or missing user is an expected invalid-session condition,
		// not an operational failure — only real repository errors are.
		if isNotFoundError(err) {
			middleware.RecordRefresh("revoked")
		} else {
			slog.Error("Failed to find user for refresh", "user_id", stored.UserID, "error", err)
			middleware.RecordRefresh("failure")
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid refresh token"})
		return
	}

	if user.Disabled {
		_ = h.refreshTokenRepo.RevokeAllForUser(stored.UserID)
		h.clearRefreshCookie(c)
		middleware.RecordRefresh("disabled")
		c.JSON(http.StatusForbidden, gin.H{"error": "Account disabled"})
		return
	}

	// Rotate atomically: consume the old token, then create its successor in
	// the same session family. Consuming first takes the row lock, so two
	// concurrent refreshes with the same token serialise and the second one
	// sees 0 changed rows.
	sess := continuedSession(stored)
	var newRawToken string
	var newExpiresAt time.Time
	err = h.refreshTokenRepo.WithTx(func(txRepo models.RefreshTokenRepository) error {
		affected, err := txRepo.MarkRotatedIfActive(stored.ID, now)
		if err != nil {
			return err
		}
		if affected == 0 {
			return errRefreshTokenConsumed
		}
		var issueErr error
		newRawToken, newExpiresAt, issueErr = h.issueRefreshTokenWith(c, txRepo, user.ID, sess, stored.ID)
		return issueErr
	})
	if errors.Is(err, errRefreshTokenConsumed) {
		// Another request consumed the token after our read. Re-read it so a
		// concurrent rotation gets the grace path and a revocation does not.
		latest, ok := h.findRefreshToken(c, tokenHash)
		if !ok {
			return
		}
		h.refreshConsumedToken(c, latest, now)
		return
	}
	if err != nil {
		slog.Error("Failed to rotate refresh token", "user_id", user.ID, "error", err)
		middleware.RecordRefresh("failure")
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	accessToken, err := h.sessionAccessToken(user, sess.familyID)
	if err != nil {
		slog.Error("Failed to generate access token during refresh", "error", err)
		middleware.RecordRefresh("failure")
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	// Close the disable race (same as login): re-read the user after signing.
	// On rejection revoke the rotated refresh token too.
	if err := recheckUserActive(h.userRepo, user.ID); err != nil {
		h.rejectRefreshAfterRecheck(c, user.ID, err)
		return
	}
	// Close the revoke race: a logout-all or admin revoke that ran after the
	// rotation committed has revoked the new token; do not hand out a token.
	if err := recheckSessionActive(h.refreshTokenRepo, sess.familyID); err != nil {
		h.rejectRefreshAfterSessionCheck(c, user.ID, sess.familyID, true, err)
		return
	}

	// Set cookie only after the transaction committed and the re-checks passed.
	h.setRefreshCookie(c, newRawToken, newExpiresAt)

	// Record success only after the access token was generated: the refresh is
	// not complete until this point.
	middleware.RecordRefresh("success")
	c.JSON(http.StatusOK, RefreshResponse{Token: accessToken})
}

// findRefreshToken looks up a refresh token by hash. On failure it writes the
// response (401 for an unknown token, 500 for a repository error) and returns
// false.
func (h *AuthHandler) findRefreshToken(c *gin.Context, tokenHash string) (*models.RefreshToken, bool) {
	stored, err := h.refreshTokenRepo.FindByTokenHash(tokenHash)
	if err == nil {
		return stored, true
	}
	if isNotFoundError(err) {
		h.clearRefreshCookie(c)
		middleware.RecordRefresh("revoked")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid refresh token"})
	} else {
		slog.Error("Failed to look up refresh token", "error", err)
		middleware.RecordRefresh("failure")
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
	}
	return nil, false
}

// refreshConsumedToken handles a refresh with a token that is already revoked.
//
// Grace: a token that a normal rotation consumed less than RefreshReuseGrace
// ago gets a new access token for the same session, when the session is still
// active (two tabs refresh with the same cookie at the same time). The
// response sets no cookie: the first response already set the successor, and
// nothing is revoked.
//
// Replay: any other reuse (after the grace window, or of a token revoked by
// logout, revoke-all or an admin action, which never has RotatedAt) revokes
// the session family. Only the family is revoked, not all sessions of the
// user: the replayed token can only belong to this family, and revoking the
// other devices of the user adds no protection (refresh token rotation family
// revocation, OAuth 2.0 Security BCP).
func (h *AuthHandler) refreshConsumedToken(c *gin.Context, stored *models.RefreshToken, now time.Time) {
	family := stored.SessionFamily()
	if h.withinReuseGrace(stored, now) {
		user, err := h.userRepo.FindByID(stored.UserID)
		if err != nil && !isNotFoundError(err) {
			slog.Error("Failed to find user for refresh", "user_id", stored.UserID, "error", err)
			middleware.RecordRefresh("failure")
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}
		if err == nil && !user.Disabled {
			active, countErr := h.refreshTokenRepo.CountActiveInFamily(family)
			if countErr != nil {
				slog.Error("Failed to check refresh token family", "user_id", stored.UserID, "error", countErr)
				middleware.RecordRefresh("failure")
				c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
				return
			}
			if active > 0 {
				h.respondGraceRefresh(c, user, family)
				return
			}
		}
	}

	slog.Warn("Refresh token reuse detected; revoking session", "user_id", stored.UserID, "token_id", stored.ID)
	if err := h.refreshTokenRepo.RevokeFamily(family); err != nil {
		slog.Error("Failed to revoke refresh token family", "user_id", stored.UserID, "error", err)
	}
	h.clearRefreshCookie(c)
	middleware.RecordRefresh("revoked")
	c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid refresh token"})
}

// withinReuseGrace reports whether a consumed token may still get an access
// token: rotated (not revoked) less than RefreshReuseGrace ago, in a session
// that has not reached its absolute lifetime.
func (h *AuthHandler) withinReuseGrace(stored *models.RefreshToken, now time.Time) bool {
	if h.cfg.RefreshReuseGrace <= 0 || stored.RotatedAt == nil {
		return false
	}
	if now.Sub(*stored.RotatedAt) > h.cfg.RefreshReuseGrace {
		return false
	}
	return !h.sessionExpired(stored, now)
}

// respondGraceRefresh answers a refresh in the reuse grace window with a new
// access token only. It does not touch the refresh cookie.
func (h *AuthHandler) respondGraceRefresh(c *gin.Context, user *models.User, sessionID string) {
	accessToken, err := h.sessionAccessToken(user, sessionID)
	if err != nil {
		slog.Error("Failed to generate access token during refresh", "error", err)
		middleware.RecordRefresh("failure")
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	if err := recheckUserActive(h.userRepo, user.ID); err != nil {
		h.rejectRefreshAfterRecheck(c, user.ID, err)
		return
	}
	if err := recheckSessionActive(h.refreshTokenRepo, sessionID); err != nil {
		h.rejectRefreshAfterSessionCheck(c, user.ID, sessionID, false, err)
		return
	}
	middleware.RecordRefresh("grace")
	c.JSON(http.StatusOK, RefreshResponse{Token: accessToken})
}

// errSessionRevoked means the session family has no active refresh token any
// more (logout-all, admin revoke or replay revocation won a race).
var errSessionRevoked = errors.New("session_revoked")

// recheckSessionActive runs after an access token is signed and confirms that
// the session family still has an active refresh token. Callers fail closed on
// every non-nil result.
func recheckSessionActive(repo models.RefreshTokenRepository, familyID string) error {
	active, err := repo.CountActiveInFamily(familyID)
	if err != nil {
		return err
	}
	if active == 0 {
		return errSessionRevoked
	}
	return nil
}

// abandonSession revokes a session family that a login created but did not
// hand out (best effort; the error is only logged).
func abandonSession(repo models.RefreshTokenRepository, familyID string) {
	if err := repo.RevokeFamily(familyID); err != nil {
		slog.Error("Failed to revoke abandoned session", "family_id", familyID, "error", err)
	}
}

// rejectRefreshAfterSessionCheck ends a refresh whose session re-check failed.
// It clears the cookie and revokes nothing more: the family is already revoked,
// and a repository error is no reason to end other sessions. rotated tells
// whether the rotation transaction already committed (the client then never
// receives the new refresh token).
func (h *AuthHandler) rejectRefreshAfterSessionCheck(c *gin.Context, userID, familyID string, rotated bool, err error) {
	h.clearRefreshCookie(c)
	if errors.Is(err, errSessionRevoked) {
		middleware.RecordRefresh("revoked")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Session revoked"})
		return
	}
	if rotated {
		slog.Error("Refresh session re-check failed: rotation committed but response failed",
			"user_id", userID, "family_id", familyID, "error", err)
	} else {
		slog.Error("Refresh session re-check failed", "user_id", userID, "family_id", familyID, "error", err)
	}
	middleware.RecordRefresh("failure")
	c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
}

// rejectRefreshAfterRecheck ends a refresh whose user re-check failed: it
// revokes all refresh tokens of the user and clears the cookie.
func (h *AuthHandler) rejectRefreshAfterRecheck(c *gin.Context, userID string, err error) {
	_ = h.refreshTokenRepo.RevokeAllForUser(userID)
	h.clearRefreshCookie(c)
	switch {
	case errors.Is(err, errAccountDisabled):
		middleware.RecordRefresh("disabled")
		c.JSON(http.StatusForbidden, gin.H{"error": "Account disabled"})
	case errors.Is(err, errUserGone):
		middleware.RecordRefresh("revoked")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid refresh token"})
	default:
		slog.Error("Refresh user re-check failed", "user_id", userID, "error", err)
		middleware.RecordRefresh("failure")
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
	}
}

// sessionExpired reports whether the session of the token is older than
// SessionMaxLifetime.
func (h *AuthHandler) sessionExpired(rt *models.RefreshToken, now time.Time) bool {
	deadline := sessionDeadline(h.cfg, rt.SessionStart())
	return !deadline.IsZero() && now.After(deadline)
}

// sessionAccessToken signs an access token for a refresh session. It carries
// the provider and email of the user record (the same claims as at login) and
// the session ID ("sid") that the JWT middleware uses for activity tracking.
func (h *AuthHandler) sessionAccessToken(user *models.User, sessionID string) (string, error) {
	return middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
		UserID:       user.ID,
		Username:     user.Username,
		DisplayName:  user.DisplayName,
		Role:         user.Role,
		Secret:       h.cfg.JWTSecret,
		Expiration:   h.cfg.AccessTokenExpiration,
		AuthProvider: authProviderOf(user),
		Email:        user.Email,
		SessionID:    sessionID,
	})
}

// authProviderOf returns the auth provider of the user record; "local" when
// the record has none.
func authProviderOf(user *models.User) string {
	if user.AuthProvider == "" {
		return "local"
	}
	return user.AuthProvider
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
	// Best-effort blocklist of the access token and close of the sockets
	// opened with it. The route is public (no auth middleware) so we parse
	// the Authorization header ourselves.
	if h.sessionStore != nil || h.wsRevoker != nil {
		if authHeader := c.GetHeader("Authorization"); authHeader != "" {
			if parts := strings.SplitN(authHeader, " ", 2); len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				if claims, err := middleware.ValidateJWT(parts[1], h.cfg.JWTSecret); err == nil && claims.ID != "" {
					if h.sessionStore != nil {
						expiry := time.Now().Add(h.cfg.AccessTokenExpiration)
						if claims.ExpiresAt != nil {
							expiry = claims.ExpiresAt.Time
						}
						if blockErr := h.sessionStore.BlockToken(c.Request.Context(), claims.ID, expiry); blockErr != nil {
							slog.Error("Failed to blocklist token on logout", "jti", claims.ID, "error", blockErr)
						}
					}
					if h.wsRevoker != nil {
						h.wsRevoker.DisconnectToken(claims.ID)
					}
				}
			}
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
	if h.sessionStore != nil {
		jti := middleware.GetJTIFromContext(c)
		if jti != "" {
			expiry, ok := middleware.GetTokenExpiryFromContext(c)
			if !ok {
				expiry = time.Now().Add(h.cfg.AccessTokenExpiration)
			}
			if blockErr := h.sessionStore.BlockToken(c.Request.Context(), jti, expiry); blockErr != nil {
				slog.Error("Failed to blocklist token on logout-all", "jti", jti, "error", blockErr)
			}
		}
	}

	// Close the open sockets of all sessions of the user on this replica
	// (with WebSocket fan-out, also on the other replicas).
	// The refresh tokens are revoked next, so a reconnect works only until
	// the access token of a session expires.
	if h.wsRevoker != nil {
		h.wsRevoker.DisconnectUser(userID)
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

// refreshSession identifies the login session (refresh-token family) that a
// new refresh token belongs to.
type refreshSession struct {
	startedAt time.Time // login time; the absolute lifetime counts from here
	// lastActivity is the LastActivity of the rotated token. The new token
	// keeps it, so a refresh does not count as activity for the idle limit:
	// idle time counts from the last authenticated request (TouchFamily), not
	// from the last refresh. Zero at login (and for legacy rows): the new
	// token uses now.
	lastActivity time.Time
	familyID     string // ID of the first token of the session; the "sid" claim
	isNew        bool   // true at login: the first token uses familyID as its ID
}

// newRefreshSession starts a new session at login.
func newRefreshSession() refreshSession {
	return refreshSession{familyID: uuid.New().String(), startedAt: time.Now().UTC(), isNew: true}
}

// continuedSession keeps the family, the start time and the last activity of
// a rotated token.
func continuedSession(rt *models.RefreshToken) refreshSession {
	return refreshSession{familyID: rt.SessionFamily(), startedAt: rt.SessionStart(), lastActivity: rt.LastActivity}
}

// sessionDeadline returns the end of a session that started at start, or the
// zero time when SessionMaxLifetime is not set.
func sessionDeadline(cfg *config.AuthConfig, start time.Time) time.Time {
	if cfg.SessionMaxLifetime <= 0 {
		return time.Time{}
	}
	return start.Add(cfg.SessionMaxLifetime)
}

// issueRefreshTokenWith creates a refresh token of the session in repo (a
// transactional repo during rotation) without setting the cookie.
// Returns the raw token and its expiry so the caller can set the cookie after a
// transaction commits. excludeTokenID is the token being consumed (rotation)
// and is skipped when the max token limit is enforced; pass "" at login.
func (h *AuthHandler) issueRefreshTokenWith(c *gin.Context, repo models.RefreshTokenRepository, userID string, sess refreshSession, excludeTokenID string) (string, time.Time, error) {
	return createRefreshToken(c, repo, h.cfg, userID, sess, excludeTokenID)
}

// createRefreshToken stores a new refresh token of the session and returns the
// raw token and its expiry. The expiry is the earlier of now +
// RefreshTokenExpiration and the session deadline, so rotation never extends
// a session. Shared by local login, refresh and OIDC login.
func createRefreshToken(c *gin.Context, repo models.RefreshTokenRepository, cfg *config.AuthConfig, userID string, sess refreshSession, excludeTokenID string) (string, time.Time, error) {
	// Clean up excess tokens if over limit.
	if cfg.MaxRefreshTokensPerUser > 0 {
		activeCount, err := repo.CountActiveForUser(userID)
		if err != nil {
			return "", time.Time{}, err
		}
		if int(activeCount) >= cfg.MaxRefreshTokensPerUser {
			// Revoke all and start fresh to stay within bounds,
			// but skip the token currently being consumed (if any).
			if excludeTokenID != "" {
				if err := repo.RevokeAllForUserExcept(userID, excludeTokenID); err != nil {
					return "", time.Time{}, err
				}
			} else {
				if err := repo.RevokeAllForUser(userID); err != nil {
					return "", time.Time{}, err
				}
			}
		}
	}

	rawToken, err := generateRefreshToken()
	if err != nil {
		return "", time.Time{}, err
	}

	now := time.Now().UTC()
	id := uuid.New().String()
	if sess.isNew && sess.familyID != "" {
		id = sess.familyID
	}
	familyID := sess.familyID
	if familyID == "" {
		familyID = id
	}
	startedAt := sess.startedAt
	if startedAt.IsZero() {
		startedAt = now
	}
	lastActivity := sess.lastActivity
	if lastActivity.IsZero() {
		lastActivity = now
	}
	expiresAt := now.Add(cfg.RefreshTokenExpiration)
	if deadline := sessionDeadline(cfg, startedAt); !deadline.IsZero() && deadline.Before(expiresAt) {
		expiresAt = deadline
	}

	rt := &models.RefreshToken{
		ID:               id,
		UserID:           userID,
		FamilyID:         familyID,
		TokenHash:        hashRefreshToken(rawToken),
		ExpiresAt:        expiresAt,
		LastActivity:     lastActivity,
		CreatedAt:        now,
		SessionStartedAt: startedAt,
		UserAgent:        truncate(c.GetHeader("User-Agent"), 500),
		IPAddress:        c.ClientIP(),
	}

	if err := repo.Create(rt); err != nil {
		return "", time.Time{}, err
	}

	return rawToken, expiresAt, nil
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

func (h *AuthHandler) setRefreshCookie(c *gin.Context, rawToken string, expiresAt time.Time) {
	writeRefreshCookie(c, h.cfg, rawToken, expiresAt)
}

// writeRefreshCookie sets the refresh token cookie. Max-Age follows the token
// expiry, so the cookie never outlives the session.
func writeRefreshCookie(c *gin.Context, cfg *config.AuthConfig, rawToken string, expiresAt time.Time) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1 // 0 would mean a browser-session cookie, negative deletes it
	}
	c.SetSameSite(cfg.HTTPSameSite())
	c.SetCookie(
		refreshTokenCookieName,
		rawToken,
		maxAge,
		"/api/v1/auth",
		"",
		cfg.SecureCookies,
		true, // httpOnly
	)
}

func (h *AuthHandler) clearRefreshCookie(c *gin.Context) {
	clearRefreshCookieWith(c, h.cfg)
}

// clearRefreshCookieWith deletes the refresh token cookie (shared with OIDC).
func clearRefreshCookieWith(c *gin.Context, cfg *config.AuthConfig) {
	c.SetSameSite(cfg.HTTPSameSite())
	c.SetCookie(
		refreshTokenCookieName,
		"",
		-1,
		"/api/v1/auth",
		"",
		cfg.SecureCookies,
		true,
	)
}
