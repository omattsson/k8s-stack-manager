package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
)

// ---- MockRefreshTokenRepository ----

type MockRefreshTokenRepository struct {
	mu           sync.RWMutex
	tokens       map[string]*models.RefreshToken // by ID
	byHash       map[string]*models.RefreshToken // by TokenHash
	createErr    error
	findErr      error
	revokeErr    error
	revokeAllErr error
	deleteErr    error
}

func NewMockRefreshTokenRepository() *MockRefreshTokenRepository {
	return &MockRefreshTokenRepository{
		tokens: make(map[string]*models.RefreshToken),
		byHash: make(map[string]*models.RefreshToken),
	}
}

func (m *MockRefreshTokenRepository) Create(token *models.RefreshToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	m.tokens[token.ID] = token
	m.byHash[token.TokenHash] = token
	return nil
}

func (m *MockRefreshTokenRepository) FindByTokenHash(hash string) (*models.RefreshToken, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.findErr != nil {
		return nil, m.findErr
	}
	t, ok := m.byHash[hash]
	if !ok {
		return nil, errors.New("record not found")
	}
	cp := *t
	return &cp, nil
}

func (m *MockRefreshTokenRepository) RevokeByID(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.revokeErr != nil {
		return m.revokeErr
	}
	if t, ok := m.tokens[id]; ok {
		t.Revoked = true
	}
	return nil
}

func (m *MockRefreshTokenRepository) RevokeByIDIfActive(id string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.revokeErr != nil {
		return 0, m.revokeErr
	}
	if t, ok := m.tokens[id]; ok && !t.Revoked {
		t.Revoked = true
		return 1, nil
	}
	return 0, nil
}

func (m *MockRefreshTokenRepository) RevokeAllForUser(userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.revokeAllErr != nil {
		return m.revokeAllErr
	}
	for _, t := range m.tokens {
		if t.UserID == userID {
			t.Revoked = true
		}
	}
	return nil
}

func (m *MockRefreshTokenRepository) RevokeAllForUserExcept(userID string, excludeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.revokeAllErr != nil {
		return m.revokeAllErr
	}
	for _, t := range m.tokens {
		if t.UserID == userID && t.ID != excludeID {
			t.Revoked = true
		}
	}
	return nil
}

func (m *MockRefreshTokenRepository) DeleteExpired() (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return 0, m.deleteErr
	}
	var count int64
	now := time.Now()
	for id, t := range m.tokens {
		if now.After(t.ExpiresAt) {
			delete(m.byHash, t.TokenHash)
			delete(m.tokens, id)
			count++
		}
	}
	return count, nil
}

func (m *MockRefreshTokenRepository) CountActiveForUser(userID string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var count int64
	now := time.Now()
	for _, t := range m.tokens {
		if t.UserID == userID && !t.Revoked && t.ExpiresAt.After(now) {
			count++
		}
	}
	return count, nil
}

func (m *MockRefreshTokenRepository) WithTx(fn func(models.RefreshTokenRepository) error) error {
	// In tests, just run the function with the same mock (no real transaction).
	return fn(m)
}

func (m *MockRefreshTokenRepository) SetCreateError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createErr = err
}

func (m *MockRefreshTokenRepository) SetFindError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.findErr = err
}

func (m *MockRefreshTokenRepository) SetRevokeError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revokeErr = err
}

func (m *MockRefreshTokenRepository) SetRevokeAllError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revokeAllErr = err
}

func (m *MockRefreshTokenRepository) SetDeleteError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteErr = err
}

// testAuthConfigWithRefresh returns an AuthConfig suitable for refresh token testing.
func testAuthConfigWithRefresh() *config.AuthConfig {
	return &config.AuthConfig{
		JWTSecret:               "test-secret-key-long-enough",
		JWTExpiration:           time.Hour,
		AccessTokenExpiration:   15 * time.Minute,
		RefreshTokenExpiration:  7 * 24 * time.Hour,
		SessionIdleTimeout:      30 * time.Minute,
		MaxRefreshTokensPerUser: 10,
		SecureCookies:           false,
	}
}

