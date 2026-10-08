package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/config"
	"backend/internal/models"
	"backend/internal/sessionstore"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Session lifecycle tests: reuse grace window (#448), idle timeout from the
// last request (#449), absolute session lifetime (#462) and the claims of
// refreshed access tokens (#446).

// sessionTestConfig returns the refresh-token test config with the session
// defaults (12h absolute lifetime, 30s reuse grace).
func sessionTestConfig(mutate ...func(*config.AuthConfig)) *config.AuthConfig {
	cfg := testAuthConfigWithRefresh()
	cfg.SessionMaxLifetime = 12 * time.Hour
	cfg.RefreshReuseGrace = 30 * time.Second
	for _, fn := range mutate {
		fn(cfg)
	}
	return cfg
}

type sessionTestEnv struct {
	cfg     *config.AuthConfig
	users   *MockUserRepository
	refresh *MockRefreshTokenRepository
	router  *gin.Engine
}

// newSessionTestEnv wires an AuthHandler with refresh tokens and a JWT
// middleware whose session activity hook writes to the refresh token mock,
// the same as the production wiring in bootstrap.
func newSessionTestEnv(t *testing.T, mutate ...func(*config.AuthConfig)) *sessionTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	env := &sessionTestEnv{
		cfg:     sessionTestConfig(mutate...),
		users:   NewMockUserRepository(),
		refresh: NewMockRefreshTokenRepository(),
	}
	store := sessionstore.NewMemoryStore()
	t.Cleanup(store.Stop)

	h := NewAuthHandler(env.users, env.cfg, &config.OIDCConfig{})
	h.SetRefreshTokenRepo(env.refresh)
	h.SetSessionStore(store)

	authMW := middleware.AuthRequiredWithOptions(middleware.JWTAuthOptions{
		JWTSecret:    env.cfg.JWTSecret,
		SessionStore: store,
		OnSessionActivity: func(ctx context.Context, sessionID string, at time.Time) error {
			return env.refresh.TouchFamily(ctx, sessionID, at)
		},
	})

	r := gin.New()
	auth := r.Group("/api/v1/auth")
	auth.POST("/login", h.Login)
	auth.POST("/refresh", h.Refresh)
	auth.POST("/logout", h.Logout)
	auth.GET("/me", authMW, h.GetCurrentUser)
	env.router = r
	return env
}

func (e *sessionTestEnv) seedUser(t *testing.T, provider, email string) *models.User {
	t.Helper()
	u := seedUser(t, e.users, "uid-1", "alice", "secret", "user")
	u.AuthProvider = provider
	u.Email = email
	require.NoError(t, e.users.Update(u))
	return u
}

