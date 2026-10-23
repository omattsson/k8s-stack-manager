package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"sync"
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

// passwordHashCost is the bcrypt cost of every stored password hash and of
// the dummy hash of the login.
const passwordHashCost = bcrypt.DefaultCost

var (
	dummyHashOnce sync.Once
	dummyHash     []byte
)

// dummyPasswordHash returns the hash that the login compares against when the
// user has no usable hash, so the answer takes the same time (#632).
func dummyPasswordHash() []byte {
	dummyHashOnce.Do(func() {
		hash, err := bcrypt.GenerateFromPassword([]byte(rand.Text()), passwordHashCost)
		if err != nil {
			panic("auth: dummy password hash: " + err.Error())
		}
		dummyHash = hash
	})
	return dummyHash
}

// AuthHandler handles authentication and user management endpoints.
type AuthHandler struct {
	userRepo         models.UserRepository
	refreshTokenRepo models.RefreshTokenRepository
	cfg              *config.AuthConfig
	oidcCfg          *config.OIDCConfig
	sessionStore     sessionstore.SessionStore
	wsRevoker        websocket.ClientRevoker
	loginCache       *cache.TTLCache[struct{}]                   // key: verified credentials; value unused
	logger           *slog.Logger                                // login failure log; nil uses slog.Default() (tests set it)
	compareHash      func(hashedPassword, password []byte) error // bcrypt.CompareHashAndPassword; tests wrap it
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(userRepo models.UserRepository, cfg *config.AuthConfig, oidcCfg *config.OIDCConfig) *AuthHandler {
	h := &AuthHandler{userRepo: userRepo, cfg: cfg, oidcCfg: oidcCfg, compareHash: bcrypt.CompareHashAndPassword}
	if cfg.LoginCacheTTL > 0 {
		h.loginCache = cache.New[struct{}](cfg.LoginCacheTTL, cfg.LoginCacheTTL)
	}
	// Create the dummy hash now: the first login of an unknown user must not take longer.
	dummyPasswordHash()
	return h
}

// comparePassword runs one bcrypt compare. bcryptSem limits the concurrent compares.
func (h *AuthHandler) comparePassword(hash []byte, password string) error {
	bcryptSem <- struct{}{}
	defer func() { <-bcryptSem }()
	return h.compareHash(hash, []byte(password))
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
// @Description Authenticate with username and password, returns a JWT access token and sets a refresh token cookie. Without the correct password the answer is always 401 "Invalid username or password". A disabled account (403) or the SSO-only restriction (403) is shown only after a correct password.
// @Tags        auth
// @Accept      json
// @Produce     json
// @Param       credentials body     LoginRequest true "Login credentials"
// @Success     200         {object} LoginResponse
// @Failure     400         {object} map[string]string
// @Failure     401         {object} map[string]string
// @Failure     403         {object} map[string]string
// @Failure     429         {object} map[string]string
// @Failure     500         {object} map[string]string
// @Router      /api/v1/auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.RecordLogin("local", "invalid")
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	// fail logs the failure with its reason, counts it and writes the response.
	// The response never shows the reason: a credential failure always gives
	// the same 401, so usernames cannot be enumerated.
	cacheHit := false
	fail := func(f loginFailure, body string) {
		f.username = req.Username
		f.cacheHit = cacheHit
		f.clientIP = c.ClientIP()
		f.requestID = c.GetString(middleware.RequestIDKey)
		h.logLoginFailure(f)
		c.JSON(f.httpCode, gin.H{"error": body})
	}
	const msgInvalidCredentials = "Invalid username or password"
	const msgAccountDisabled = "Account disabled"
	const msgLocalLoginRestricted = "Local login is restricted to service accounts. Please use SSO."

	// The lookups use the request context: a client that left gets 499 and
	// no "Login failed" line (#656).
	ctx := c.Request.Context()

	// Without the correct password, every answer is the same 401 after one bcrypt
	// compare, so it does not show whether the user exists or its state (#632).
	user, err := h.userRepo.FindByUsername(ctx, req.Username)
	if err != nil {
		if respondCancelled(c, err) {
			return
		}
		// A lookup error other than "not found" is a server-side failure
		// (500): a 401 would tell the client that the password is wrong.
		if !isNotFoundError(err) {
			fail(loginFailure{reason: loginReasonLookupError, status: "failure", httpCode: http.StatusInternalServerError, err: err}, msgInternalServerError)
			return
		}
		_ = h.comparePassword(dummyPasswordHash(), req.Password)
		fail(loginFailure{reason: loginReasonUserNotFound, status: "invalid", httpCode: http.StatusUnauthorized}, msgInvalidCredentials)
		return
	}

	// Check login cache to skip expensive bcrypt comparison on repeated logins.
	cacheKey := loginCacheKey(req.Username, user.PasswordHash, req.Password)
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
		// bcrypt rejects an empty or malformed hash without the key
		// derivation, so spend the time on the dummy hash.
		if user.PasswordHash == "" {
			_ = h.comparePassword(dummyPasswordHash(), req.Password)
			fail(loginFailure{reason: loginReasonNoPassword, status: "invalid", httpCode: http.StatusUnauthorized, userID: user.ID}, msgInvalidCredentials)
			return
		}
		if _, costErr := bcrypt.Cost([]byte(user.PasswordHash)); costErr != nil {
			_ = h.comparePassword(dummyPasswordHash(), req.Password)
			fail(loginFailure{reason: loginReasonHashInvalid, status: "failure", httpCode: http.StatusUnauthorized, userID: user.ID, err: costErr}, msgInvalidCredentials)
			return
		}
		bcryptErr := h.comparePassword([]byte(user.PasswordHash), req.Password)

		if bcryptErr != nil {
			// A password mismatch is an invalid credential. Any other bcrypt
			// error is counted as a failure, but the answer stays the generic
			// 401: a different answer would reveal the user.
			if errors.Is(bcryptErr, bcrypt.ErrMismatchedHashAndPassword) {
				fail(loginFailure{reason: loginReasonPasswordMismatch, status: "invalid", httpCode: http.StatusUnauthorized, userID: user.ID}, msgInvalidCredentials)
			} else {
				fail(loginFailure{reason: loginReasonHashInvalid, status: "failure", httpCode: http.StatusUnauthorized, userID: user.ID, err: bcryptErr}, msgInvalidCredentials)
			}
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
	fresh, err := h.userRepo.FindByID(ctx, user.ID)
	if err != nil {
		if respondCancelled(c, err) {
			return
		}
		if isNotFoundError(err) {
			fail(loginFailure{reason: loginReasonRereadNotFound, status: "invalid", httpCode: http.StatusUnauthorized, userID: user.ID}, msgInvalidCredentials)
		} else {
			fail(loginFailure{reason: loginReasonRereadError, status: "failure", httpCode: http.StatusInternalServerError, userID: user.ID, err: err}, msgInternalServerError)
		}
		return
	}
	if fresh.PasswordHash != user.PasswordHash {
		// The password changed after the check: the verified password is the old one.
		fail(loginFailure{reason: loginReasonHashChanged, status: "invalid", httpCode: http.StatusUnauthorized, userID: user.ID}, msgInvalidCredentials)
		return
	}
	user = fresh
	if user.Disabled {
		fail(loginFailure{reason: loginReasonDisabled, status: "disabled", httpCode: http.StatusForbidden, userID: user.ID}, msgAccountDisabled)
		return
	}
	// When OIDC is enabled and local auth is not explicitly allowed, only service accounts can use local login.
	if h.oidcCfg != nil && h.oidcCfg.Enabled && !h.oidcCfg.LocalAuth && !user.ServiceAccount {
		fail(loginFailure{reason: loginReasonLocalLoginNotAllowed, status: "restricted", httpCode: http.StatusForbidden, userID: user.ID}, msgLocalLoginRestricted)
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
		fail(loginFailure{reason: loginReasonTokenError, status: "failure", httpCode: http.StatusInternalServerError, userID: user.ID, err: err}, msgInternalServerError)
		return
	}

	// Close the disable race: re-read the user after signing and before the
	// token or a refresh token leaves the server.
	if err := recheckUserActive(ctx, h.userRepo, user.ID); err != nil {
		if respondCancelled(c, err) {
			return
		}
		switch {
		case errors.Is(err, errAccountDisabled):
			fail(loginFailure{reason: loginReasonDisabled, status: "disabled", httpCode: http.StatusForbidden, userID: user.ID}, msgAccountDisabled)
		case errors.Is(err, errUserGone):
			fail(loginFailure{reason: loginReasonRecheckFailed, status: "invalid", httpCode: http.StatusUnauthorized, userID: user.ID}, msgInvalidCredentials)
		default:
			fail(loginFailure{reason: loginReasonRecheckError, status: "failure", httpCode: http.StatusInternalServerError, userID: user.ID, err: err}, msgInternalServerError)
		}
		return
	}

	// Issue refresh token if repository is configured.
	if h.refreshTokenRepo != nil {
		// Detached from the first write on: a client that leaves must not skip
		// the token cap, the session re-check or the abandon revoke (#656).
		writeCtx := context.WithoutCancel(ctx)
		rawRefresh, refreshExpiry, err := h.issueRefreshTokenWith(writeCtx, c, h.refreshTokenRepo, user.ID, sess, "")
		if err != nil {
			slog.Error("Failed to issue refresh token", "user_id", user.ID, "error", err)
			// Continue without refresh token — access token still works.
		} else {
			// Close the revoke race: a revoke-all between the issue and here
			// must not leave a working session.
			if err := recheckSessionActive(writeCtx, h.refreshTokenRepo, sess.familyID); err != nil {
				abandonSession(writeCtx, h.refreshTokenRepo, sess.familyID)
				h.clearRefreshCookie(c)
				if errors.Is(err, errSessionRevoked) {
					fail(loginFailure{reason: loginReasonSessionRevoked, status: "revoked", httpCode: http.StatusUnauthorized, userID: user.ID}, "Session revoked")
				} else {
					fail(loginFailure{reason: loginReasonSessionError, status: "failure", httpCode: http.StatusInternalServerError, userID: user.ID, err: err}, msgInternalServerError)
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

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), passwordHashCost)
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

	// Detached: a client cancel must not commit the user and then skip the
	// audit entry (#656).
	if err := h.userRepo.Create(context.WithoutCancel(c.Request.Context()), user); err != nil {
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

	user, err := h.userRepo.FindByID(c.Request.Context(), userID)
	if err != nil {
		status, message := mapError(err, "User")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, user)
}

// EnsureAdminUser creates the initial admin user if configured and not already present.
func (h *AuthHandler) EnsureAdminUser(ctx context.Context) {
	if h.cfg.AdminUsername == "" || h.cfg.AdminPassword == "" {
		return
	}

	_, err := h.userRepo.FindByUsername(ctx, h.cfg.AdminUsername)
	if err == nil {
		return // Admin already exists.
	}
	// Only a missing user is created: a failed lookup (for example a
	// timeout) must not try a second admin row.
	if !isNotFoundError(err) {
		slog.Error("Failed to look up the admin user; admin user not created", "error", err)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(h.cfg.AdminPassword), passwordHashCost)
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

	if err := h.userRepo.Create(ctx, admin); err != nil {
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

	// The reads that decide the request use the request context: a client
	// that leaves gets 499 and nothing changes. Every write uses writeCtx, so
	// a client that leaves cannot skip a revoke or half-finish a rotation
	// (#656).
	reqCtx := c.Request.Context()
	writeCtx := context.WithoutCancel(reqCtx)

	tokenHash := hashRefreshToken(rawToken)
	stored, ok := h.findRefreshToken(reqCtx, c, tokenHash)
	if !ok {
		return
	}

	now := time.Now().UTC()
	if stored.Revoked {
		h.refreshConsumedToken(reqCtx, c, stored, now)
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
		if err := h.refreshTokenRepo.RevokeFamily(writeCtx, stored.SessionFamily()); err != nil {
			slog.Error("Failed to revoke expired refresh session", "user_id", stored.UserID, "error", err)
		}
		h.clearRefreshCookie(c)
		middleware.RecordRefresh("expired")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Session expired"})
		return
	}

	// Check idle timeout. Authenticated requests move LastActivity forward
	// (session activity hook in the JWT middleware), so this is the time
	// since the last request, not since the last refresh.
	if now.Sub(stored.LastActivity) > h.cfg.SessionIdleTimeout {
		if err := h.refreshTokenRepo.RevokeByID(writeCtx, stored.ID); err != nil {
			slog.Error("Failed to revoke idle refresh token", "user_id", stored.UserID, "error", err)
		}
		h.clearRefreshCookie(c)
		middleware.RecordRefresh("expired")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Session idle timeout exceeded"})
		return
	}

	// Look up user to get current role/username.
	user, err := h.userRepo.FindByID(reqCtx, stored.UserID)
	if err != nil {
		if respondCancelled(c, err) {
			return
		}
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
		if err := h.refreshTokenRepo.RevokeAllForUser(writeCtx, stored.UserID); err != nil {
			slog.Error("Failed to revoke refresh tokens of disabled user", "user_id", stored.UserID, "error", err)
		}
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
	err = h.refreshTokenRepo.WithTx(writeCtx, func(txRepo models.RefreshTokenRepository) error {
		affected, err := txRepo.MarkRotatedIfActive(writeCtx, stored.ID, now)
		if err != nil {
			return err
		}
		if affected == 0 {
			return errRefreshTokenConsumed
		}
		// A TouchFamily that committed after the read above updated only the
		// old row, and the rotation now excludes that row from later touches.
		// Read the old row again (after MarkRotatedIfActive, so the row lock
		// is held and the read sees the latest activity) and carry the later
		// time forward. Never use now: a refresh is not activity.
		txSess, carryErr := carryLatestActivity(writeCtx, txRepo, tokenHash, sess)
		if carryErr != nil {
			return carryErr
		}
		var issueErr error
		newRawToken, newExpiresAt, issueErr = h.issueRefreshTokenWith(writeCtx, c, txRepo, user.ID, txSess, stored.ID)
		return issueErr
	})
	if errors.Is(err, errRefreshTokenConsumed) {
		// Another request consumed the token after our read. Re-read it so a
		// concurrent rotation gets the grace path and a revocation does not.
		latest, ok := h.findRefreshToken(writeCtx, c, tokenHash)
		if !ok {
			return
		}
		h.refreshConsumedToken(writeCtx, c, latest, now)
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
	// On rejection revoke the rotated refresh token too. The re-read keeps the
	// request context: a client that left gets 499 and no revoke-all.
	if err := recheckUserActive(reqCtx, h.userRepo, user.ID); err != nil {
		h.rejectRefreshAfterRecheck(writeCtx, c, user.ID, err)
		return
	}
	// Close the revoke race: a logout-all or admin revoke that ran after the
	// rotation committed has revoked the new token; do not hand out a token.
	if err := recheckSessionActive(writeCtx, h.refreshTokenRepo, sess.familyID); err != nil {
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

// findRefreshToken looks up a refresh token by hash with ctx. On failure it
// writes the response (401 for an unknown token, 499 for a cancelled ctx, 500
// for a repository error) and returns false.
func (h *AuthHandler) findRefreshToken(ctx context.Context, c *gin.Context, tokenHash string) (*models.RefreshToken, bool) {
	stored, err := h.refreshTokenRepo.FindByTokenHash(ctx, tokenHash)
	if err == nil {
		return stored, true
	}
	if respondCancelled(c, err) {
		return nil, false
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
//
// readCtx bounds the grace reads. The family revoke always uses a detached
// context: a client that leaves must never skip the revoke of a replayed
// family (#656).
func (h *AuthHandler) refreshConsumedToken(readCtx context.Context, c *gin.Context, stored *models.RefreshToken, now time.Time) {
	family := stored.SessionFamily()
	if h.withinReuseGrace(stored, now) {
		user, err := h.userRepo.FindByID(readCtx, stored.UserID)
		if err != nil && respondCancelled(c, err) {
			return
		}
		if err != nil && !isNotFoundError(err) {
			slog.Error("Failed to find user for refresh", "user_id", stored.UserID, "error", err)
			middleware.RecordRefresh("failure")
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}
		if err == nil && !user.Disabled {
			active, countErr := h.refreshTokenRepo.CountActiveInFamily(readCtx, family)
			if countErr != nil {
				if respondCancelled(c, countErr) {
					return
				}
				slog.Error("Failed to check refresh token family", "user_id", stored.UserID, "error", countErr)
				middleware.RecordRefresh("failure")
				c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
				return
			}
			if active > 0 {
				h.respondGraceRefresh(readCtx, c, user, family)
				return
			}
		}
	}

	slog.Warn("Refresh token reuse detected; revoking session", "user_id", stored.UserID, "token_id", stored.ID)
	if err := h.refreshTokenRepo.RevokeFamily(context.WithoutCancel(c.Request.Context()), family); err != nil {
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
// access token only. It does not touch the refresh cookie. readCtx bounds the
// session re-check. The user re-check keeps the request context on purpose: a
// cancel gives 499 and never revokes all sessions.
func (h *AuthHandler) respondGraceRefresh(readCtx context.Context, c *gin.Context, user *models.User, sessionID string) {
	accessToken, err := h.sessionAccessToken(user, sessionID)
	if err != nil {
		slog.Error("Failed to generate access token during refresh", "error", err)
		middleware.RecordRefresh("failure")
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	if err := recheckUserActive(c.Request.Context(), h.userRepo, user.ID); err != nil {
		h.rejectRefreshAfterRecheck(context.WithoutCancel(c.Request.Context()), c, user.ID, err)
		return
	}
	if err := recheckSessionActive(readCtx, h.refreshTokenRepo, sessionID); err != nil {
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
func recheckSessionActive(ctx context.Context, repo models.RefreshTokenRepository, familyID string) error {
	active, err := repo.CountActiveInFamily(ctx, familyID)
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
func abandonSession(ctx context.Context, repo models.RefreshTokenRepository, familyID string) {
	if err := repo.RevokeFamily(ctx, familyID); err != nil {
		slog.Error("Failed to revoke abandoned session", "family_id", familyID, "error", err)
	}
}

// rejectRefreshAfterSessionCheck ends a refresh whose session re-check failed.
// It clears the cookie and revokes nothing more: the family is already revoked,
// and a repository error is no reason to end other sessions. rotated tells
// whether the rotation transaction already committed (the client then never
// receives the new refresh token).
func (h *AuthHandler) rejectRefreshAfterSessionCheck(c *gin.Context, userID, familyID string, rotated bool, err error) {
	if respondCancelled(c, err) {
		return
	}
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
// revokes all refresh tokens of the user with writeCtx (detached) and clears
// the cookie. A cancelled request gives 499 and revokes nothing: the token
// never leaves the server, and a client that left is no reason to end the
// other sessions (#656).
func (h *AuthHandler) rejectRefreshAfterRecheck(writeCtx context.Context, c *gin.Context, userID string, err error) {
	if respondCancelled(c, err) {
		return
	}
	if revokeErr := h.refreshTokenRepo.RevokeAllForUser(writeCtx, userID); revokeErr != nil {
		slog.Error("Failed to revoke refresh tokens after user re-check", "user_id", userID, "error", revokeErr)
	}
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
// @Description Revokes the session of the refresh token cookie (all refresh tokens of its family), blocklists the access token and closes its WebSockets. When the refresh token lookup or the revoke fails, the cookie is still cleared and the access token blocked, but the answer is 500.
// @Tags        auth
// @Accept      json
// @Produce     json
// @Success     200 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/auth/logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	// Detached: a client that leaves is still logged out (#656).
	ctx := context.WithoutCancel(c.Request.Context())

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
						if blockErr := h.sessionStore.BlockToken(ctx, claims.ID, expiry); blockErr != nil {
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

	// Revoke the whole session of the refresh token: a rotated token can
	// have an active successor (#673). A failure still clears the cookie but
	// answers 500, because the session can stay valid on the server.
	revokeFailed := false
	if h.refreshTokenRepo != nil {
		if rawToken, err := c.Cookie(refreshTokenCookieName); err == nil && rawToken != "" {
			tokenHash := hashRefreshToken(rawToken)
			stored, err := h.refreshTokenRepo.FindByTokenHash(ctx, tokenHash)
			switch {
			case err == nil:
				if revokeErr := h.refreshTokenRepo.RevokeFamily(ctx, stored.SessionFamily()); revokeErr != nil {
					slog.Error("Failed to revoke the refresh session on logout", "user_id", stored.UserID, "error", revokeErr)
					revokeFailed = true
				}
			case !isNotFoundError(err):
				slog.Error("Failed to look up refresh token on logout", "error", err)
				revokeFailed = true
			}
		}
	}

	h.clearRefreshCookie(c)
	if revokeFailed {
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
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

	// Detached: a client that leaves is still logged out everywhere (#656).
	ctx := context.WithoutCancel(c.Request.Context())

	// Blocklist the current access token.
	if h.sessionStore != nil {
		jti := middleware.GetJTIFromContext(c)
		if jti != "" {
			expiry, ok := middleware.GetTokenExpiryFromContext(c)
			if !ok {
				expiry = time.Now().Add(h.cfg.AccessTokenExpiration)
			}
			if blockErr := h.sessionStore.BlockToken(ctx, jti, expiry); blockErr != nil {
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
		if err := h.refreshTokenRepo.RevokeAllForUser(ctx, userID); err != nil {
			slog.Error("Failed to revoke all refresh tokens", "user_id", userID, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}
	}

	h.clearRefreshCookie(c)
	c.JSON(http.StatusOK, gin.H{"message": "Logged out from all sessions"})
}

// CleanupExpiredTokens deletes expired refresh tokens from the store. The
// leader worker refresh-token-cleanup calls it with its own context.
func (h *AuthHandler) CleanupExpiredTokens(ctx context.Context) {
	if h.refreshTokenRepo == nil {
		return
	}
	deleted, err := h.refreshTokenRepo.DeleteExpired(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// The leader term ended: the next term cleans up.
			return
		}
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

// carryLatestActivity returns sess with lastActivity set to the later of
// sess.lastActivity and the LastActivity that repo now stores for the token
// with tokenHash. The rotation calls it inside its transaction, after it has
// marked the token rotated, so a concurrent TouchFamily on the old row is not
// lost.
func carryLatestActivity(ctx context.Context, repo models.RefreshTokenRepository, tokenHash string, sess refreshSession) (refreshSession, error) {
	current, err := repo.FindByTokenHash(ctx, tokenHash)
	if err != nil {
		return sess, err
	}
	if current.LastActivity.After(sess.lastActivity) {
		sess.lastActivity = current.LastActivity
	}
	return sess, nil
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
func (h *AuthHandler) issueRefreshTokenWith(ctx context.Context, c *gin.Context, repo models.RefreshTokenRepository, userID string, sess refreshSession, excludeTokenID string) (string, time.Time, error) {
	return createRefreshToken(ctx, c, repo, h.cfg, userID, sess, excludeTokenID)
}

// createRefreshToken stores a new refresh token of the session and returns the
// raw token and its expiry. The expiry is the earlier of now +
// RefreshTokenExpiration and the session deadline, so rotation never extends
// a session. Shared by local login, refresh and OIDC login. ctx bounds the
// queries; c gives only the user agent and the client IP.
func createRefreshToken(ctx context.Context, c *gin.Context, repo models.RefreshTokenRepository, cfg *config.AuthConfig, userID string, sess refreshSession, excludeTokenID string) (string, time.Time, error) {
	// Keep the user within MaxRefreshTokensPerUser: revoke the oldest active
	// tokens so that the new one fits, but never the newest ones. Revoking all
	// of them (the old rule) also revoked the session of a login of the same
	// user that ran at the same time; its session re-check then failed with
	// 401 "Session revoked" (#619). On refresh, MarkRotatedIfActive has
	// already revoked the consumed token, so it is not active here; excluding
	// it is defense in depth, so the cap never revokes the token in use.
	if cfg.MaxRefreshTokensPerUser > 0 {
		activeCount, err := repo.CountActiveForUser(ctx, userID)
		if err != nil {
			return "", time.Time{}, err
		}
		if int(activeCount) >= cfg.MaxRefreshTokensPerUser {
			if _, err := repo.RevokeOldestForUser(ctx, userID, cfg.MaxRefreshTokensPerUser-1, excludeTokenID); err != nil {
				return "", time.Time{}, err
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

	if err := repo.Create(ctx, rt); err != nil {
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
