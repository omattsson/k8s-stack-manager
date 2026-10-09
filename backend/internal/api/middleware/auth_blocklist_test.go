package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/sessionstore"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
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

func (m *mockSessionStore) IsUserBlocked(_ context.Context, userID string, _ time.Time) (bool, error) {
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

func (m *mockSessionStore) SaveCLIAuth(_ context.Context, _ string, _ sessionstore.CLIAuthData, _ time.Duration) error {
	return nil
}

func (m *mockSessionStore) GetCLIAuth(_ context.Context, _ string) (*sessionstore.CLIAuthData, error) {
	return nil, nil
}

func (m *mockSessionStore) UpdateCLIAuth(_ context.Context, _ string, _ sessionstore.CLIAuthData) error {
	return nil
}
func (m *mockSessionStore) ConsumeCLIAuth(_ context.Context, _ string) (*sessionstore.CLIAuthData, error) {
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
			name: "blocked user gets 401 Session revoked",
			setupStore: func(store *mockSessionStore) {
				store.blockedUsers[targetUserID] = true
			},
			wantStatus: http.StatusUnauthorized,
			wantErrMsg: "Session revoked",
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

// signTestToken signs a token for userID with the given issued-at time. A zero
// iat leaves the claim out.
func signTestToken(t *testing.T, userID string, iat time.Time) string {
	t.Helper()
	rc := jwt.RegisteredClaims{
		ID:        "jti-" + userID + "-" + strings.ReplaceAll(iat.Format(time.RFC3339Nano), ":", ""),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		Subject:   userID,
	}
	if !iat.IsZero() {
		rc.IssuedAt = jwt.NewNumericDate(iat)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		UserID: userID, Username: "alice", Role: "user", RegisteredClaims: rc,
	})
	signed, err := tok.SignedString([]byte(testSecret))
	require.NoError(t, err)
	return signed
}

// TestAuthRequired_UserBlock_IssuedAt checks that a user block revokes only
// tokens issued at or before the block, with a real MemoryStore.
func TestAuthRequired_UserBlock_IssuedAt(t *testing.T) {
	t.Parallel()

	const userID = "user-iat"
	tests := []struct {
		name       string
		block      bool
		iat        time.Time
		wantStatus int
	}{
		{"no block, old token", false, time.Now().Add(-time.Minute), http.StatusOK},
		{"blocked, token issued before block", true, time.Now().Add(-time.Minute), http.StatusUnauthorized},
		{"blocked, token issued after block", true, time.Now().Add(2 * time.Second), http.StatusOK},
		{"blocked, token without iat", true, time.Time{}, http.StatusUnauthorized},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := sessionstore.NewMemoryStore()
			t.Cleanup(store.Stop)
			if tt.block {
				require.NoError(t, store.BlockUser(context.Background(), userID, time.Now().Add(time.Hour)))
			}

			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(AuthRequiredWithSessionStore(testSecret, store))
			r.GET("/protected", func(c *gin.Context) { c.Status(http.StatusOK) })

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set("Authorization", "Bearer "+signTestToken(t, userID, tt.iat))
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus == http.StatusUnauthorized {
				assert.Contains(t, w.Body.String(), "Session revoked")
			}
		})
	}
}

// TestValidateJWT_ExpirationRequired checks that a token without an exp claim
// is rejected.
func TestValidateJWT_ExpirationRequired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		exp     *jwt.NumericDate
		wantErr bool
	}{
		{name: "with exp", exp: jwt.NewNumericDate(time.Now().Add(time.Hour))},
		{name: "without exp", wantErr: true},
		{name: "expired", exp: jwt.NewNumericDate(time.Now().Add(-time.Minute)), wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tok := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
				UserID: "u1", Role: "user",
				RegisteredClaims: jwt.RegisteredClaims{ID: "jti-1", IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: tt.exp},
			})
			signed, err := tok.SignedString([]byte(testSecret))
			require.NoError(t, err)
			_, err = ValidateJWT(signed, testSecret)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRevocationStatus(t *testing.T) {
	t.Parallel()

	dbDown := errors.New("db down")
	tests := []struct {
		name        string
		setup       func(*mockSessionStore)
		nilStore    bool
		wantRevoked error
		wantLookup  bool
	}{
		{name: "nothing revoked"},
		{name: "nil store", nilStore: true},
		{name: "token revoked", setup: func(m *mockSessionStore) { m.blockedTokens["jti-1"] = true }, wantRevoked: ErrTokenRevoked},
		{name: "user revoked", setup: func(m *mockSessionStore) { m.blockedUsers["u1"] = true }, wantRevoked: ErrSessionRevoked},
		{name: "token check fails, user revoked", setup: func(m *mockSessionStore) { m.tokenBlockErr = dbDown; m.blockedUsers["u1"] = true }, wantRevoked: ErrSessionRevoked, wantLookup: true},
		{name: "both checks fail", setup: func(m *mockSessionStore) { m.tokenBlockErr = dbDown; m.userBlockErr = dbDown }, wantLookup: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMockSessionStore()
			if tt.setup != nil {
				tt.setup(m)
			}
			var store sessionstore.SessionStore = m
			if tt.nilStore {
				store = nil
			}
			claims := &Claims{UserID: "u1", RegisteredClaims: jwt.RegisteredClaims{ID: "jti-1", IssuedAt: jwt.NewNumericDate(time.Now())}}
			revoked, lookupErr := RevocationStatus(context.Background(), store, claims)
			assert.Equal(t, tt.wantRevoked, revoked)
			if tt.wantLookup {
				assert.ErrorIs(t, lookupErr, dbDown)
			} else {
				assert.NoError(t, lookupErr)
			}
			// CheckRevocation returns the same revocation and fails open.
			assert.Equal(t, tt.wantRevoked, CheckRevocation(context.Background(), store, claims))
		})
	}
}