func (e *sessionTestEnv) login(t *testing.T) (string, string) {
	t.Helper()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login",
		bytes.NewBufferString(`{"username":"alice","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	e.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp LoginResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	cookie := findRefreshCookie(w)
	require.NotNil(t, cookie, "login must set the refresh cookie")
	return resp.Token, cookie.Value
}

func (e *sessionTestEnv) postRefresh(raw string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: refreshTokenCookieName, Value: raw})
	e.router.ServeHTTP(w, req)
	return w
}

func (e *sessionTestEnv) postLogout(raw string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: refreshTokenCookieName, Value: raw})
	e.router.ServeHTTP(w, req)
	return w
}

func (e *sessionTestEnv) getMe(token string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	e.router.ServeHTTP(w, req)
	return w
}

// claimsOf validates the access token of a 200 login/refresh response body.
func (e *sessionTestEnv) claimsOf(t *testing.T, w *httptest.ResponseRecorder) *middleware.Claims {
	t.Helper()
	var resp RefreshResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Token)
	claims, err := middleware.ValidateJWT(resp.Token, e.cfg.JWTSecret)
	require.NoError(t, err)
	return claims
}

func (e *sessionTestEnv) tokenByRaw(t *testing.T, raw string) models.RefreshToken {
	t.Helper()
	rt, err := e.refresh.FindByTokenHash(hashRefreshToken(raw))
	require.NoError(t, err)
	return *rt
}

func (e *sessionTestEnv) tokenByID(t *testing.T, id string) models.RefreshToken {
	t.Helper()
	e.refresh.mu.RLock()
	defer e.refresh.mu.RUnlock()
	rt, ok := e.refresh.tokens[id]
	require.True(t, ok, "token %s not found", id)
	return *rt
}

func (e *sessionTestEnv) updateToken(t *testing.T, raw string, fn func(*models.RefreshToken)) {
	t.Helper()
	e.refresh.mu.Lock()
	defer e.refresh.mu.Unlock()
	rt, ok := e.refresh.byHash[hashRefreshToken(raw)]
	require.True(t, ok)
	fn(rt)
}

// seedToken stores a refresh token of user uid-1 with the raw value
// "raw-<id>" in family "fam-1", started one hour ago, last used now.
func (e *sessionTestEnv) seedToken(t *testing.T, id string, mutate func(*models.RefreshToken)) string {
	t.Helper()
	now := time.Now().UTC()
	raw := "raw-" + id
	rt := &models.RefreshToken{
		ID:               id,
		UserID:           "uid-1",
		FamilyID:         "fam-1",
		TokenHash:        hashRefreshToken(raw),
		ExpiresAt:        now.Add(24 * time.Hour),
		LastActivity:     now,
		CreatedAt:        now.Add(-time.Hour),
		SessionStartedAt: now.Add(-time.Hour),
	}
	if mutate != nil {
		mutate(rt)
	}
	require.NoError(t, e.refresh.Create(rt))
	return raw
}

func findRefreshCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == refreshTokenCookieName {
			return c
		}
	}
	return nil
}

// ---- #448: reuse grace window ----

func TestRefresh_ReuseGraceWindow(t *testing.T) {
	t.Parallel()

	ago := func(d time.Duration) *time.Time {
		at := time.Now().UTC().Add(-d)
		return &at
	}

	tests := []struct {
		name             string
		mutateCfg        func(*config.AuthConfig)
		presented        func(*models.RefreshToken) // state of the consumed token
		successorRevoked bool
		disableUser      bool
		wantStatus       int
		wantCleared      bool // cookie cleared (else: no refresh cookie at all)
		wantFamilyGone   bool // successor revoked after the call
	}{
		{
			name:       "rotated 5s ago gets an access token, no cookie, nothing revoked",
			presented:  func(rt *models.RefreshToken) { rt.RotatedAt = ago(5 * time.Second) },
			wantStatus: http.StatusOK,
		},
		{
			name:           "rotated 31s ago is a replay and revokes the family",
			presented:      func(rt *models.RefreshToken) { rt.RotatedAt = ago(31 * time.Second) },
			wantStatus:     http.StatusUnauthorized,
			wantCleared:    true,
			wantFamilyGone: true,
		},
		{
			name:           "logout-revoked token never gets grace",
			presented:      func(rt *models.RefreshToken) { rt.RotatedAt = nil },
			wantStatus:     http.StatusUnauthorized,
			wantCleared:    true,
			wantFamilyGone: true,
		},
		{
			name:           "grace disabled with REFRESH_REUSE_GRACE=0",
			mutateCfg:      func(c *config.AuthConfig) { c.RefreshReuseGrace = 0 },
			presented:      func(rt *models.RefreshToken) { rt.RotatedAt = ago(time.Second) },
			wantStatus:     http.StatusUnauthorized,
			wantCleared:    true,
			wantFamilyGone: true,
		},
		{
			name: "no grace after the absolute session lifetime",
			presented: func(rt *models.RefreshToken) {
				rt.RotatedAt = ago(5 * time.Second)
				rt.SessionStartedAt = time.Now().UTC().Add(-12*time.Hour - time.Minute)
			},
			wantStatus:     http.StatusUnauthorized,
			wantCleared:    true,
			wantFamilyGone: true,
		},
		{
			name:             "no grace when the family was logged out",
			presented:        func(rt *models.RefreshToken) { rt.RotatedAt = ago(5 * time.Second) },
			successorRevoked: true,
			wantStatus:       http.StatusUnauthorized,
			wantCleared:      true,
			wantFamilyGone:   true,
		},
		{
			name:           "no grace for a disabled user",
			presented:      func(rt *models.RefreshToken) { rt.RotatedAt = ago(5 * time.Second) },
			disableUser:    true,
			wantStatus:     http.StatusUnauthorized,
			wantCleared:    true,
			wantFamilyGone: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var mutators []func(*config.AuthConfig)
			if tt.mutateCfg != nil {
				mutators = append(mutators, tt.mutateCfg)
			}
			env := newSessionTestEnv(t, mutators...)
			u := env.seedUser(t, "local", "alice@example.com")
			if tt.disableUser {
				u.Disabled = true
				require.NoError(t, env.users.Update(u))
			}

			raw := env.seedToken(t, "rt-old", func(rt *models.RefreshToken) {
				rt.Revoked = true
				tt.presented(rt)
			})
			env.seedToken(t, "rt-new", func(rt *models.RefreshToken) { rt.Revoked = tt.successorRevoked })
			env.seedToken(t, "rt-other", func(rt *models.RefreshToken) { rt.FamilyID = "fam-2" })

			w := env.postRefresh(raw)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())

			cookie := findRefreshCookie(w)
			if tt.wantCleared {
				require.NotNil(t, cookie)
				assert.Less(t, cookie.MaxAge, 0, "refresh cookie must be cleared")
			} else {
				assert.Nil(t, cookie, "grace response must not set or clear the refresh cookie")
			}

			if tt.wantStatus == http.StatusOK {
				claims := env.claimsOf(t, w)
				assert.Equal(t, "fam-1", claims.SessionID)
				assert.Equal(t, "local", claims.AuthProvider)
				assert.Equal(t, "alice@example.com", claims.Email)
			}

			assert.Equal(t, tt.wantFamilyGone || tt.successorRevoked, env.tokenByID(t, "rt-new").Revoked)
			assert.False(t, env.tokenByID(t, "rt-other").Revoked, "other sessions of the user must stay active")
		})
	}
}

// Two tabs share one cookie: the second refresh presents the token the first
// one just rotated.
func TestRefresh_TwoTabsSameCookieSequential(t *testing.T) {
	t.Parallel()

	env := newSessionTestEnv(t)
	env.seedUser(t, "local", "alice@example.com")
	_, raw0 := env.login(t)
	family := env.tokenByRaw(t, raw0).FamilyID

	first := env.postRefresh(raw0)
	require.Equal(t, http.StatusOK, first.Code)
	newCookie := findRefreshCookie(first)
	require.NotNil(t, newCookie)
	require.Greater(t, newCookie.MaxAge, 0)

	second := env.postRefresh(raw0)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	assert.Nil(t, findRefreshCookie(second), "second tab must not get a Set-Cookie")
	assert.Equal(t, family, env.claimsOf(t, second).SessionID)

	active, err := env.refresh.CountActiveInFamily(family)
	require.NoError(t, err)
	assert.Equal(t, int64(1), active, "family must not be revoked")

	// The successor from the first refresh keeps working.
	third := env.postRefresh(newCookie.Value)
	require.Equal(t, http.StatusOK, third.Code)
	require.NotNil(t, findRefreshCookie(third))
}

// Both refreshes run at the same time with the same cookie. Whatever the
// interleaving, both get 200, exactly one rotates the cookie, and the family
// stays active.
func TestRefresh_ConcurrentSameCookie(t *testing.T) {
	t.Parallel()

	for i := 0; i < 10; i++ {
		i := i
		t.Run(fmt.Sprintf("run-%d", i), func(t *testing.T) {
			t.Parallel()

			env := newSessionTestEnv(t)
			env.seedUser(t, "local", "")
			_, raw0 := env.login(t)
			family := env.tokenByRaw(t, raw0).FamilyID

			start := make(chan struct{})
			results := make([]*httptest.ResponseRecorder, 2)
			var wg sync.WaitGroup
			for n := range results {
				wg.Add(1)
				go func(n int) {
					defer wg.Done()
					<-start
					results[n] = env.postRefresh(raw0)
				}(n)
			}
			close(start)
			wg.Wait()

			rotated := 0
			for _, w := range results {
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				assert.Equal(t, family, env.claimsOf(t, w).SessionID)
				if c := findRefreshCookie(w); c != nil {
					assert.Greater(t, c.MaxAge, 0, "no response may clear the cookie")
					rotated++
				}
			}
			assert.Equal(t, 1, rotated, "exactly one response sets the new refresh cookie")

			active, err := env.refresh.CountActiveInFamily(family)
			require.NoError(t, err)
			assert.Equal(t, int64(1), active, "family must stay active")
		})
	}
}

func TestRefresh_AfterLogoutNeverGetsGrace(t *testing.T) {
	t.Parallel()

	env := newSessionTestEnv(t)
	env.seedUser(t, "local", "")
	_, raw0 := env.login(t)

	require.Equal(t, http.StatusOK, env.postLogout(raw0).Code)
	assert.Nil(t, env.tokenByRaw(t, raw0).RotatedAt, "logout must not set rotated_at")

	w := env.postRefresh(raw0)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ---- #449: idle timeout from the last request ----

func TestRefresh_IdleTimeoutFromLastRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		lastRequest  time.Duration // 0 = no request since the last refresh
		wantStatus   int
		wantErrorMsg string
	}{
		{name: "request 20 min ago keeps the session (last refresh 31 min ago)", lastRequest: 20 * time.Minute, wantStatus: http.StatusOK},
		{name: "idle for 31 min ends the session", wantStatus: http.StatusUnauthorized, wantErrorMsg: "Session idle timeout exceeded"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSessionTestEnv(t)
			env.seedUser(t, "local", "")
			now := time.Now().UTC()
			// Last refresh (token issue) 31 min ago.
			raw := env.seedToken(t, "rt-idle", func(rt *models.RefreshToken) {
				rt.LastActivity = now.Add(-31 * time.Minute)
				rt.CreatedAt = now.Add(-31 * time.Minute)
			})
			if tt.lastRequest > 0 {
				// What the JWT middleware hook wrote at the last request.
				require.NoError(t, env.refresh.TouchFamily(context.Background(), "fam-1", now.Add(-tt.lastRequest)))
			}

			w := env.postRefresh(raw)
			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantErrorMsg != "" {
				assert.Contains(t, w.Body.String(), tt.wantErrorMsg)
			}
		})
	}
}

// An authenticated request with the session access token moves LastActivity
// of the family forward, so a refresh after a long gap since the last
// refresh still works.
func TestSessionActivity_AuthenticatedRequestKeepsSessionAlive(t *testing.T) {
	t.Parallel()

	env := newSessionTestEnv(t)
	env.seedUser(t, "local", "")
	access, raw0 := env.login(t)

	stale := time.Now().UTC().Add(-31 * time.Minute)
	env.updateToken(t, raw0, func(rt *models.RefreshToken) { rt.LastActivity = stale })

	require.Equal(t, http.StatusOK, env.getMe(access).Code)
	assert.Eventually(t, func() bool {
		return time.Since(env.tokenByRaw(t, raw0).LastActivity) < 5*time.Second
	}, 2*time.Second, 10*time.Millisecond, "the request must update last_activity")

	assert.Equal(t, http.StatusOK, env.postRefresh(raw0).Code)
}

// ---- #462: absolute session lifetime ----

func TestRefresh_SessionMaxLifetime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		token      func(now time.Time, rt *models.RefreshToken)
		wantStatus int
		wantSID    string
		// For 200: expected session start of the new token.
		wantStart func(now time.Time) time.Time
	}{
		{
			name: "family started 12h1m ago is rejected",
			token: func(now time.Time, rt *models.RefreshToken) {
				rt.SessionStartedAt = now.Add(-12*time.Hour - time.Minute)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "family started 11h ago rotates with a capped expiry",
			token: func(now time.Time, rt *models.RefreshToken) {
				rt.SessionStartedAt = now.Add(-11 * time.Hour)
			},
			wantStatus: http.StatusOK,
			wantSID:    "fam-1",
			wantStart:  func(now time.Time) time.Time { return now.Add(-11 * time.Hour) },
		},
		{
			name: "legacy token older than the lifetime is rejected (created_at fallback)",
			token: func(now time.Time, rt *models.RefreshToken) {
				rt.FamilyID = ""
				rt.SessionStartedAt = time.Time{}
				rt.CreatedAt = now.Add(-13 * time.Hour)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "legacy token rotates into a family named after its ID",
			token: func(now time.Time, rt *models.RefreshToken) {
				rt.FamilyID = ""
				rt.SessionStartedAt = time.Time{}
				rt.CreatedAt = now.Add(-time.Minute)
			},
			wantStatus: http.StatusOK,
			wantSID:    "rt-life",
			wantStart:  func(now time.Time) time.Time { return now.Add(-time.Minute) },
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSessionTestEnv(t)
			env.seedUser(t, "local", "")
			now := time.Now().UTC()
			raw := env.seedToken(t, "rt-life", func(rt *models.RefreshToken) { tt.token(now, rt) })

			w := env.postRefresh(raw)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			cookie := findRefreshCookie(w)
			require.NotNil(t, cookie)

			if tt.wantStatus != http.StatusOK {
				assert.Less(t, cookie.MaxAge, 0, "cookie must be cleared")
				assert.True(t, env.tokenByID(t, "rt-life").Revoked)
				return
			}

			assert.Equal(t, tt.wantSID, env.claimsOf(t, w).SessionID)
			next := env.tokenByRaw(t, cookie.Value)
			start := tt.wantStart(now)
			assert.Equal(t, tt.wantSID, next.FamilyID)
			assert.WithinDuration(t, start, next.SessionStartedAt, time.Second)
			deadline := start.Add(env.cfg.SessionMaxLifetime)
			assert.WithinDuration(t, deadline, next.ExpiresAt, 2*time.Second,
				"rotation must not extend the session beyond SESSION_MAX_LIFETIME")
			assert.InDelta(t, time.Until(deadline).Seconds(), float64(cookie.MaxAge), 3,
				"cookie Max-Age follows the capped expiry")
		})
	}
}

// ---- #446 + sid: claims of login and refreshed access tokens ----

func TestSessionClaims_LoginAndRefresh(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		provider     string
		email        string
		wantProvider string
	}{
		{name: "local user", provider: "local", email: "alice@example.com", wantProvider: "local"},
		{name: "user without provider is local", provider: "", email: "", wantProvider: "local"},
		{name: "SSO user keeps oidc after refresh", provider: "oidc", email: "sso@example.com", wantProvider: "oidc"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSessionTestEnv(t)
			env.seedUser(t, tt.provider, tt.email)

			access, raw0 := env.login(t)
			loginClaims, err := middleware.ValidateJWT(access, env.cfg.JWTSecret)
			require.NoError(t, err)
			first := env.tokenByRaw(t, raw0)
			require.NotEmpty(t, loginClaims.SessionID, "login token must carry sid")
			assert.Equal(t, first.ID, loginClaims.SessionID, "sid is the ID of the first token")
			assert.Equal(t, first.ID, first.FamilyID)
			assert.Equal(t, tt.wantProvider, loginClaims.AuthProvider)
			assert.Equal(t, tt.email, loginClaims.Email)
			assert.WithinDuration(t, first.SessionStartedAt.Add(env.cfg.SessionMaxLifetime), first.ExpiresAt, time.Second,
				"first token expires at the session deadline when it is earlier than REFRESH_TOKEN_EXPIRATION")

			w := env.postRefresh(raw0)
			require.Equal(t, http.StatusOK, w.Code)
			claims := env.claimsOf(t, w)
			assert.Equal(t, loginClaims.SessionID, claims.SessionID)
			assert.Equal(t, tt.wantProvider, claims.AuthProvider)
			assert.Equal(t, tt.email, claims.Email)
		})
	}
}

func TestSessionClaims_LoginWithoutRefreshTokensHasNoSID(t *testing.T) {
	t.Parallel()

	userRepo := NewMockUserRepository()
	seedUser(t, userRepo, "uid-1", "alice", "secret", "user")
	cfg := sessionTestConfig()
	h := NewAuthHandler(userRepo, cfg, &config.OIDCConfig{})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/login", h.Login)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/login", bytes.NewBufferString(`{"username":"alice","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp LoginResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	claims, err := middleware.ValidateJWT(resp.Token, cfg.JWTSecret)
	require.NoError(t, err)
	assert.Empty(t, claims.SessionID)
	assert.Equal(t, "local", claims.AuthProvider)
}

func TestGetCurrentUser_ReturnsAuthProvider(t *testing.T) {
	t.Parallel()

	env := newSessionTestEnv(t)
	env.seedUser(t, "oidc", "sso@example.com")
	access, _ := env.login(t)

	w := env.getMe(access)
	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "oidc", body["auth_provider"])
}

// The OIDC browser login starts a session: the access token carries sid, the
// refresh cookie belongs to that family, and a refresh keeps auth_provider
// "oidc" and the email.
func TestOIDCCallback_SessionClaims(t *testing.T) {
	t.Parallel()

	h, stateStore, userRepo := newOIDCHandlerSetup(t, false)
	cfg := sessionTestConfig()
	h.authCfg = cfg
	refreshRepo := NewMockRefreshTokenRepository()
	h.SetRefreshTokenRepo(refreshRepo)

	require.NoError(t, stateStore.SaveOIDCState(context.Background(), "state-sid", sessionstore.OIDCStateData{
		CodeVerifier: "test-verifier",
		RedirectURL:  "/",
	}, 5*time.Minute))

	r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=state-sid", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusFound, w.Code)

	location := w.Header().Get("Location")
	idx := strings.Index(location, "#")
	require.GreaterOrEqual(t, idx, 0)
	fragment, err := url.ParseQuery(location[idx+1:])
	require.NoError(t, err)
	loginClaims, err := middleware.ValidateJWT(fragment.Get("token"), cfg.JWTSecret)
	require.NoError(t, err)

	cookie := findRefreshCookie(w)
	require.NotNil(t, cookie)
	stored, err := refreshRepo.FindByTokenHash(hashRefreshToken(cookie.Value))
	require.NoError(t, err)
	assert.Equal(t, stored.FamilyID, loginClaims.SessionID)
	assert.Equal(t, "oidc", loginClaims.AuthProvider)

	// Refresh through the AuthHandler with the same repositories.
	ah := NewAuthHandler(userRepo, cfg, &config.OIDCConfig{})
	ah.SetRefreshTokenRepo(refreshRepo)
	rr := gin.New()
	rr.POST("/refresh", ah.Refresh)
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/refresh", nil)
	req2.AddCookie(&http.Cookie{Name: refreshTokenCookieName, Value: cookie.Value})
	rr.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code, w2.Body.String())

	var resp RefreshResponse
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp))
	claims, err := middleware.ValidateJWT(resp.Token, cfg.JWTSecret)
	require.NoError(t, err)
	assert.Equal(t, "oidc", claims.AuthProvider, "refresh must keep auth_provider of an SSO user")
	assert.Equal(t, loginClaims.Email, claims.Email)
	assert.NotEmpty(t, claims.Email)
	assert.Equal(t, loginClaims.SessionID, claims.SessionID)
}