// setupRefreshAuthRouter creates a gin engine with auth + refresh routes.
func setupRefreshAuthRouter(userRepo *MockUserRepository, refreshRepo *MockRefreshTokenRepository, store sessionstore.SessionStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	cfg := testAuthConfigWithRefresh()
	h := NewAuthHandler(userRepo, cfg, &config.OIDCConfig{})
	h.SetRefreshTokenRepo(refreshRepo)
	if store != nil {
		h.SetSessionStore(store)
	}

	auth := r.Group("/api/v1/auth")
	{
		auth.POST("/login", h.Login)
		auth.POST("/refresh", h.Refresh)
		// logout is public (no auth middleware) — matches production config.
		auth.POST("/logout", h.Logout)
		// logout-all requires auth context.
		auth.POST("/logout-all", injectAuthContext("uid-1", "user"), h.LogoutAll)
	}
	return r
}

// ---- Login + Refresh Token Cookie Tests ----

func TestLoginSetsRefreshCookie(t *testing.T) {
	t.Parallel()

	userRepo := NewMockUserRepository()
	refreshRepo := NewMockRefreshTokenRepository()
	seedUser(t, userRepo, "uid-1", "alice", "secret", "user")

	router := setupRefreshAuthRouter(userRepo, refreshRepo, nil)

	w := httptest.NewRecorder()
	body := `{"username":"alice","password":"secret"}`
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Check that a refresh token cookie was set.
	cookies := w.Result().Cookies()
	var refreshCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "refresh_token" {
			refreshCookie = c
			break
		}
	}
	require.NotNil(t, refreshCookie, "refresh_token cookie should be set on login")
	assert.True(t, refreshCookie.HttpOnly, "cookie must be httpOnly")
	assert.Equal(t, "/api/v1/auth", refreshCookie.Path)
	assert.NotEmpty(t, refreshCookie.Value)

	// Verify a token was stored in the repository.
	var storedCount int64
	storedCount, err := refreshRepo.CountActiveForUser("uid-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), storedCount)
}

// ---- Refresh Endpoint Tests ----

