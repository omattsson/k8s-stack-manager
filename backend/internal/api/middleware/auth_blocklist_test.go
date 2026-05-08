package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/sessionstore"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSessionStore is a minimal SessionStore implementation for middleware tests.
// It supports configuring errors on IsUserBlocked to test fail-open behaviour.
type mockSessionStore struct {
	blockedUsers  map[string]bool
	blockedTokens map[string]bool
	userBlockErr  error
	tokenBlockErr error
}

func newMockSessionStore() *mockSessionStore {
	return &mockSessionStore{
		blockedUsers:  make(map[string]bool),
		blockedTokens: make(map[string]bool),
	}
}

func (m *mockSessionStore) BlockToken(_ context.Context, jti string, _ time.Time) error {
	m.blockedTokens[jti] = true
	return nil
}

func (m *mockSessionStore) IsTokenBlocked(_ context.Context, jti string) (bool, error) {
	if m.tokenBlockErr != nil {
		return false, m.tokenBlockErr
	}
	return m.blockedTokens[jti], nil
}

func (m *mockSessionStore) BlockUser(_ context.Context, userID string, _ time.Time) error {
	m.blockedUsers[userID] = true
	return nil
}

func (m *mockSessionStore) IsUserBlocked(_ context.Context, userID string) (bool, error) {
	if m.userBlockErr != nil {
		return false, m.userBlockErr
	}
	return m.blockedUsers[userID], nil
}

func (m *mockSessionStore) UnblockUser(_ context.Context, userID string) error {
	delete(m.blockedUsers, userID)
	return nil
}

func (m *mockSessionStore) SaveOIDCState(_ context.Context, _ string, _ sessionstore.OIDCStateData, _ time.Duration) error {
	return nil
}

func (m *mockSessionStore) ConsumeOIDCState(_ context.Context, _ string) (*sessionstore.OIDCStateData, error) {
	return nil, nil
}

func (m *mockSessionStore) Cleanup(_ context.Context) error { return nil }
func (m *mockSessionStore) Stop()                           {}

func TestAuthRequired_IsTokenBlocked_Error_FailOpen(t *testing.T) {
	t.Parallel()

	token, err := GenerateToken("user-tok-err", "alice", "user", testSecret, time.Hour)
	require.NoError(t, err)

	store := newMockSessionStore()
	store.tokenBlockErr = errors.New("db unavailable")

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AuthRequiredWithSessionStore(testSecret, store))
	r.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "should fail open when IsTokenBlocked errors")
}

func TestAuthRequired_UserBlocked(t *testing.T) {
	t.Parallel()

	const targetUserID = "user-blocked-123"

	tests := []struct {
		name       string
		setupStore func(store *mockSessionStore)
		wantStatus int
		wantErrMsg string
	}{
		{
			name: "blocked user gets 403 Account disabled",
			setupStore: func(store *mockSessionStore) {
				store.blockedUsers[targetUserID] = true
			},
			wantStatus: http.StatusForbidden,
			wantErrMsg: "Account disabled",
		},
		{
			name: "IsUserBlocked error is fail-open and request succeeds",
			setupStore: func(store *mockSessionStore) {
				store.userBlockErr = errors.New("db unavailable")
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			token, err := GenerateToken(targetUserID, "alice", "user", testSecret, time.Hour)
			require.NoError(t, err)

			store := newMockSessionStore()
			tt.setupStore(store)

			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(AuthRequiredWithSessionStore(testSecret, store))
			r.GET("/protected", func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantErrMsg != "" {
				assert.Contains(t, w.Body.String(), tt.wantErrMsg)
			}
		})
	}
}