// The long-lived CLI token is not a browser session token: it has no sid, and
// the CLI callback creates no refresh token and sets no cookie.
func TestOIDCCallback_CLITokenHasNoSID(t *testing.T) {
	t.Parallel()

	h, stateStore, _ := newOIDCHandlerSetup(t, false)
	cfg := sessionTestConfig()
	h.authCfg = cfg
	refreshRepo := NewMockRefreshTokenRepository()
	h.SetRefreshTokenRepo(refreshRepo)

	const cliSession = "cli-session-sid"
	require.NoError(t, stateStore.SaveOIDCState(context.Background(), "state-cli-sid", sessionstore.OIDCStateData{
		CodeVerifier: "test-verifier",
		RedirectURL:  "cli:" + cliSession,
	}, 5*time.Minute))
	require.NoError(t, stateStore.SaveCLIAuth(context.Background(), cliSession, sessionstore.CLIAuthData{Status: "pending"}, 10*time.Minute))

	r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=state-cli-sid", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	assert.Nil(t, findRefreshCookie(w), "a CLI login must not set a browser refresh cookie")
	refreshRepo.mu.RLock()
	assert.Empty(t, refreshRepo.tokens, "a CLI login must not create a refresh token")
	refreshRepo.mu.RUnlock()

	data, err := stateStore.GetCLIAuth(context.Background(), cliSession)
	require.NoError(t, err)
	assert.Equal(t, "completed", data.Status)
	require.NotEmpty(t, data.Token, "the CLI session gets its token")
	claims, err := middleware.ValidateJWT(data.Token, cfg.JWTSecret)
	require.NoError(t, err)
	assert.Empty(t, claims.SessionID)
	assert.Equal(t, "oidc", claims.AuthProvider)
}

