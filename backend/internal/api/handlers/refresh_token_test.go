package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/config"
	"backend/internal/models"

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
		return nil, &internalError{}
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

// testAuthConfigWithRefresh returns an AuthConfig suitable for refresh token testing.
func testAuthConfigWithRefresh() *config.AuthConfig {
	return &config.AuthConfig{
		JWTSecret:               "test-secret-key-long-enough",
		JWTExpiration:           time.Hour,
		AccessTokenExpiration:   15 * time.Minute,
		RefreshTokenExpiration:  7 * 24 * time.Hour,
		SessionIdleTimeout:     30 * time.Minute,
		MaxRefreshTokensPerUser: 10,
		SecureCookies:           false,
	}
}

// setupRefreshAuthRouter creates a gin engine with auth + refresh routes.
func setupRefreshAuthRouter(userRepo *MockUserRepository, refreshRepo *MockRefreshTokenRepository, blocklist *middleware.TokenBlocklist) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	cfg := testAuthConfigWithRefresh()
	h := NewAuthHandler(userRepo, cfg)
	h.SetRefreshTokenRepo(refreshRepo)
	if blocklist != nil {
		h.SetTokenBlocklist(blocklist)
	}

	auth := r.Group("/api/v1/auth")
	{
		auth.POST("/login", h.Login)
		auth.POST("/refresh", h.Refresh)
		// logout/logout-all require auth context
		auth.POST("/logout", injectAuthContext("uid-1", "user"), h.Logout)
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
		setup      func(*MockUserRepository, *MockRefreshTokenRepository) string // returns raw cookie value
		wantStatus int
		wantToken  bool
	}{
		{
			name: "valid refresh token returns new access token",
			setup: func(ur *MockUserRepository, rr *MockRefreshTokenRepository) string {
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
			setup: func(_ *MockUserRepository, _ *MockRefreshTokenRepository) string {
				return "" // no cookie
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "unknown token returns 401",
			setup: func(_ *MockUserRepository, _ *MockRefreshTokenRepository) string {
				return "unknown-token-value"
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "revoked token returns 401 and revokes all for user",
			setup: func(ur *MockUserRepository, rr *MockRefreshTokenRepository) string {
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
			setup: func(ur *MockUserRepository, rr *MockRefreshTokenRepository) string {
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
			setup: func(ur *MockUserRepository, rr *MockRefreshTokenRepository) string {
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
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			userRepo := NewMockUserRepository()
			refreshRepo := NewMockRefreshTokenRepository()
			rawCookie := tt.setup(userRepo, refreshRepo)

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
	blocklist := middleware.NewTokenBlocklist(time.Minute)
	defer blocklist.Stop()

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

	router := setupRefreshAuthRouter(userRepo, refreshRepo, blocklist)
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
	blocklist := middleware.NewTokenBlocklist(time.Minute)
	defer blocklist.Stop()

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

	router := setupRefreshAuthRouter(userRepo, refreshRepo, blocklist)
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

// ---- Token Blocklist Tests ----

func TestTokenBlocklist(t *testing.T) {
	t.Parallel()

	bl := middleware.NewTokenBlocklist(100 * time.Millisecond)
	defer bl.Stop()

	assert.False(t, bl.IsBlocked("jti-1"))

	bl.Add("jti-1", time.Now().Add(time.Hour))
	assert.True(t, bl.IsBlocked("jti-1"))

	// After expiry, the entry should be cleaned up.
	bl.Add("jti-2", time.Now().Add(50*time.Millisecond))
	assert.True(t, bl.IsBlocked("jti-2"))
	time.Sleep(200 * time.Millisecond) // wait for cleanup + expiry
	assert.False(t, bl.IsBlocked("jti-2"))
}

// ---- Refresh without repository configured returns 501 ----

func TestRefreshNotEnabled(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	cfg := testAuthConfigWithRefresh()
	h := NewAuthHandler(NewMockUserRepository(), cfg)
	// NOT setting refresh token repo

	r.POST("/api/v1/auth/refresh", h.Refresh)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "some-value"})
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotImplemented, w.Code)
}
