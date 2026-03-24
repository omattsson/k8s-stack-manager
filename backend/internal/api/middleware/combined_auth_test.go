package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const combinedTestSecret = "combined-test-jwt-secret"

// ---- in-package minimal mocks ----

type testAPIKeyRepo struct {
	mu   sync.RWMutex
	keys map[string][]*models.APIKey // keyed by Prefix
}

func newTestAPIKeyRepo() *testAPIKeyRepo {
	return &testAPIKeyRepo{keys: make(map[string][]*models.APIKey)}
}

func (r *testAPIKeyRepo) addKey(k *models.APIKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[k.Prefix] = append(r.keys[k.Prefix], k)
}

func (r *testAPIKeyRepo) FindByPrefix(prefix string) ([]*models.APIKey, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ks, ok := r.keys[prefix]
	if !ok || len(ks) == 0 {
		return nil, http.ErrNoCookie // any non-nil error signals "not found"
	}
	out := make([]*models.APIKey, len(ks))
	for i, k := range ks {
		cp := *k
		out[i] = &cp
	}
	return out, nil
}

func (r *testAPIKeyRepo) UpdateLastUsed(userID, keyID string, t time.Time) error { return nil }
func (r *testAPIKeyRepo) Create(key *models.APIKey) error                        { return nil }
func (r *testAPIKeyRepo) FindByID(userID, keyID string) (*models.APIKey, error)  { return nil, nil }
func (r *testAPIKeyRepo) ListByUser(userID string) ([]*models.APIKey, error)     { return nil, nil }
func (r *testAPIKeyRepo) Delete(userID, keyID string) error                      { return nil }

type testUserRepo struct {
	mu    sync.RWMutex
	users map[string]*models.User // keyed by ID
}

func newTestUserRepo() *testUserRepo {
	return &testUserRepo{users: make(map[string]*models.User)}
}

func (r *testUserRepo) addUser(u *models.User) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[u.ID] = u
}

func (r *testUserRepo) FindByID(id string) (*models.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	u, ok := r.users[id]
	if !ok {
		return nil, http.ErrNoCookie // any non-nil error signals "not found"
	}
	cp := *u
	return &cp, nil
}

func (r *testUserRepo) Create(user *models.User) error                       { return nil }
func (r *testUserRepo) FindByUsername(username string) (*models.User, error) { return nil, nil }
func (r *testUserRepo) FindByExternalID(provider, externalID string) (*models.User, error) {
	return nil, nil
}
func (r *testUserRepo) Update(user *models.User) error { return nil }
func (r *testUserRepo) Delete(id string) error         { return nil }
func (r *testUserRepo) List() ([]models.User, error)   { return nil, nil }

// ---- helpers ----

func buildCombinedAuthRouter(deps APIKeyAuthDeps) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CombinedAuth(deps))
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"user_id":  GetUserIDFromContext(c),
			"username": GetUsernameFromContext(c),
			"role":     GetRoleFromContext(c),
		})
	})
	return r
}

func doGET(router *gin.Engine, headers map[string]string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/test", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	router.ServeHTTP(w, req)
	return w
}

// makeValidJWT creates a signed JWT for the given identity, expiring in 1 hour.
func makeValidJWT(t *testing.T, userID, username, role string) string {
	t.Helper()
	token, err := GenerateToken(userID, username, role, combinedTestSecret, time.Hour)
	require.NoError(t, err)
	return token
}

// seedKeyInRepo generates a real key, stores it in the mock repos, and returns the raw key.
func seedKeyInRepo(t *testing.T, apiKeyRepo *testAPIKeyRepo, userRepo *testUserRepo,
	keyID, userID, username, role string, expiresAt *time.Time,
) string {
	t.Helper()
	rawKey, prefix, hash, err := models.GenerateAPIKey()
	require.NoError(t, err)
	apiKeyRepo.addKey(&models.APIKey{
		ID:        keyID,
		UserID:    userID,
		Prefix:    prefix,
		KeyHash:   hash,
		ExpiresAt: expiresAt,
	})
	userRepo.addUser(&models.User{
		ID:       userID,
		Username: username,
		Role:     role,
	})
	return rawKey
}

// ---- TestCombinedAuth ----