func TestRefresh(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(*testing.T, *MockUserRepository, *MockRefreshTokenRepository) string // returns raw cookie value
		wantStatus int
		wantToken  bool
		verify     func(*testing.T, *httptest.ResponseRecorder, *MockRefreshTokenRepository)
	}{
		{
			name: "valid refresh token returns new access token",
			setup: func(t *testing.T, ur *MockUserRepository, rr *MockRefreshTokenRepository) string {
				seedUser(t, ur, "uid-1", "alice", "secret", "user")
				raw := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
				hash := hashRefreshToken(raw)
				now := time.Now()
				_ = rr.Create(&models.RefreshToken{
					ID:           "rt-1",
					UserID:       "uid-1",
					TokenHash:    hash,
					ExpiresAt:    now.Add(7 * 24 * time.Hour),
					LastActivity: now,
					CreatedAt:    now,
				})
				return raw
			},
			wantStatus: http.StatusOK,
			wantToken:  true,
		},
		{
			name: "no cookie returns 401",
			setup: func(_ *testing.T, _ *MockUserRepository, _ *MockRefreshTokenRepository) string {
				return "" // no cookie
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "unknown token returns 401",
			setup: func(_ *testing.T, _ *MockUserRepository, _ *MockRefreshTokenRepository) string {
				return "unknown-token-value"
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "database error on lookup returns 500",
			setup: func(_ *testing.T, _ *MockUserRepository, rr *MockRefreshTokenRepository) string {
				rr.findErr = errors.New("unexpected db failure")
				return "some-token-value"
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "revoked token returns 401 and revokes all for user",
			setup: func(t *testing.T, ur *MockUserRepository, rr *MockRefreshTokenRepository) string {
				seedUser(t, ur, "uid-1", "alice", "secret", "user")
				raw := "revoked0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
				hash := hashRefreshToken(raw)
				now := time.Now()
				_ = rr.Create(&models.RefreshToken{
					ID:           "rt-revoked",
					UserID:       "uid-1",
					TokenHash:    hash,
					ExpiresAt:    now.Add(7 * 24 * time.Hour),
					LastActivity: now,
					CreatedAt:    now,
					Revoked:      true,
				})
				return raw
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "expired token returns 401",
			setup: func(t *testing.T, ur *MockUserRepository, rr *MockRefreshTokenRepository) string {
				seedUser(t, ur, "uid-1", "alice", "secret", "user")
				raw := "expired00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
				hash := hashRefreshToken(raw)
				now := time.Now()
				_ = rr.Create(&models.RefreshToken{
					ID:           "rt-expired",
					UserID:       "uid-1",
					TokenHash:    hash,
					ExpiresAt:    now.Add(-time.Hour), // expired
					LastActivity: now,
					CreatedAt:    now,
				})
				return raw
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "idle timeout exceeded returns 401",
			setup: func(t *testing.T, ur *MockUserRepository, rr *MockRefreshTokenRepository) string {
				seedUser(t, ur, "uid-1", "alice", "secret", "user")
				raw := "idletimeout0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
				hash := hashRefreshToken(raw)
				now := time.Now()
				_ = rr.Create(&models.RefreshToken{
					ID:           "rt-idle",
					UserID:       "uid-1",
					TokenHash:    hash,
					ExpiresAt:    now.Add(7 * 24 * time.Hour),
					LastActivity: now.Add(-time.Hour), // 60min ago > 30min idle timeout
					CreatedAt:    now,
				})
				return raw
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "disabled user gets 403 and all tokens revoked",
			setup: func(t *testing.T, ur *MockUserRepository, rr *MockRefreshTokenRepository) string {
				u := seedUser(t, ur, "uid-dis", "disabled-alice", "secret", "user")
				u.Disabled = true
				_ = ur.Update(u)
				raw := "disabled0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
				hash := hashRefreshToken(raw)
				now := time.Now()
				_ = rr.Create(&models.RefreshToken{
					ID:           "rt-disabled",
					UserID:       "uid-dis",
					TokenHash:    hash,
					ExpiresAt:    now.Add(7 * 24 * time.Hour),
					LastActivity: now,
					CreatedAt:    now,
				})
				return raw
			},
			wantStatus: http.StatusForbidden,
			verify: func(t *testing.T, w *httptest.ResponseRecorder, rr *MockRefreshTokenRepository) {
				// All refresh tokens for the disabled user must be revoked.
				rr.mu.RLock()
				for _, tok := range rr.tokens {
					if tok.UserID == "uid-dis" {
						assert.True(t, tok.Revoked, "refresh token for disabled user should be revoked")
					}
				}
				rr.mu.RUnlock()
				// Refresh cookie must be cleared (MaxAge < 0).
				for _, c := range w.Result().Cookies() {
					if c.Name == "refresh_token" {
						assert.Less(t, c.MaxAge, 0, "refresh_token cookie should be cleared")
					}
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			userRepo := NewMockUserRepository()
			refreshRepo := NewMockRefreshTokenRepository()
			rawCookie := tt.setup(t, userRepo, refreshRepo)

			router := setupRefreshAuthRouter(userRepo, refreshRepo, nil)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
			if rawCookie != "" {
				req.AddCookie(&http.Cookie{
					Name:  "refresh_token",
					Value: rawCookie,
				})
			}
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.verify != nil {
				tt.verify(t, w, refreshRepo)
			}
			if tt.wantToken {
				var resp RefreshResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.NotEmpty(t, resp.Token)

				// Verify old token was revoked (rotation).
				refreshRepo.mu.RLock()
				for _, tok := range refreshRepo.tokens {
					if tok.ID == "rt-1" {
						assert.True(t, tok.Revoked, "old refresh token should be revoked after rotation")
					}
				}
				refreshRepo.mu.RUnlock()

				// Verify new refresh cookie was set.
				cookies := w.Result().Cookies()
				var newCookie *http.Cookie
				for _, c := range cookies {
					if c.Name == "refresh_token" {
						newCookie = c
						break
					}
				}
				require.NotNil(t, newCookie, "new refresh_token cookie should be set")
			}
		})
	}
}

// ---- Logout Tests ----

func TestLogout(t *testing.T) {
	t.Parallel()

	userRepo := NewMockUserRepository()
	refreshRepo := NewMockRefreshTokenRepository()
	store := sessionstore.NewMemoryStore()
	defer store.Stop()

	seedUser(t, userRepo, "uid-1", "alice", "secret", "user")

	// Create a refresh token.
	raw := "logout000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
	hash := hashRefreshToken(raw)
	now := time.Now()
	_ = refreshRepo.Create(&models.RefreshToken{
		ID:           "rt-logout",
		UserID:       "uid-1",
		TokenHash:    hash,
		ExpiresAt:    now.Add(7 * 24 * time.Hour),
		LastActivity: now,
		CreatedAt:    now,
	})

	router := setupRefreshAuthRouter(userRepo, refreshRepo, store)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: raw})
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify refresh token was revoked.
	refreshRepo.mu.RLock()
	rt := refreshRepo.tokens["rt-logout"]
	refreshRepo.mu.RUnlock()
	assert.True(t, rt.Revoked, "refresh token should be revoked after logout")

	// Verify the cookie was cleared.
	cookies := w.Result().Cookies()
	var cleared bool
	for _, c := range cookies {
		if c.Name == "refresh_token" && c.MaxAge < 0 {
			cleared = true
			break
		}
	}
	assert.True(t, cleared, "refresh_token cookie should be cleared on logout")
}

// ---- LogoutAll Tests ----

func TestLogoutAll(t *testing.T) {
	t.Parallel()

	userRepo := NewMockUserRepository()
	refreshRepo := NewMockRefreshTokenRepository()
	store := sessionstore.NewMemoryStore()
	defer store.Stop()

	seedUser(t, userRepo, "uid-1", "alice", "secret", "user")

	// Create multiple refresh tokens.
	now := time.Now()
	for i, raw := range []string{
		"logoutall_token1_0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
		"logoutall_token2_0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
	} {
		_ = refreshRepo.Create(&models.RefreshToken{
			ID:           "rt-all-" + string(rune('0'+i)),
			UserID:       "uid-1",
			TokenHash:    hashRefreshToken(raw),
			ExpiresAt:    now.Add(7 * 24 * time.Hour),
			LastActivity: now,
			CreatedAt:    now,
		})
	}

	router := setupRefreshAuthRouter(userRepo, refreshRepo, store)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/logout-all", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify all refresh tokens were revoked.
	refreshRepo.mu.RLock()
	for _, rt := range refreshRepo.tokens {
		if rt.UserID == "uid-1" {
			assert.True(t, rt.Revoked, "all refresh tokens for user should be revoked")
		}
	}
	refreshRepo.mu.RUnlock()
}

// ---- Refresh without repository configured returns 501 ----

func TestRefreshNotEnabled(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	cfg := testAuthConfigWithRefresh()
	h := NewAuthHandler(NewMockUserRepository(), cfg, &config.OIDCConfig{})
	// NOT setting refresh token repo

	r.POST("/api/v1/auth/refresh", h.Refresh)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "some-value"})
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotImplemented, w.Code)
}

// ---- CleanupExpiredTokens Tests ----

func TestCleanupExpiredTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		hasRepo        bool
		seedExpired    int
		deleteErr      error
		wantDeleteCall bool
	}{
		{
			name:           "no repo configured — no-op",
			hasRepo:        false,
			wantDeleteCall: false,
		},
		{
			name:           "no expired tokens",
			hasRepo:        true,
			seedExpired:    0,
			wantDeleteCall: true,
		},
		{
			name:           "deletes expired tokens",
			hasRepo:        true,
			seedExpired:    3,
			wantDeleteCall: true,
		},
		{
			name:           "DeleteExpired returns error — logs and returns",
			hasRepo:        true,
			deleteErr:      errors.New("db failure"),
			wantDeleteCall: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := testAuthConfigWithRefresh()
			h := NewAuthHandler(NewMockUserRepository(), cfg, &config.OIDCConfig{})

			var refreshRepo *MockRefreshTokenRepository
			if tt.hasRepo {
				refreshRepo = NewMockRefreshTokenRepository()
				if tt.deleteErr != nil {
					refreshRepo.SetDeleteError(tt.deleteErr)
				}
				// Seed expired tokens.
				for i := 0; i < tt.seedExpired; i++ {
					_ = refreshRepo.Create(&models.RefreshToken{
						ID:        fmt.Sprintf("rt-expired-%d", i),
						UserID:    "uid-1",
						TokenHash: fmt.Sprintf("hash-expired-%d", i),
						ExpiresAt: time.Now().Add(-time.Hour), // already expired
						CreatedAt: time.Now().Add(-2 * time.Hour),
					})
				}
				h.SetRefreshTokenRepo(refreshRepo)
			}

			// Should not panic regardless of configuration.
			h.CleanupExpiredTokens()

			if tt.hasRepo && tt.deleteErr == nil && tt.seedExpired > 0 {
				// Verify tokens were actually removed from the store.
				refreshRepo.mu.RLock()
				remaining := len(refreshRepo.tokens)
				refreshRepo.mu.RUnlock()
				assert.Equal(t, 0, remaining, "expired tokens should have been deleted")
			}
		})
	}
}

