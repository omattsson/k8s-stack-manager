package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

// TestLogout_RevokesSessionAndReportsFailures checks that logout revokes the
// whole session family of the refresh token cookie, and that a failed lookup
// or revoke answers 500 but still clears the cookie, blocks the access token
// and closes its sockets (#673).
func TestLogout_RevokesSessionAndReportsFailures(t *testing.T) {
	t.Parallel()

	const (
		rawOld   = "logout-session-old-token"
		rawOther = "logout-session-unknown-token"
	)

	tests := []struct {
		setup       func(repo *MockRefreshTokenRepository)
		name        string
		cookie      string
		wantRevoked []string
		wantActive  []string
		wantCode    int
	}{
		{
			name:        "rotated token revokes its active successor",
			cookie:      rawOld,
			wantCode:    http.StatusOK,
			wantRevoked: []string{"rt-old", "rt-new"},
			wantActive:  []string{"rt-other-session"},
		},
		{
			name:       "unknown token answers 200",
			cookie:     rawOther,
			wantCode:   http.StatusOK,
			wantActive: []string{"rt-new", "rt-other-session"},
		},
		{
			name:       "failed lookup answers 500",
			cookie:     rawOld,
			setup:      func(repo *MockRefreshTokenRepository) { repo.findErr = errors.New("db down") },
			wantCode:   http.StatusInternalServerError,
			wantActive: []string{"rt-new", "rt-other-session"},
		},
		{
			name:       "failed revoke answers 500",
			cookie:     rawOld,
			setup:      func(repo *MockRefreshTokenRepository) { repo.revokeAllErr = errors.New("db down") },
			wantCode:   http.StatusInternalServerError,
			wantActive: []string{"rt-new", "rt-other-session"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
			cfg := testAuthConfigWithRefresh()

			repo := NewMockRefreshTokenRepository()
			now := time.Now().UTC()
			rotated := now.Add(-time.Minute)
			seed := []*models.RefreshToken{
				{ID: "rt-old", FamilyID: "fam-1", TokenHash: hashRefreshToken(rawOld), Revoked: true, RotatedAt: &rotated},
				{ID: "rt-new", FamilyID: "fam-1", TokenHash: hashRefreshToken("logout-session-new-token")},
				{ID: "rt-other-session", FamilyID: "fam-2", TokenHash: hashRefreshToken("logout-session-other")},
			}
			for _, rt := range seed {
				rt.UserID, rt.ExpiresAt, rt.CreatedAt, rt.LastActivity = "uid-1", now.Add(time.Hour), now, now
				require.NoError(t, repo.Create(context.Background(), rt))
			}
			if tt.setup != nil {
				tt.setup(repo)
			}

			store := sessionstore.NewMemoryStore()
			t.Cleanup(store.Stop)
			revoker := &fakeRevoker{}
			h := NewAuthHandler(NewMockUserRepository(), cfg, &config.OIDCConfig{})
			h.SetRefreshTokenRepo(repo)
			h.SetSessionStore(store)
			h.SetWebSocketRevoker(revoker)
			r := gin.New()
			r.POST("/api/v1/auth/logout", h.Logout)

			access, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
				UserID: "uid-1", Username: "alice", Role: "user",
				Secret: cfg.JWTSecret, Expiration: cfg.AccessTokenExpiration,
			})
			require.NoError(t, err)
			claims, err := middleware.ValidateJWT(access, cfg.JWTSecret)
			require.NoError(t, err)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
			req.Header.Set("Authorization", "Bearer "+access)
			req.AddCookie(&http.Cookie{Name: refreshTokenCookieName, Value: tt.cookie})
			r.ServeHTTP(w, req)

			require.Equal(t, tt.wantCode, w.Code, w.Body.String())
			if tt.wantCode == http.StatusInternalServerError {
				assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
			}

			repo.mu.RLock()
			for _, id := range tt.wantRevoked {
				assert.True(t, repo.tokens[id].Revoked, "%s is revoked", id)
			}
			for _, id := range tt.wantActive {
				assert.False(t, repo.tokens[id].Revoked, "%s stays active", id)
			}
			repo.mu.RUnlock()

			var cleared bool
			for _, ck := range w.Result().Cookies() {
				if ck.Name == refreshTokenCookieName && ck.MaxAge < 0 {
					cleared = true
				}
			}
			assert.True(t, cleared, "the refresh cookie is cleared")
			blocked, err := store.IsTokenBlocked(context.Background(), claims.ID)
			require.NoError(t, err)
			assert.True(t, blocked, "the access token is blocked")
			_, tokens := revoker.calls()
			assert.Equal(t, []string{claims.ID}, tokens, "the sockets of the access token are closed")
		})
	}
}