func TestCombinedAuth(t *testing.T) {
	t.Parallel()

	t.Run("valid JWT proceeds and sets context", func(t *testing.T) {
		t.Parallel()
		apiKeyRepo := newTestAPIKeyRepo()
		userRepo := newTestUserRepo()
		router := buildCombinedAuthRouter(APIKeyAuthDeps{
			JWTSecret:  combinedTestSecret,
			APIKeyRepo: apiKeyRepo,
			UserRepo:   userRepo,
		})

		token := makeValidJWT(t, "jwt-user-1", "alice", "admin")
		w := doGET(router, map[string]string{"Authorization": "Bearer " + token})

		assert.Equal(t, http.StatusOK, w.Code)
		var body map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, "jwt-user-1", body["user_id"])
		assert.Equal(t, "alice", body["username"])
		assert.Equal(t, "admin", body["role"])
	})

	t.Run("valid X-API-Key proceeds and sets context", func(t *testing.T) {
		t.Parallel()
		apiKeyRepo := newTestAPIKeyRepo()
		userRepo := newTestUserRepo()
		rawKey := seedKeyInRepo(t, apiKeyRepo, userRepo, "key-1", "api-user-1", "bob", "user", nil)
		router := buildCombinedAuthRouter(APIKeyAuthDeps{
			JWTSecret:  combinedTestSecret,
			APIKeyRepo: apiKeyRepo,
			UserRepo:   userRepo,
		})

		w := doGET(router, map[string]string{"X-API-Key": "sk_" + rawKey})

		assert.Equal(t, http.StatusOK, w.Code)
		var body map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, "api-user-1", body["user_id"])
		assert.Equal(t, "bob", body["username"])
		assert.Equal(t, "user", body["role"])
	})

	t.Run("missing auth returns 401", func(t *testing.T) {
		t.Parallel()
		router := buildCombinedAuthRouter(APIKeyAuthDeps{
			JWTSecret:  combinedTestSecret,
			APIKeyRepo: newTestAPIKeyRepo(),
			UserRepo:   newTestUserRepo(),
		})

		w := doGET(router, nil)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("expired API key returns 401", func(t *testing.T) {
		t.Parallel()
		apiKeyRepo := newTestAPIKeyRepo()
		userRepo := newTestUserRepo()
		past := time.Now().Add(-time.Hour)
		rawKey := seedKeyInRepo(t, apiKeyRepo, userRepo, "key-exp", "user-exp", "eve", "user", &past)
		router := buildCombinedAuthRouter(APIKeyAuthDeps{
			JWTSecret:  combinedTestSecret,
			APIKeyRepo: apiKeyRepo,
			UserRepo:   userRepo,
		})

		w := doGET(router, map[string]string{"X-API-Key": "sk_" + rawKey})
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("hash mismatch returns 401", func(t *testing.T) {
		t.Parallel()
		apiKeyRepo := newTestAPIKeyRepo()
		userRepo := newTestUserRepo()
		rawKey := seedKeyInRepo(t, apiKeyRepo, userRepo, "key-real", "user-real", "charlie", "user", nil)

		// Keep the same prefix (first 16 chars of rawKey) but replace the rest with 'f's.
		// The prefix will match the stored key, but the hash will differ.
		fakeRaw := rawKey[:16] + strings.Repeat("f", len(rawKey)-16)

		router := buildCombinedAuthRouter(APIKeyAuthDeps{
			JWTSecret:  combinedTestSecret,
			APIKeyRepo: apiKeyRepo,
			UserRepo:   userRepo,
		})

		w := doGET(router, map[string]string{"X-API-Key": "sk_" + fakeRaw})
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("JWT takes precedence over X-API-Key when both headers present", func(t *testing.T) {
		t.Parallel()
		apiKeyRepo := newTestAPIKeyRepo()
		userRepo := newTestUserRepo()
		// API key belongs to "api-user"; JWT belongs to "jwt-user".
		rawKey := seedKeyInRepo(t, apiKeyRepo, userRepo, "key-both", "api-user-both", "apiuser", "user", nil)
		router := buildCombinedAuthRouter(APIKeyAuthDeps{
			JWTSecret:  combinedTestSecret,
			APIKeyRepo: apiKeyRepo,
			UserRepo:   userRepo,
		})

		token := makeValidJWT(t, "jwt-user-both", "jwtuser", "admin")
		w := doGET(router, map[string]string{
			"Authorization": "Bearer " + token,
			"X-API-Key":     "sk_" + rawKey,
		})

		assert.Equal(t, http.StatusOK, w.Code)
		var body map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		// Must use JWT identity, not API key identity.
		assert.Equal(t, "jwt-user-both", body["user_id"])
		assert.Equal(t, "jwtuser", body["username"])
	})

	t.Run("fallback to JWT-only when repos are nil", func(t *testing.T) {
		t.Parallel()
		// When APIKeyRepo or UserRepo is nil, CombinedAuth falls back to AuthRequired.
		router := buildCombinedAuthRouter(APIKeyAuthDeps{
			JWTSecret:  combinedTestSecret,
			APIKeyRepo: nil,
			UserRepo:   nil,
		})

		token := makeValidJWT(t, "jwt-user-2", "dave", "devops")
		w := doGET(router, map[string]string{"Authorization": "Bearer " + token})
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("key too short returns 401", func(t *testing.T) {
		t.Parallel()
		router := buildCombinedAuthRouter(APIKeyAuthDeps{
			JWTSecret:  combinedTestSecret,
			APIKeyRepo: newTestAPIKeyRepo(),
			UserRepo:   newTestUserRepo(),
		})

		// "sk_" + 15-char raw = raw length 15, which is < 16, so middleware rejects it.
		w := doGET(router, map[string]string{"X-API-Key": "sk_short0123456789"})
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}
