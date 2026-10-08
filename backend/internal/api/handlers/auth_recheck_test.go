package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/config"
	"backend/internal/models"
	"backend/internal/sessionstore"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// raceUserRepo simulates an admin action that lands while a token is being
// issued: FindByID call number >= disableFrom returns the user as disabled
// (or as missing when gone is set). Other reads see the stored user.
type raceUserRepo struct {
	*MockUserRepository
	calls       atomic.Int64
	disableFrom int64 // 0 = never
	gone        bool
}

func (r *raceUserRepo) FindByID(id string) (*models.User, error) {
	n := r.calls.Add(1)
	u, err := r.MockUserRepository.FindByID(id)
	if err != nil || r.disableFrom == 0 || n < r.disableFrom {
		return u, err
	}
	if r.gone {
		return nil, errors.New("not found")
	}
	cp := *u
	cp.Disabled = true
	return &cp, nil
}

func refreshCookieFrom(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == "refresh_token" {
			return c
		}
	}
	return nil
}

func TestLogin_DisabledDuringIssue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		disableFrom int64
		gone        bool
		wantStatus  int
		wantError   string
	}{
		{name: "no admin action", disableFrom: 0, wantStatus: http.StatusOK},
		{name: "disabled after first check", disableFrom: 1, wantStatus: http.StatusForbidden, wantError: "Account disabled"},
		{name: "deleted after first check", disableFrom: 1, gone: true, wantStatus: http.StatusUnauthorized, wantError: "Invalid username or password"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base := NewMockUserRepository()
			seedUser(t, base, "uid-1", "alice", "secret-pass", "user")
			repo := &raceUserRepo{MockUserRepository: base, disableFrom: tt.disableFrom, gone: tt.gone}
			refreshRepo := NewMockRefreshTokenRepository()

			h := NewAuthHandler(repo, testAuthConfigWithRefresh(), &config.OIDCConfig{})
			h.SetRefreshTokenRepo(refreshRepo)
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.POST("/api/v1/auth/login", h.Login)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login",
				bytes.NewBufferString(`{"username":"alice","password":"secret-pass"}`))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			active, err := refreshRepo.CountActiveForUser("uid-1")
			require.NoError(t, err)

			if tt.wantStatus == http.StatusOK {
				assert.Contains(t, w.Body.String(), `"token"`)
				assert.Equal(t, int64(1), active)
				return
			}
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, tt.wantError, body["error"])
			assert.NotContains(t, body, "token", "no access token in the response")
			assert.Nil(t, refreshCookieFrom(w), "no refresh cookie")
			assert.Zero(t, active, "no refresh token persisted")
		})
	}
}

func TestRefresh_DisabledDuringIssue(t *testing.T) {
	t.Parallel()

	base := NewMockUserRepository()
	seedUser(t, base, "uid-1", "alice", "secret-pass", "user")
	// FindByID #1 = refresh's own user read, #2 = the re-check after signing.
	repo := &raceUserRepo{MockUserRepository: base}
	refreshRepo := NewMockRefreshTokenRepository()

	h := NewAuthHandler(repo, testAuthConfigWithRefresh(), &config.OIDCConfig{})
	h.SetRefreshTokenRepo(refreshRepo)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/auth/login", h.Login)
	r.POST("/api/v1/auth/refresh", h.Refresh)

	// Log in normally to get a refresh cookie (login re-check is FindByID #1).
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login",
		bytes.NewBufferString(`{"username":"alice","password":"secret-pass"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	cookie := refreshCookieFrom(w)
	require.NotNil(t, cookie)

	// Refresh: #2 = refresh's user read (active), #3 = re-check (disabled).
	repo.disableFrom = 3

	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.AddCookie(cookie)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "Account disabled")
	assert.NotContains(t, w.Body.String(), `"token"`)
	if c := refreshCookieFrom(w); c != nil {
		assert.Empty(t, c.Value, "refresh cookie must be cleared, not set")
	}
	active, err := refreshRepo.CountActiveForUser("uid-1")
	require.NoError(t, err)
	assert.Zero(t, active, "rotated refresh token must be revoked")
}

func TestOIDCCallback_DisabledDuringIssue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cli  bool
	}{
		{name: "web flow", cli: false},
		{name: "CLI flow", cli: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, store, base := newOIDCHandlerSetup(t, false)
			// Provisioning reads by external ID; the first FindByID is the re-check.
			h.userRepo = &raceUserRepo{MockUserRepository: base, disableFrom: 1}
			refreshRepo := NewMockRefreshTokenRepository()
			h.refreshTokenRepo = refreshRepo

			ctx := context.Background()
			redirect := "/"
			const sessionID = "cli-session-race"
			if tt.cli {
				redirect = "cli:" + sessionID
				require.NoError(t, store.SaveCLIAuth(ctx, sessionID, sessionstore.CLIAuthData{Status: "pending"}, 10*time.Minute))
			}
			require.NoError(t, store.SaveOIDCState(ctx, "state-race", sessionstore.OIDCStateData{
				CodeVerifier: "test-verifier", RedirectURL: redirect,
			}, 5*time.Minute))

			r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=state-race", nil)
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusFound, w.Code, w.Body.String())
			loc := w.Header().Get("Location")
			assert.Contains(t, loc, "/login?error=account_disabled")
			assert.NotContains(t, loc, "token=")
			assert.Nil(t, refreshCookieFrom(w), "no refresh cookie")
			assert.Empty(t, refreshRepo.tokens, "no refresh token persisted")

			if tt.cli {
				data, err := store.GetCLIAuth(ctx, sessionID)
				require.NoError(t, err)
				require.NotNil(t, data)
				assert.Equal(t, "pending", data.Status, "CLI session must not complete")
				assert.Empty(t, data.Token, "no long-lived CLI token stored")
			}
		})
	}
}
