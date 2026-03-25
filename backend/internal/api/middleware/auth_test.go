package middleware

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-jwt-secret"

// makeValidToken creates a signed JWT for testing.
func makeValidToken(t *testing.T, secret, userID, username, role string, expiry time.Duration) string {
	t.Helper()
	token, err := GenerateToken(userID, username, role, secret, expiry)
	require.NoError(t, err)
	return token
}

func TestGenerateToken(t *testing.T) {
	t.Parallel()

	t.Run("generates valid token", func(t *testing.T) {
		t.Parallel()
		token, err := GenerateToken("user-1", "alice", "admin", testSecret, time.Hour)
		require.NoError(t, err)
		assert.NotEmpty(t, token)
	})

	t.Run("token contains correct claims", func(t *testing.T) {
		t.Parallel()
		token, err := GenerateToken("user-42", "bob", "devops", testSecret, time.Hour)
		require.NoError(t, err)

		claims := &Claims{}
		parsed, parseErr := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
			return []byte(testSecret), nil
		})
		require.NoError(t, parseErr)
		assert.True(t, parsed.Valid)
		assert.Equal(t, "user-42", claims.UserID)
		assert.Equal(t, "bob", claims.Username)
		assert.Equal(t, "devops", claims.Role)
	})

	t.Run("expired token fails validation", func(t *testing.T) {
		t.Parallel()
		token, err := GenerateToken("user-1", "alice", "user", testSecret, -time.Second)
		require.NoError(t, err)

		claims := &Claims{}
		_, parseErr := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
			return []byte(testSecret), nil
		})
		assert.Error(t, parseErr)
	})
}

func TestAuthRequired(t *testing.T) {
	t.Parallel()

	setupRouter := func() *gin.Engine {
		r := gin.New()
		r.Use(AuthRequired(testSecret))
		r.GET("/protected", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"user_id":  GetUserIDFromContext(c),
				"username": GetUsernameFromContext(c),
				"role":     GetRoleFromContext(c),
			})
		})
		return r
	}

	validToken := makeValidToken(t, testSecret, "user-1", "alice", "admin", time.Hour)
	expiredToken := makeValidToken(t, testSecret, "user-1", "alice", "admin", -time.Second)

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
		wantUserID string
	}{
		{
			name:       "missing authorization header",
			authHeader: "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "non-Bearer scheme",
			authHeader: "Basic dXNlcjpwYXNz",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "Bearer with no token",
			authHeader: "Bearer",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "Bearer with garbage token",
			authHeader: "Bearer not.a.valid.jwt",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "valid token",
			authHeader: "Bearer " + validToken,
			wantStatus: http.StatusOK,
			wantUserID: "user-1",
		},
		{
			name:       "expired token",
			authHeader: "Bearer " + expiredToken,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "token signed with wrong secret",
			authHeader: "Bearer " + makeValidToken(t, "other-secret", "user-1", "alice", "admin", time.Hour),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "case-insensitive Bearer",
			authHeader: "bearer " + validToken,
			wantStatus: http.StatusOK,
			wantUserID: "user-1",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router := setupRouter()
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			router.ServeHTTP(w, req)
			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

func TestAuthRequiredSetsContextValues(t *testing.T) {
	t.Parallel()

	r := gin.New()
	r.Use(AuthRequired(testSecret))

	var capturedID, capturedUsername, capturedRole string
	r.GET("/check", func(c *gin.Context) {
		capturedID = GetUserIDFromContext(c)
		capturedUsername = GetUsernameFromContext(c)
		capturedRole = GetRoleFromContext(c)
		c.Status(http.StatusOK)
	})

	token := makeValidToken(t, testSecret, "uid-99", "devuser", "devops", time.Hour)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/check", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "uid-99", capturedID)
	assert.Equal(t, "devuser", capturedUsername)
	assert.Equal(t, "devops", capturedRole)
}

func TestGetContextHelpers_EmptyWhenNotSet(t *testing.T) {
	t.Parallel()

	r := gin.New()
	var id, username, role string
	r.GET("/unprotected", func(c *gin.Context) {
		id = GetUserIDFromContext(c)
		username = GetUsernameFromContext(c)
		role = GetRoleFromContext(c)
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/unprotected", nil)
	r.ServeHTTP(w, req)

	assert.Empty(t, id)
	assert.Empty(t, username)
	assert.Empty(t, role)
}

func TestGenerateTokenWithOpts_IncludesAuthProvider(t *testing.T) {
	t.Parallel()

	token, err := GenerateTokenWithOpts(GenerateTokenOptions{
		UserID:       "uid-1",
		Username:     "alice",
		Role:         "admin",
		Secret:       testSecret,
		Expiration:   time.Hour,
		AuthProvider: "oidc",
		Email:        "user@example.com",
	})
	require.NoError(t, err)

	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(testSecret), nil
	})
	require.NoError(t, err)
	assert.True(t, parsed.Valid)
	assert.Equal(t, "oidc", claims.AuthProvider)
	assert.Equal(t, "user@example.com", claims.Email)
	assert.Equal(t, "uid-1", claims.UserID)
	assert.Equal(t, "alice", claims.Username)
	assert.Equal(t, "admin", claims.Role)
}

func TestGenerateTokenWithOpts_OmitsEmptyFields(t *testing.T) {
	t.Parallel()

	token, err := GenerateTokenWithOpts(GenerateTokenOptions{
		UserID:     "uid-2",
		Username:   "bob",
		Role:       "user",
		Secret:     testSecret,
		Expiration: time.Hour,
	})
	require.NoError(t, err)

	// Parse the raw JWT payload to check omitempty behaviour.
	parts := strings.SplitN(token, ".", 3)
	require.Len(t, parts, 3, "JWT must have 3 segments")

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(payload, &raw))

	assert.NotContains(t, raw, "auth_provider", "auth_provider must be omitted when empty")
	assert.NotContains(t, raw, "email", "email must be omitted when empty")

	// Standard claims should still be present.
	assert.Equal(t, "uid-2", raw["user_id"])
	assert.Equal(t, "bob", raw["username"])
}

func TestGenerateToken_BackwardCompatible(t *testing.T) {
	t.Parallel()

	token, err := GenerateToken("uid-3", "carol", "devops", testSecret, time.Hour)
	require.NoError(t, err)

	// Parse with Claims struct — should work identically.
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(testSecret), nil
	})
	require.NoError(t, err)
	assert.True(t, parsed.Valid)
	assert.Equal(t, "uid-3", claims.UserID)
	assert.Equal(t, "carol", claims.Username)
	assert.Equal(t, "devops", claims.Role)
	assert.Empty(t, claims.AuthProvider, "legacy GenerateToken must not set auth_provider")
	assert.Empty(t, claims.Email, "legacy GenerateToken must not set email")

	// Also verify the raw payload omits the fields.
	parts := strings.SplitN(token, ".", 3)
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(payload, &raw))
	assert.NotContains(t, raw, "auth_provider")
	assert.NotContains(t, raw, "email")
}