// ---- Logout with access token blocklisting Tests ----

func TestLogout_BlocklistsAccessToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		withBearerToken   bool
		withSessionStore  bool
		withRefreshCookie bool
		wantTokenBlocked  bool
		wantRefreshRevoke bool
	}{
		{
			name:              "full logout — blocklists access token and revokes refresh token",
			withBearerToken:   true,
			withSessionStore:  true,
			withRefreshCookie: true,
			wantTokenBlocked:  true,
			wantRefreshRevoke: true,
		},
		{
			name:              "logout with bearer token only — no refresh cookie",
			withBearerToken:   true,
			withSessionStore:  true,
			withRefreshCookie: false,
			wantTokenBlocked:  true,
			wantRefreshRevoke: false,
		},
		{
			name:              "logout without session store — skips blocklist",
			withBearerToken:   true,
			withSessionStore:  false,
			withRefreshCookie: true,
			wantTokenBlocked:  false,
			wantRefreshRevoke: true,
		},
		{
			name:              "logout without bearer token — no blocklist",
			withBearerToken:   false,
			withSessionStore:  true,
			withRefreshCookie: true,
			wantTokenBlocked:  false,
			wantRefreshRevoke: true,
		},
		{
			name:              "minimal logout — no store, no cookie, no bearer",
			withBearerToken:   false,
			withSessionStore:  false,
			withRefreshCookie: false,
			wantTokenBlocked:  false,
			wantRefreshRevoke: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			userRepo := NewMockUserRepository()
			refreshRepo := NewMockRefreshTokenRepository()
			seedUser(t, userRepo, "uid-1", "alice", "secret", "user")

			var store *sessionstore.MemoryStore
			if tt.withSessionStore {
				store = sessionstore.NewMemoryStore()
				defer store.Stop()
			}

			cfg := testAuthConfigWithRefresh()

			// Set up refresh token if needed.
			var refreshCookieValue string
			if tt.withRefreshCookie {
				raw := "logoutblock00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
				hash := hashRefreshToken(raw)
				now := time.Now()
				_ = refreshRepo.Create(&models.RefreshToken{
					ID:           "rt-block-test",
					UserID:       "uid-1",
					TokenHash:    hash,
					ExpiresAt:    now.Add(7 * 24 * time.Hour),
					LastActivity: now,
					CreatedAt:    now,
				})
				refreshCookieValue = raw
			}

			// Build router with or without session store.
			gin.SetMode(gin.TestMode)
			r := gin.New()
			h := NewAuthHandler(userRepo, cfg, &config.OIDCConfig{})
			h.SetRefreshTokenRepo(refreshRepo)
			if store != nil {
				h.SetSessionStore(store)
			}
			r.POST("/api/v1/auth/logout", h.Logout)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)

			// Generate a real JWT so Logout can parse and blocklist it.
			var jti string
			if tt.withBearerToken {
				token, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
					UserID:     "uid-1",
					Username:   "alice",
					Role:       "user",
					Secret:     cfg.JWTSecret,
					Expiration: cfg.AccessTokenExpiration,
				})
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer "+token)

				// Parse back to get the JTI.
				claims, err := middleware.ValidateJWT(token, cfg.JWTSecret)
				require.NoError(t, err)
				jti = claims.ID
			}

			if refreshCookieValue != "" {
				req.AddCookie(&http.Cookie{Name: "refresh_token", Value: refreshCookieValue})
			}

			r.ServeHTTP(w, req)
			assert.Equal(t, http.StatusOK, w.Code)

			// Verify access token blocklist.
			if tt.wantTokenBlocked && store != nil {
				blocked, err := store.IsTokenBlocked(req.Context(), jti)
				require.NoError(t, err)
				assert.True(t, blocked, "access token JTI should be blocklisted after logout")
			}

			// Verify refresh token revocation.
			if tt.wantRefreshRevoke {
				refreshRepo.mu.RLock()
				rt := refreshRepo.tokens["rt-block-test"]
				refreshRepo.mu.RUnlock()
				require.NotNil(t, rt)
				assert.True(t, rt.Revoked, "refresh token should be revoked after logout")
			}

			// Verify refresh cookie is always cleared.
			var cookieCleared bool
			for _, c := range w.Result().Cookies() {
				if c.Name == "refresh_token" && c.MaxAge < 0 {
					cookieCleared = true
					break
				}
			}
			assert.True(t, cookieCleared, "refresh_token cookie should always be cleared on logout")
		})
	}
}

