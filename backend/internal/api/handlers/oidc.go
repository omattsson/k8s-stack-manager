package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/auth"
	"backend/internal/config"
	"backend/internal/models"
	"backend/internal/sessionstore"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// OIDC handler message constants.
const (
	errMsgAuthFailed = "auth_failed"
)

func redactSessionID(id string) string {
	if len(id) >= 8 {
		return id[:8] + "..."
	}
	return "***"
}

// CLITokenRequest is the request body for polling CLI auth status.
type CLITokenRequest struct {
	SessionID string `json:"session_id" binding:"required"`
}

var cliAuthSuccessPage = []byte(`<!DOCTYPE html>
<html><head><title>Authentication Successful</title></head>
<body style="font-family:system-ui,sans-serif;text-align:center;padding:60px">
<h1>Authentication successful</h1>
<p>You can close this tab and return to your terminal.</p>
</body></html>`)

// OIDCHandler handles OpenID Connect authentication endpoints.
type OIDCHandler struct {
	provider         *auth.Provider
	sessionStore     sessionstore.SessionStore
	userRepo         models.UserRepository
	refreshTokenRepo models.RefreshTokenRepository
	cfg              *config.OIDCConfig
	authCfg          *config.AuthConfig
}

// NewOIDCHandler creates a new OIDCHandler.
func NewOIDCHandler(provider *auth.Provider, sessionStore sessionstore.SessionStore, userRepo models.UserRepository, cfg *config.OIDCConfig, authCfg *config.AuthConfig) *OIDCHandler {
	return &OIDCHandler{
		provider:     provider,
		sessionStore: sessionStore,
		userRepo:     userRepo,
		cfg:          cfg,
		authCfg:      authCfg,
	}
}

// SetRefreshTokenRepo sets the refresh token repository for the OIDC handler.
func (h *OIDCHandler) SetRefreshTokenRepo(repo models.RefreshTokenRepository) {
	h.refreshTokenRepo = repo
}

// GetConfig godoc
// @Summary      Get OIDC configuration
// @Description  Returns public OIDC configuration for the frontend (enabled status, provider name, local auth availability)
// @Tags         auth
// @Produce      json
// @Success      200 {object} map[string]interface{}
// @Router       /api/v1/auth/oidc/config [get]
func (h *OIDCHandler) GetConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"enabled":            h.cfg.Enabled,
		"provider_name":      deriveProviderName(h.cfg.ProviderURL),
		"local_auth_enabled": !h.cfg.Enabled || h.cfg.LocalAuth,
	})
}

