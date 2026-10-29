package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type identitySnapshot struct {
	method    string
	apiKeyID  string
	jti       string
	sid       string
	issuedAt  time.Time
	hasIssued bool
	keyExp    time.Time
	hasKeyExp bool
	expiry    time.Time
	hasExpiry bool
}

func snapshot(c *gin.Context) identitySnapshot {
	var s identitySnapshot
	s.method = GetAuthMethodFromContext(c)
	s.apiKeyID = GetAPIKeyIDFromContext(c)
	s.jti = GetJTIFromContext(c)
	s.sid = GetSessionIDFromContext(c)
	s.issuedAt, s.hasIssued = GetTokenIssuedAtFromContext(c)
	s.keyExp, s.hasKeyExp = GetAPIKeyExpiryFromContext(c)
	s.expiry, s.hasExpiry = GetTokenExpiryFromContext(c)
	return s
}

func TestCombinedAuth_IdentityContext(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rawKey, prefix, hash, err := models.GenerateAPIKey()
	require.NoError(t, err)
	keyExpiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	const secret = "identity-secret-0123456789abcdef0123"

	tests := []struct {
		name   string
		header func(t *testing.T) (string, string)
		check  func(t *testing.T, s identitySnapshot)
	}{
		{
			name: "jwt",
			header: func(t *testing.T) (string, string) {
				tok, err := GenerateTokenWithOpts(GenerateTokenOptions{UserID: "u1", Username: "a", Role: "user", Secret: secret, Expiration: time.Hour, SessionID: "sid-1"})
				require.NoError(t, err)
				return "Authorization", "Bearer " + tok
			},
			check: func(t *testing.T, s identitySnapshot) {
				assert.Equal(t, AuthMethodJWT, s.method)
				assert.Empty(t, s.apiKeyID)
				assert.NotEmpty(t, s.jti)
				assert.Equal(t, "sid-1", s.sid)
				assert.True(t, s.hasIssued)
				assert.WithinDuration(t, time.Now(), s.issuedAt, 5*time.Second)
				assert.NotZero(t, s.issuedAt.Nanosecond()%int(time.Second), "iat_ms keeps the sub-second part")
				assert.True(t, s.hasExpiry)
				assert.False(t, s.hasKeyExp)
			},
		},
		{
			name:   "api key",
			header: func(*testing.T) (string, string) { return "X-API-Key", rawKey },
			check: func(t *testing.T, s identitySnapshot) {
				assert.Equal(t, AuthMethodAPIKey, s.method)
				assert.Equal(t, "key-1", s.apiKeyID)
				assert.Empty(t, s.jti)
				assert.Empty(t, s.sid)
				assert.False(t, s.hasIssued)
				assert.True(t, s.hasKeyExp)
				assert.True(t, keyExpiry.Equal(s.keyExp))
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &identityAPIKeyRepo{records: []*models.APIKey{{ID: "key-1", UserID: "u1", Prefix: prefix, KeyHash: hash, ExpiresAt: &keyExpiry}}}
			var got identitySnapshot
			r := gin.New()
			r.Use(CombinedAuth(APIKeyAuthDeps{
				JWTSecret:  secret,
				APIKeyRepo: repo,
				UserRepo:   &stubUserRepo{user: &models.User{ID: "u1", Username: "a", Role: "user"}},
			}))
			r.GET("/x", func(c *gin.Context) {
				got = snapshot(c)
				c.Status(http.StatusOK)
			})
			name, value := tt.header(t)
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set(name, value)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
			tt.check(t, got)
		})
	}
}

func TestAuthRequired_IdentityContextJWTOnly(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	const secret = "identity-secret-0123456789abcdef0123"
	tok, err := GenerateToken("u1", "a", "user", secret, time.Hour)
	require.NoError(t, err)

	var got identitySnapshot
	r := gin.New()
	// No API key repository: CombinedAuth falls back to the JWT middleware.
	r.Use(CombinedAuth(APIKeyAuthDeps{JWTSecret: secret}))
	r.GET("/x", func(c *gin.Context) {
		got = snapshot(c)
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, AuthMethodJWT, got.method)
	assert.True(t, got.hasIssued)
}

type identityAPIKeyRepo struct {
	models.APIKeyRepository
	records []*models.APIKey
}

func (r *identityAPIKeyRepo) FindByPrefix(_ context.Context, _ string) ([]*models.APIKey, error) {
	return r.records, nil
}

func (r *identityAPIKeyRepo) UpdateLastUsed(_ context.Context, _, _ string, _ time.Time) error {
	return nil
}