// ---- LogoutAll with session store blocklisting Tests ----

func TestLogoutAll_BlocklistsAccessToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		callerUserID     string
		callerJTI        string
		callerExpiry     *time.Time
		withSessionStore bool
		withRefreshRepo  bool
		revokeAllErr     error
		wantStatus       int
		wantTokenBlocked bool
	}{
		{
			name:             "blocklists access token with JTI and expiry from context",
			callerUserID:     "uid-1",
			callerJTI:        "jti-abc",
			callerExpiry:     timePtr(time.Now().Add(15 * time.Minute)),
			withSessionStore: true,
			withRefreshRepo:  true,
			wantStatus:       http.StatusOK,
			wantTokenBlocked: true,
		},
		{
			name:             "blocklists access token — no expiry in context uses default",
			callerUserID:     "uid-1",
			callerJTI:        "jti-def",
			callerExpiry:     nil, // no expiry
			withSessionStore: true,
			withRefreshRepo:  true,
			wantStatus:       http.StatusOK,
			wantTokenBlocked: true,
		},
		{
			name:             "no session store — skips blocklist",
			callerUserID:     "uid-1",
			callerJTI:        "jti-ghi",
			withSessionStore: false,
			withRefreshRepo:  true,
			wantStatus:       http.StatusOK,
			wantTokenBlocked: false,
		},
		{
			name:             "no JTI in context — skips blocklist",
			callerUserID:     "uid-1",
			callerJTI:        "",
			withSessionStore: true,
			withRefreshRepo:  true,
			wantStatus:       http.StatusOK,
			wantTokenBlocked: false,
		},
		{
			name:             "no refresh repo — still clears cookie and succeeds",
			callerUserID:     "uid-1",
			callerJTI:        "jti-norepo",
			withSessionStore: true,
			withRefreshRepo:  false,
			wantStatus:       http.StatusOK,
			wantTokenBlocked: true,
		},
		{
			name:             "RevokeAllForUser error returns 500",
			callerUserID:     "uid-1",
			callerJTI:        "jti-err",
			withSessionStore: true,
			withRefreshRepo:  true,
			revokeAllErr:     errors.New("db failure"),
			wantStatus:       http.StatusInternalServerError,
			wantTokenBlocked: true, // blocklist happens before revoke
		},
		{
			name:         "no userID in context returns 401",
			callerUserID: "",
			wantStatus:   http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			userRepo := NewMockUserRepository()
			cfg := testAuthConfigWithRefresh()

			var store *sessionstore.MemoryStore
			if tt.withSessionStore {
				store = sessionstore.NewMemoryStore()
				defer store.Stop()
			}

			gin.SetMode(gin.TestMode)
			r := gin.New()

			// Inject auth context to simulate what AuthRequired middleware does.
			r.Use(func(c *gin.Context) {
				if tt.callerUserID != "" {
					c.Set("userID", tt.callerUserID)
				}
				if tt.callerJTI != "" {
					c.Set("jti", tt.callerJTI)
				}
				if tt.callerExpiry != nil {
					c.Set("tokenExpiry", *tt.callerExpiry)
				}
				c.Next()
			})

			h := NewAuthHandler(userRepo, cfg, &config.OIDCConfig{})
			if store != nil {
				h.SetSessionStore(store)
			}
			if tt.withRefreshRepo {
				refreshRepo := NewMockRefreshTokenRepository()
				if tt.revokeAllErr != nil {
					refreshRepo.SetRevokeAllError(tt.revokeAllErr)
				}
				// Seed a token for the user so RevokeAllForUser has something to do.
				now := time.Now()
				_ = refreshRepo.Create(&models.RefreshToken{
					ID:           "rt-logoutall-test",
					UserID:       "uid-1",
					TokenHash:    "somehash",
					ExpiresAt:    now.Add(7 * 24 * time.Hour),
					LastActivity: now,
					CreatedAt:    now,
				})
				h.SetRefreshTokenRepo(refreshRepo)
			}

			r.POST("/api/v1/auth/logout-all", h.LogoutAll)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/logout-all", nil)
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			if tt.wantTokenBlocked && store != nil && tt.callerJTI != "" {
				blocked, err := store.IsTokenBlocked(req.Context(), tt.callerJTI)
				require.NoError(t, err)
				assert.True(t, blocked, "access token JTI should be blocklisted")
			}

			// Verify cookie is cleared on success.
			if tt.wantStatus == http.StatusOK {
				var cookieCleared bool
				for _, c := range w.Result().Cookies() {
					if c.Name == "refresh_token" && c.MaxAge < 0 {
						cookieCleared = true
						break
					}
				}
				assert.True(t, cookieCleared, "refresh_token cookie should be cleared")
			}
		})
	}
}