// ---- revoke race: a revoke after the token is signed fails closed ----

// revokeAllBeforeCount revokes all refresh tokens of uid-1 (an admin revoke or a
// logout-all on another device) right before the given CountActiveInFamily
// call, that is between the handler step and its session re-check.
func (e *sessionTestEnv) revokeAllBeforeCount(callNo int) {
	e.refresh.mu.Lock()
	defer e.refresh.mu.Unlock()
	e.refresh.countCalls = 0
	e.refresh.countHook = func(call int) {
		if call == callNo {
			_ = e.refresh.RevokeAllForUser("uid-1")
		}
	}
}

func TestSessionRecheck_RevokeAfterSigningFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// run performs the request; the revoke hits the given count call.
		run       func(t *testing.T, env *sessionTestEnv) *httptest.ResponseRecorder
		countCall int
	}{
		{
			name: "rotation: revoke between the transaction and the re-check",
			run: func(t *testing.T, env *sessionTestEnv) *httptest.ResponseRecorder {
				raw := env.seedToken(t, "rt-rot", nil)
				return env.postRefresh(raw)
			},
			countCall: 1, // the rotation path counts only in the re-check
		},
		{
			name: "grace: revoke between the grace check and the re-check",
			run: func(t *testing.T, env *sessionTestEnv) *httptest.ResponseRecorder {
				rotated := time.Now().UTC().Add(-5 * time.Second)
				raw := env.seedToken(t, "rt-used", func(rt *models.RefreshToken) {
					rt.Revoked = true
					rt.RotatedAt = &rotated
				})
				env.seedToken(t, "rt-succ", nil)
				return env.postRefresh(raw)
			},
			countCall: 2, // call 1 is the grace eligibility check
		},
		{
			name: "login: revoke between the issue and the re-check",
			run: func(t *testing.T, env *sessionTestEnv) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login",
					bytes.NewBufferString(`{"username":"alice","password":"secret"}`))
				req.Header.Set("Content-Type", "application/json")
				env.router.ServeHTTP(w, req)
				return w
			},
			countCall: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSessionTestEnv(t)
			env.seedUser(t, "local", "")
			env.revokeAllBeforeCount(tt.countCall)

			w := tt.run(t, env)
			require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "Session revoked")
			assert.NotContains(t, w.Body.String(), `"token"`, "no access token may leave the server")
			cookie := findRefreshCookie(w)
			require.NotNil(t, cookie)
			assert.Less(t, cookie.MaxAge, 0, "the only refresh cookie must be the clearing one")
			assert.Len(t, w.Result().Cookies(), 1)
		})
	}
}

