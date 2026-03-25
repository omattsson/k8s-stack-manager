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
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// OIDCHandler handles OpenID Connect authentication endpoints.
type OIDCHandler struct {
	provider   *auth.Provider
	stateStore *auth.StateStore
	userRepo   models.UserRepository
	cfg        *config.OIDCConfig
	authCfg    *config.AuthConfig
}

// NewOIDCHandler creates a new OIDCHandler.
func NewOIDCHandler(provider *auth.Provider, stateStore *auth.StateStore, userRepo models.UserRepository, cfg *config.OIDCConfig, authCfg *config.AuthConfig) *OIDCHandler {
	return &OIDCHandler{
		provider:   provider,
		stateStore: stateStore,
		userRepo:   userRepo,
		cfg:        cfg,
		authCfg:    authCfg,
	}
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
		"local_auth_enabled": h.cfg.LocalAuth,
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	verifier, challenge, err := auth.GenerateCodeVerifier()
	if err != nil {
		slog.Error("Failed to generate PKCE code verifier", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	redirectURL := c.DefaultQuery("redirect", "/")
	// Validate the redirect is a safe relative path — prevent open redirects.
	if !isSafeRedirect(redirectURL) {
		redirectURL = "/"
	}

	h.stateStore.Store(&auth.AuthState{
		State:        state,
		CodeVerifier: verifier,
		RedirectURL:  redirectURL,
		CreatedAt:    time.Now(),
	})

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
// @Success      302   {string} string "Redirect to frontend with JWT"
// @Failure      302   {string} string "Redirect to login with error"
// @Router       /api/v1/auth/oidc/callback [get]
func (h *OIDCHandler) Callback(c *gin.Context) {
	stateParam := c.Query("state")
	code := c.Query("code")

	// Validate state (one-time use — prevents CSRF and replay).
	authState, ok := h.stateStore.Retrieve(stateParam)
	if !ok {
		slog.Warn("OIDC callback with invalid or expired state")
		c.Redirect(http.StatusFound, "/login?error=invalid_state")
		return
	}

	// Exchange authorization code for tokens.
	oidcUser, err := h.provider.Exchange(c.Request.Context(), code, authState.CodeVerifier)
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
	token, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
		UserID:       user.ID,
		Username:     user.Username,
		Role:         user.Role,
		Secret:       h.authCfg.JWTSecret,
		Expiration:   h.authCfg.JWTExpiration,
		AuthProvider: "oidc",
		Email:        user.Email,
	})
	if err != nil {
		slog.Error("Failed to generate JWT for OIDC user", "error", err)
		c.Redirect(http.StatusFound, "/login?error=auth_failed")
		return
	}

	// Redirect to frontend with token.
	redirectPath := authState.RedirectURL
	if redirectPath == "" {
		redirectPath = "/"
	}
	params := url.Values{}
	params.Set("token", token)
	params.Set("redirect", redirectPath)
	c.Redirect(http.StatusFound, "/auth/callback#"+params.Encode())
}

func (h *OIDCHandler) provisionUser(oidcUser *auth.OIDCUser) (*models.User, error) {
	// Try to find existing user by external ID.
	user, err := h.userRepo.FindByExternalID("oidc", oidcUser.Subject)
	if err == nil && user != nil {
		// Update user if details changed.
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
				return nil, fmt.Errorf("auth_failed")
			}
		}
		return user, nil
	}

	// Distinguish "not found" from unexpected DB errors — only proceed
	// to user creation when the user genuinely doesn't exist.
	if err != nil && !isNotFoundError(err) {
		slog.Error("Failed to look up OIDC user", "external_id", oidcUser.Subject, "error", err)
		return nil, fmt.Errorf("auth_failed")
	}

	// User not found — check auto-provisioning.
	if !h.cfg.AutoProvision {
		return nil, fmt.Errorf("no_account")
	}

	// Determine username — prefer email, fall back to name, then subject.
	username := oidcUser.Email
	if username == "" {
		username = oidcUser.Name
	}
	if username == "" {
		username = oidcUser.Subject
	}

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
			user, err = h.userRepo.FindByExternalID("oidc", oidcUser.Subject)
			if err == nil && user != nil {
				return user, nil
			}
		}
		slog.Error("Failed to create OIDC user", "username", username, "error", createErr)
		return nil, fmt.Errorf("auth_failed")
	}

	slog.Info("OIDC user provisioned", "user_id", newUser.ID, "username", username)
	return newUser, nil
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