// ---- issueRefreshTokenWith max-tokens enforcement Tests ----

func TestIssueRefreshToken_MaxTokensEnforcement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		maxTokens      int
		existingTokens int
		excludeTokenID string
		wantRevokeAll  bool
		wantExcept     bool
	}{
		{
			name:           "under limit — no revocation",
			maxTokens:      5,
			existingTokens: 3,
		},
		{
			name:           "at limit without excludeTokenID — RevokeAllForUser",
			maxTokens:      3,
			existingTokens: 3,
			wantRevokeAll:  true,
		},
		{
			name:           "at limit with excludeTokenID — RevokeAllForUserExcept",
			maxTokens:      3,
			existingTokens: 3,
			excludeTokenID: "rt-exclude-me",
			wantRevokeAll:  true,
			wantExcept:     true,
		},
		{
			name:           "over limit without excludeTokenID",
			maxTokens:      2,
			existingTokens: 5,
			wantRevokeAll:  true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			userRepo := NewMockUserRepository()
			refreshRepo := NewMockRefreshTokenRepository()
			seedUser(t, userRepo, "uid-1", "alice", "secret", "user")

			cfg := testAuthConfigWithRefresh()
			cfg.MaxRefreshTokensPerUser = tt.maxTokens

			h := NewAuthHandler(userRepo, cfg, &config.OIDCConfig{})
			h.SetRefreshTokenRepo(refreshRepo)

			// Seed existing active tokens.
			now := time.Now()
			for i := 0; i < tt.existingTokens; i++ {
				id := fmt.Sprintf("rt-existing-%d", i)
				_ = refreshRepo.Create(&models.RefreshToken{
					ID:           id,
					UserID:       "uid-1",
					TokenHash:    fmt.Sprintf("hash-%d", i),
					ExpiresAt:    now.Add(7 * 24 * time.Hour),
					LastActivity: now,
					CreatedAt:    now,
				})
			}
			// If testing excludeTokenID, seed that specific token too.
			if tt.excludeTokenID != "" {
				_ = refreshRepo.Create(&models.RefreshToken{
					ID:           tt.excludeTokenID,
					UserID:       "uid-1",
					TokenHash:    "hash-exclude",
					ExpiresAt:    now.Add(7 * 24 * time.Hour),
					LastActivity: now,
					CreatedAt:    now,
				})
			}

			// Build a gin context for the handler.
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request, _ = http.NewRequest(http.MethodPost, "/", nil)
			c.Request.Header.Set("User-Agent", "test-agent")

			var rawToken string
			var err error
			if tt.excludeTokenID != "" {
				rawToken, err = h.issueRefreshTokenWith(c, refreshRepo, "uid-1", tt.excludeTokenID)
			} else {
				rawToken, err = h.issueRefreshTokenWith(c, refreshRepo, "uid-1")
			}

			require.NoError(t, err)
			assert.NotEmpty(t, rawToken, "should return a raw token")

			refreshRepo.mu.RLock()
			defer refreshRepo.mu.RUnlock()

			if tt.wantRevokeAll {
				if tt.wantExcept {
					// The exclude token should NOT be revoked, others should be.
					excludeToken := refreshRepo.tokens[tt.excludeTokenID]
					require.NotNil(t, excludeToken)
					assert.False(t, excludeToken.Revoked, "excluded token should not be revoked")

					// Other pre-existing tokens should be revoked.
					for _, tok := range refreshRepo.tokens {
						if tok.UserID == "uid-1" && tok.ID != tt.excludeTokenID && tok.TokenHash != hashRefreshToken(rawToken) {
							assert.True(t, tok.Revoked, "token %s should be revoked", tok.ID)
						}
					}
				} else {
					// All pre-existing tokens should be revoked.
					for _, tok := range refreshRepo.tokens {
						if tok.UserID == "uid-1" && tok.TokenHash != hashRefreshToken(rawToken) {
							assert.True(t, tok.Revoked, "token %s should be revoked", tok.ID)
						}
					}
				}
			} else {
				// No revocation expected — all existing tokens should still be active.
				for _, tok := range refreshRepo.tokens {
					if tok.TokenHash != hashRefreshToken(rawToken) {
						assert.False(t, tok.Revoked, "token %s should not be revoked (under limit)", tok.ID)
					}
				}
			}
		})
	}
}