func TestOIDCCallback_RevokeAfterIssueFailsClosed(t *testing.T) {
	t.Parallel()

	h, stateStore, _ := newOIDCHandlerSetup(t, false)
	h.authCfg = sessionTestConfig()
	refreshRepo := NewMockRefreshTokenRepository()
	refreshRepo.countHook = func(call int) {
		if call == 1 {
			refreshRepo.mu.Lock()
			for _, tok := range refreshRepo.tokens {
				tok.Revoked = true
			}
			refreshRepo.mu.Unlock()
		}
	}
	h.SetRefreshTokenRepo(refreshRepo)

	require.NoError(t, stateStore.SaveOIDCState(context.Background(), "state-revoke", sessionstore.OIDCStateData{
		CodeVerifier: "test-verifier",
		RedirectURL:  "/",
	}, 5*time.Minute))

	r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=state-revoke", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusFound, w.Code)
	location := w.Header().Get("Location")
	assert.Equal(t, "/login?error=auth_failed", location)
	assert.NotContains(t, location, "token=")
	cookie := findRefreshCookie(w)
	require.NotNil(t, cookie)
	assert.Less(t, cookie.MaxAge, 0, "only the clearing cookie after a revoked session")
	assert.Len(t, w.Result().Cookies(), 1)
}