// Authorize godoc
// @Summary      Start OIDC authorization flow
// @Description  Generates PKCE parameters and state, returns the IdP authorization URL for the frontend to redirect to
// @Tags         auth
// @Produce      json
// @Param        redirect query string false "Frontend URL to return to after authentication"
// @Success      200 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/auth/oidc/authorize [get]
func (h *OIDCHandler) Authorize(c *gin.Context) {
	if !h.cfg.Enabled {
		c.JSON(http.StatusNotFound, gin.H{"error": "OIDC is not enabled"})
		return
	}

	state, err := auth.GenerateState()
	if err != nil {
		slog.Error("Failed to generate OIDC state", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	verifier, challenge, err := auth.GenerateCodeVerifier()
	if err != nil {
		slog.Error("Failed to generate PKCE code verifier", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	redirectURL := c.DefaultQuery("redirect", "/")
	// Validate the redirect is a safe relative path — prevent open redirects.
	if !isSafeRedirect(redirectURL) {
		redirectURL = "/"
	}

	if err := h.sessionStore.SaveOIDCState(c.Request.Context(), state, sessionstore.OIDCStateData{
		CodeVerifier: verifier,
		RedirectURL:  redirectURL,
	}, h.cfg.StateTTL); err != nil {
		slog.Error("Failed to save OIDC state", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	authURL := h.provider.AuthorizationURL(state, challenge)
	c.JSON(http.StatusOK, gin.H{"redirect_url": authURL})
}

// Callback godoc
// @Summary      OIDC callback
// @Description  Handles the IdP callback after user authentication. Exchanges the authorization code for tokens, provisions or updates the local user, and redirects to the frontend with a JWT.
// @Tags         auth
// @Produce      html
// @Param        code  query string true  "Authorization code from IdP"
// @Param        state query string true  "State parameter for CSRF validation"
// @Success      200   {string} string "CLI auth success page (HTML)"
// @Success      302   {string} string "Redirect to frontend with JWT"
// @Failure      302   {string} string "Redirect to login with error"
// @Failure      410   {string} string "CLI session expired (HTML)"
// @Failure      500   {string} string "Internal error (HTML, CLI flow only)"
// @Router       /api/v1/auth/oidc/callback [get]
func (h *OIDCHandler) Callback(c *gin.Context) {
	stateParam := c.Query("state")
	code := c.Query("code")

	// Validate state (one-time use — prevents CSRF and replay).
	stateData, stateErr := h.sessionStore.ConsumeOIDCState(c.Request.Context(), stateParam)
	if stateErr != nil || stateData == nil {
		if stateErr != nil {
			slog.Error("OIDC state lookup failed", "error", stateErr)
		} else {
			slog.Warn("OIDC callback with invalid or expired state")
		}
		c.Redirect(http.StatusFound, "/login?error=invalid_state")
		return
	}

	// Exchange authorization code for tokens.
	oidcUser, err := h.provider.Exchange(c.Request.Context(), code, stateData.CodeVerifier)
	if err != nil {
		slog.Error("OIDC token exchange failed", "error", err)
		c.Redirect(http.StatusFound, "/login?error=auth_failed")
		return
	}

	// Provision or update local user.
	user, err := h.provisionUser(oidcUser)
	if err != nil {
		slog.Error("OIDC user provisioning failed", "error", err)
		c.Redirect(http.StatusFound, "/login?error="+err.Error())
		return
	}

	// Generate local JWT with OIDC-specific claims.
	expiration := h.authCfg.AccessTokenExpiration
	if h.refreshTokenRepo == nil {
		expiration = h.authCfg.JWTExpiration
	}

	token, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
		UserID:       user.ID,
		Username:     user.Username,
		DisplayName:  user.DisplayName,
		Role:         user.Role,
		Secret:       h.authCfg.JWTSecret,
		Expiration:   expiration,
		AuthProvider: "oidc",
		Email:        user.Email,
	})
	if err != nil {
		slog.Error("Failed to generate JWT for OIDC user", "error", err)
		c.Redirect(http.StatusFound, "/login?error=auth_failed")
		return
	}

	// Issue refresh token cookie for OIDC users too.
	if h.refreshTokenRepo != nil {
		if issueErr := issueOIDCRefreshToken(c, h.refreshTokenRepo, h.authCfg, user.ID); issueErr != nil {
			slog.Error("Failed to issue refresh token for OIDC user", "user_id", user.ID, "error", issueErr)
			// Continue — access token is still valid.
		}
	}

	// Check if this is a CLI auth session.
	if strings.HasPrefix(stateData.RedirectURL, "cli:") {
		sessionID := strings.TrimPrefix(stateData.RedirectURL, "cli:")

		// CLI can't use refresh token cookies, so issue a long-lived JWT.
		cliToken := token
		if h.refreshTokenRepo != nil {
			longLived, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
				UserID:       user.ID,
				Username:     user.Username,
				DisplayName:  user.DisplayName,
				Role:         user.Role,
				Secret:       h.authCfg.JWTSecret,
				Expiration:   h.authCfg.JWTExpiration,
				AuthProvider: "oidc",
				Email:        user.Email,
			})
			if err == nil {
				cliToken = longLived
			} else {
				slog.Warn("Failed to generate long-lived CLI token, using short-lived", "error", err)
			}
		}

		if err := h.sessionStore.UpdateCLIAuth(c.Request.Context(), sessionID, sessionstore.CLIAuthData{
			Token:    cliToken,
			UserID:   user.ID,
			Username: user.Username,
			Status:   "completed",
		}); err != nil {
			if errors.Is(err, sessionstore.ErrSessionNotFound) {
				slog.Warn("CLI auth session expired or not found", "session_id", redactSessionID(sessionID))
				c.Data(http.StatusGone, "text/html; charset=utf-8", []byte(`<html><body><h1>Session Expired</h1><p>The CLI login session has expired. Please run the login command again.</p></body></html>`))
			} else {
				slog.Error("Failed to update CLI auth session", "session_id", redactSessionID(sessionID), "error", err)
				c.Data(http.StatusInternalServerError, "text/html; charset=utf-8", []byte(`<html><body><h1>Error</h1><p>Something went wrong. Please try again.</p></body></html>`))
			}
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", cliAuthSuccessPage)
		return
	}

	// Redirect to frontend with token.
	redirectPath := stateData.RedirectURL
	if redirectPath == "" {
		redirectPath = "/"
	}
	params := url.Values{}
	params.Set("token", token)
	params.Set("redirect", redirectPath)
	c.Redirect(http.StatusFound, "/auth/callback#"+params.Encode())
}

// CLIAuth godoc
// @Summary      Start CLI SSO authentication flow
// @Description  Generates a session ID and OIDC authorization URL for CLI-based SSO login. The CLI opens the returned login_url in a browser and polls cli-token until authentication completes.
// @Tags         auth
// @Produce      json
// @Success      200 {object} map[string]interface{} "session_id, login_url, expires_in"
// @Failure      404 {object} map[string]string
// @Failure      500 {object} map[string]string
// @Router       /api/v1/auth/oidc/cli-auth [post]
func (h *OIDCHandler) CLIAuth(c *gin.Context) {
	if !h.cfg.Enabled {
		c.JSON(http.StatusNotFound, gin.H{"error": "OIDC is not enabled"})
		return
	}

	sessionID := uuid.New().String()

	state, err := auth.GenerateState()
	if err != nil {
		slog.Error("Failed to generate OIDC state for CLI auth", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	verifier, challenge, err := auth.GenerateCodeVerifier()
	if err != nil {
		slog.Error("Failed to generate PKCE code verifier for CLI auth", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	cliTTL := h.cfg.StateTTL
	if cliTTL <= 0 {
		cliTTL = 5 * time.Minute
	}

	if err := h.sessionStore.SaveOIDCState(c.Request.Context(), state, sessionstore.OIDCStateData{
		CodeVerifier: verifier,
		RedirectURL:  "cli:" + sessionID,
	}, cliTTL); err != nil {
		slog.Error("Failed to save OIDC state for CLI auth", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	if err := h.sessionStore.SaveCLIAuth(c.Request.Context(), sessionID, sessionstore.CLIAuthData{
		Status: "pending",
	}, cliTTL); err != nil {
		slog.Error("Failed to save CLI auth session", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	authURL := h.provider.AuthorizationURL(state, challenge)
	c.JSON(http.StatusOK, gin.H{
		"session_id": sessionID,
		"login_url":  authURL,
		"expires_in": int(cliTTL.Seconds()),
	})
}

// CLIToken godoc
// @Summary      Poll CLI SSO authentication status
// @Description  Returns the current status of a CLI auth session. Returns pending while waiting for the user to complete browser authentication, or completed with a JWT token once done.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        request body CLITokenRequest true "CLI token poll request"
// @Success      200 {object} map[string]interface{} "status, token (when completed), username, user_id"
// @Failure      400 {object} map[string]string
// @Failure      410 {object} map[string]string "session expired or not found"
// @Failure      500 {object} map[string]string
// @Router       /api/v1/auth/oidc/cli-token [post]
func (h *OIDCHandler) CLIToken(c *gin.Context) {
	var req CLITokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}

	// Peek first to check for pending status without consuming.
	data, err := h.sessionStore.GetCLIAuth(c.Request.Context(), req.SessionID)
	if err != nil {
		slog.Error("Failed to look up CLI auth session", "session_id", redactSessionID(req.SessionID), "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	if data == nil {
		c.JSON(http.StatusGone, gin.H{"error": "session expired or not found"})
		return
	}
	if data.Status == "pending" {
		c.JSON(http.StatusOK, gin.H{"status": "pending"})
		return
	}

	// Atomically consume the completed session — only one poll gets the token.
	consumed, err := h.sessionStore.ConsumeCLIAuth(c.Request.Context(), req.SessionID)
	if err != nil {
		slog.Error("Failed to consume CLI auth session", "session_id", redactSessionID(req.SessionID), "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	if consumed == nil || consumed.Token == "" {
		c.JSON(http.StatusGone, gin.H{"error": "session expired or already consumed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   "completed",
		"token":    consumed.Token,
		"username": consumed.Username,
		"user_id":  consumed.UserID,
	})
}

func (h *OIDCHandler) provisionUser(oidcUser *auth.OIDCUser) (*models.User, error) {
	// Try to find existing user by external ID.
	user, err := h.userRepo.FindByExternalID("oidc", oidcUser.Subject)
	if err == nil && user != nil {
		return h.updateExistingOIDCUser(user, oidcUser)
	}

	// Distinguish "not found" from unexpected DB errors — only proceed
	// to user creation when the user genuinely doesn't exist.
	if err != nil && !isNotFoundError(err) {
		slog.Error("Failed to look up OIDC user", "external_id", oidcUser.Subject, "error", err)
		return nil, fmt.Errorf(errMsgAuthFailed)
	}

	// Check if a local user with the same username already exists — link it to OIDC.
	username := deriveOIDCUsername(oidcUser)
	existing, err := h.userRepo.FindByUsername(username)
	if err == nil && existing != nil {
		return h.linkLocalUserToOIDC(existing, oidcUser)
	}
	if err != nil && !isNotFoundError(err) {
		slog.Error("Failed to look up user by username", "username", username, "error", err)
		return nil, fmt.Errorf(errMsgAuthFailed)
	}

	// User not found — check auto-provisioning.
	if !h.cfg.AutoProvision {
		return nil, fmt.Errorf("no_account")
	}

	return h.createOIDCUser(oidcUser)
}

// updateExistingOIDCUser syncs changed fields (email, display name, role) from the
// IdP response into the local user record.
func (h *OIDCHandler) updateExistingOIDCUser(user *models.User, oidcUser *auth.OIDCUser) (*models.User, error) {
	if user.Disabled {
		return nil, fmt.Errorf("account_disabled")
	}
	changed := false
	if oidcUser.Email != "" && user.Email != oidcUser.Email {
		user.Email = oidcUser.Email
		changed = true
	}
	if oidcUser.Name != "" && user.DisplayName != oidcUser.Name {
		user.DisplayName = oidcUser.Name
		changed = true
	}
	newRole := h.provider.MapRole(oidcUser.Roles)
	if newRole != user.Role {
		user.Role = newRole
		changed = true
	}
	if changed {
		if updateErr := h.userRepo.Update(user); updateErr != nil {
			slog.Error("Failed to update OIDC user", "user_id", user.ID, "error", updateErr)
			// Abort authentication to avoid issuing a token with
			// unpersisted changes (e.g., elevated role or new email).
			return nil, fmt.Errorf(errMsgAuthFailed)
		}
	}
	return user, nil
}

// linkLocalUserToOIDC converts a local user to an OIDC-linked user.
func (h *OIDCHandler) linkLocalUserToOIDC(user *models.User, oidcUser *auth.OIDCUser) (*models.User, error) {
	if user.Disabled {
		return nil, fmt.Errorf("account_disabled")
	}
	user.AuthProvider = "oidc"
	user.ExternalID = &oidcUser.Subject
	if oidcUser.Email != "" {
		user.Email = oidcUser.Email
	}
	if oidcUser.Name != "" {
		user.DisplayName = oidcUser.Name
	}
	user.Role = h.provider.MapRole(oidcUser.Roles)
	user.PasswordHash = ""

	if err := h.userRepo.Update(user); err != nil {
		slog.Error("Failed to link local user to OIDC", "user_id", user.ID, "error", err)
		return nil, fmt.Errorf(errMsgAuthFailed)
	}
	slog.Info("Local user linked to OIDC", "user_id", user.ID, "username", user.Username)
	return user, nil
}

// createOIDCUser provisions a new local user from the OIDC identity.
func (h *OIDCHandler) createOIDCUser(oidcUser *auth.OIDCUser) (*models.User, error) {
	username := deriveOIDCUsername(oidcUser)

	newUser := &models.User{
		ID:           uuid.New().String(),
		Username:     username,
		DisplayName:  oidcUser.Name,
		Role:         h.provider.MapRole(oidcUser.Roles),
		AuthProvider: "oidc",
		ExternalID:   &oidcUser.Subject,
		Email:        oidcUser.Email,
	}

	if createErr := h.userRepo.Create(newUser); createErr != nil {
		// Race condition: another request created the user between our check and create.
		// Retry the lookup to return the existing user.
		if isDuplicateError(createErr) {
			user, err := h.userRepo.FindByExternalID("oidc", oidcUser.Subject)
			if err == nil && user != nil {
				return user, nil
			}
		}
		slog.Error("Failed to create OIDC user", "username", username, "error", createErr)
		return nil, fmt.Errorf(errMsgAuthFailed)
	}

	slog.Info("OIDC user provisioned", "user_id", newUser.ID, "username", username)
	return newUser, nil
}

// deriveOIDCUsername picks a username from the OIDC user info, preferring
// email, then name, then subject as a fallback.
func deriveOIDCUsername(oidcUser *auth.OIDCUser) string {
	if oidcUser.Email != "" {
		return oidcUser.Email
	}
	// Only use Name as username if it looks like an identifier (no spaces).
	// Display names like "Jane Doe" from given_name/family_name are not
	// suitable as stable usernames.
	if oidcUser.Name != "" && !strings.Contains(oidcUser.Name, " ") {
		return oidcUser.Name
	}
	return oidcUser.Subject
}

func deriveProviderName(providerURL string) string {
	lower := strings.ToLower(providerURL)
	switch {
	case strings.Contains(lower, "login.microsoftonline.com"):
		return "Microsoft"
	case strings.Contains(lower, ".okta.com"):
		return "Okta"
	case strings.Contains(lower, "accounts.google.com"):
		return "Google"
	case strings.Contains(lower, "keycloak"):
		return "Keycloak"
	default:
		return "SSO Provider"
	}
}

// isSafeRedirect checks that the redirect target is a relative path,
// preventing open-redirect attacks via absolute URLs or protocol-relative URLs.
func isSafeRedirect(target string) bool {
	if target == "" {
		return false
	}
	// Must start with "/" and must not start with "//" (protocol-relative).
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		return false
	}
	// Parse to reject paths containing scheme-like constructs (e.g., "/\evil.com").
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	return u.Scheme == "" && u.Host == ""
}

// isNotFoundError checks whether an error represents a "not found" condition,
// using the typed dberrors system with string fallback for legacy callers.
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, dberrors.ErrNotFound) {
		return true
	}
	return strings.Contains(err.Error(), "not found")
}

// isDuplicateError checks whether an error represents a duplicate key violation,
// using the typed dberrors system with string fallback for legacy callers.
func isDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, dberrors.ErrDuplicateKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "Duplicate entry")
}

// issueOIDCRefreshToken creates and stores a refresh token for an OIDC-authenticated user
// and sets the httpOnly cookie. Enforces MaxRefreshTokensPerUser like the main auth flow.
func issueOIDCRefreshToken(c *gin.Context, repo models.RefreshTokenRepository, cfg *config.AuthConfig, userID string) error {
	// Enforce max tokens per user.
	if cfg.MaxRefreshTokensPerUser > 0 {
		activeCount, err := repo.CountActiveForUser(userID)
		if err != nil {
			return err
		}
		if int(activeCount) >= cfg.MaxRefreshTokensPerUser {
			if err := repo.RevokeAllForUser(userID); err != nil {
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
		ExpiresAt:    now.Add(cfg.RefreshTokenExpiration),
		LastActivity: now,
		CreatedAt:    now,
		UserAgent:    truncate(c.GetHeader("User-Agent"), 500),
		IPAddress:    c.ClientIP(),
	}

	if err := repo.Create(rt); err != nil {
		return err
	}

	maxAge := int(cfg.RefreshTokenExpiration.Seconds())
	c.SetSameSite(cfg.HTTPSameSite())
	c.SetCookie(
		refreshTokenCookieName,
		rawToken,
		maxAge,
		"/api/v1/auth",
		"",
		cfg.SecureCookies,
		true,
	)
	return nil
}