// ---- EnsureAdminUser additional coverage Tests ----

func TestEnsureAdminUser_CreateError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		adminUsername string
		adminPassword string
		seedExisting  bool
		createErr     error
		wantCreated   bool
	}{
		{
			name:          "creates admin when not present",
			adminUsername: "admin",
			adminPassword: "admin-pass",
			wantCreated:   true,
		},
		{
			name:          "skips when admin already exists",
			adminUsername: "admin",
			adminPassword: "admin-pass",
			seedExisting:  true,
			wantCreated:   false,
		},
		{
			name:          "skips when admin username is empty",
			adminUsername: "",
			adminPassword: "admin-pass",
			wantCreated:   false,
		},
		{
			name:          "skips when admin password is empty",
			adminUsername: "admin",
			adminPassword: "",
			wantCreated:   false,
		},
		{
			name:          "repo.Create error — logs and returns",
			adminUsername: "admin",
			adminPassword: "admin-pass",
			createErr:     errors.New("db write failed"),
			wantCreated:   false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := NewMockUserRepository()
			if tt.seedExisting {
				seedUser(t, repo, "existing-admin-id", tt.adminUsername, "old-pass", "admin")
			}
			if tt.createErr != nil {
				repo.SetCreateError(tt.createErr)
			}

			cfg := &config.AuthConfig{
				JWTSecret:     testJWTSecret,
				JWTExpiration: time.Hour,
				AdminUsername: tt.adminUsername,
				AdminPassword: tt.adminPassword,
			}
			h := NewAuthHandler(repo, cfg, &config.OIDCConfig{})

			// Should not panic.
			h.EnsureAdminUser()

			// Check if user was created.
			user, err := repo.FindByUsername(tt.adminUsername)
			if tt.wantCreated {
				require.NoError(t, err)
				assert.Equal(t, "admin", user.Role)
				assert.Equal(t, "Administrator", user.DisplayName)
				assert.True(t, user.ServiceAccount)
			} else if tt.adminUsername != "" && !tt.seedExisting {
				// Should not exist if createErr prevented creation.
				if tt.createErr != nil {
					assert.Error(t, err, "user should not have been created when repo.Create fails")
				}
			}
		})
	}
}

// timePtr is a helper to create a *time.Time for table-driven tests.
func timePtr(t time.Time) *time.Time {
	return &t
}