// authStatusCount sums an auth counter for the given attribute values.
func authStatusCount(t *testing.T, reader sdkmetric.Reader, name string, want map[string]string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok)
		points:
			for _, dp := range sum.DataPoints {
				for k, v := range want {
					got, _ := dp.Attributes.Value(attribute.Key(k))
					if got.AsString() != v {
						continue points
					}
				}
				total += dp.Value
			}
		}
	}
	return total
}

// A repository error in the session re-check fails closed: 500 (or an
// auth_failed redirect), "failure" metric, the new family is revoked and no
// new refresh cookie is set. Not parallel: it swaps the global meter provider.
func TestSessionRecheck_RepositoryError(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	middleware.RebindAuthMeter()
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		middleware.RebindAuthMeter()
		_ = mp.Shutdown(context.Background())
	})
	errCount := errors.New("count failed")

	t.Run("local login", func(t *testing.T) {
		env := newSessionTestEnv(t)
		env.seedUser(t, "local", "")
		env.refresh.countErr = errCount

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login",
			bytes.NewBufferString(`{"username":"alice","password":"secret"}`))
		req.Header.Set("Content-Type", "application/json")
		env.router.ServeHTTP(w, req)

		require.Equal(t, http.StatusInternalServerError, w.Code)
		assert.NotContains(t, w.Body.String(), `"token"`)
		cookie := findRefreshCookie(w)
		require.NotNil(t, cookie)
		assert.Less(t, cookie.MaxAge, 0, "only the clearing cookie")
		assert.Len(t, w.Result().Cookies(), 1)
		active, _ := env.refresh.CountActiveForUser("uid-1")
		assert.Equal(t, int64(0), active, "the new family must be revoked")
		assert.Equal(t, int64(1), authStatusCount(t, reader, "auth.login.total",
			map[string]string{"method": "local", "status": "failure"}))
		assert.Equal(t, int64(0), authStatusCount(t, reader, "auth.login.total",
			map[string]string{"method": "local", "status": "success"}))
	})

	t.Run("refresh", func(t *testing.T) {
		env := newSessionTestEnv(t)
		env.seedUser(t, "local", "")
		raw := env.seedToken(t, "rt-err", nil)
		env.refresh.countErr = errCount

		w := env.postRefresh(raw)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		assert.NotContains(t, w.Body.String(), `"token"`)
		cookie := findRefreshCookie(w)
		require.NotNil(t, cookie)
		assert.Less(t, cookie.MaxAge, 0, "no new refresh cookie")
		assert.Len(t, w.Result().Cookies(), 1)
		assert.Equal(t, int64(1), authStatusCount(t, reader, "auth.token.refresh.total",
			map[string]string{"status": "failure"}))
	})

	t.Run("OIDC browser login", func(t *testing.T) {
		h, stateStore, _ := newOIDCHandlerSetup(t, false)
		h.authCfg = sessionTestConfig()
		refreshRepo := NewMockRefreshTokenRepository()
		refreshRepo.countErr = errCount
		h.SetRefreshTokenRepo(refreshRepo)
		require.NoError(t, stateStore.SaveOIDCState(context.Background(), "state-count-err", sessionstore.OIDCStateData{
			CodeVerifier: "test-verifier",
			RedirectURL:  "/",
		}, 5*time.Minute))

		r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=state-count-err", nil)
		r.ServeHTTP(w, req)

		require.Equal(t, http.StatusFound, w.Code)
		assert.Equal(t, "/login?error=auth_failed", w.Header().Get("Location"))
		cookie := findRefreshCookie(w)
		require.NotNil(t, cookie)
		assert.Less(t, cookie.MaxAge, 0, "OIDC clears the refresh cookie like local login")
		assert.Len(t, w.Result().Cookies(), 1)
		refreshRepo.mu.RLock()
		for _, tok := range refreshRepo.tokens {
			assert.True(t, tok.Revoked, "the new family must be revoked")
		}
		assert.Len(t, refreshRepo.tokens, 1)
		refreshRepo.mu.RUnlock()
		assert.Equal(t, int64(1), authStatusCount(t, reader, "auth.login.total",
			map[string]string{"method": "oidc", "status": "failure"}))
	})
}
