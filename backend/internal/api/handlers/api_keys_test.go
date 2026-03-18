package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupAPIKeyRouter creates a gin engine wired to APIKeyHandler routes.
// callerID and callerRole are injected into the Gin context to simulate prior auth middleware.
func setupAPIKeyRouter(
	userRepo *MockUserRepository,
	apiKeyRepo *MockAPIKeyRepository,
	callerID, callerRole string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))
	h := NewAPIKeyHandler(apiKeyRepo, userRepo)
	keys := r.Group("/api/v1/users/:id/api-keys")
	{
		keys.GET("", h.ListAPIKeys)
		keys.POST("", h.CreateAPIKey)
		keys.DELETE("/:keyId", h.DeleteAPIKey)
	}
	return r
}

// seedAPIKey inserts an APIKey with deterministic values into the mock repo.
func seedAPIKey(t *testing.T, repo *MockAPIKeyRepository, id, userID, name string) *models.APIKey {
	t.Helper()
	// Ensure prefix is exactly 16 characters (required by GenerateAPIKey contract).
	prefix := (id + "0000000000000000")[:16]
	key := &models.APIKey{
		ID:        id,
		UserID:    userID,
		Name:      name,
		Prefix:    prefix,
		KeyHash:   "hash-" + id,
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, repo.Create(key))
	return key
}

// ---- TestCreateAPIKey ----

func TestCreateAPIKey(t *testing.T) {
	t.Parallel()

	type tc struct {
		name         string
		callerID     string
		callerRole   string
		targetUserID string
		body         string
		setupRepos   func(u *MockUserRepository, a *MockAPIKeyRepository)
		wantStatus   int
		checkRawKey  bool
	}

	tests := []tc{
		{
			name:         "valid - owner creates own key",
			callerID:     "user-1",
			callerRole:   "user",
			targetUserID: "user-1",
			body:         `{"name":"ci-token"}`,
			setupRepos: func(u *MockUserRepository, a *MockAPIKeyRepository) {
				seedUser(t, u, "user-1", "alice", "testpass", "user")
			},
			wantStatus:  http.StatusCreated,
			checkRawKey: true,
		},
		{
			name:         "valid - admin creates key for other user",
			callerID:     "admin-1",
			callerRole:   "admin",
			targetUserID: "user-2",
			body:         `{"name":"deploy-key"}`,
			setupRepos: func(u *MockUserRepository, a *MockAPIKeyRepository) {
				seedUser(t, u, "user-2", "bob", "testpass", "user")
			},
			wantStatus:  http.StatusCreated,
			checkRawKey: true,
		},
		{
			name:         "missing name returns 400",
			callerID:     "user-1",
			callerRole:   "user",
			targetUserID: "user-1",
			body:         `{}`,
			setupRepos: func(u *MockUserRepository, a *MockAPIKeyRepository) {
				seedUser(t, u, "user-1", "alice", "testpass", "user")
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:         "non-admin accessing other user returns 403",
			callerID:     "user-1",
			callerRole:   "user",
			targetUserID: "user-2",
			body:         `{"name":"my-key"}`,
			setupRepos:   func(u *MockUserRepository, a *MockAPIKeyRepository) {},
			wantStatus:   http.StatusForbidden,
		},
		{
			name:         "target user not found returns 404",
			callerID:     "admin-1",
			callerRole:   "admin",
			targetUserID: "ghost-user",
			body:         `{"name":"my-key"}`,
			setupRepos:   func(u *MockUserRepository, a *MockAPIKeyRepository) {},
			wantStatus:   http.StatusNotFound,
		},
		{
			name:         "repo create error returns 500",
			callerID:     "admin-1",
			callerRole:   "admin",
			targetUserID: "user-1",
			body:         `{"name":"my-key"}`,
			setupRepos: func(u *MockUserRepository, a *MockAPIKeyRepository) {
				seedUser(t, u, "user-1", "alice", "testpass", "user")
				a.SetCreateError(errors.New("db failure"))
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			userRepo := NewMockUserRepository()
			apiKeyRepo := NewMockAPIKeyRepository()
			tt.setupRepos(userRepo, apiKeyRepo)

			router := setupAPIKeyRouter(userRepo, apiKeyRepo, tt.callerID, tt.callerRole)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost,
				"/api/v1/users/"+tt.targetUserID+"/api-keys",
				bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.checkRawKey {
				var resp map[string]interface{}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				rawKey, ok := resp["raw_key"].(string)
				assert.True(t, ok && rawKey != "", "raw_key must be present and non-empty")
				_, hasHash := resp["key_hash"]
				assert.False(t, hasHash, "key_hash must never be returned in the response")
			}
		})
	}
}

// ---- TestListAPIKeys ----

func TestListAPIKeys(t *testing.T) {
	t.Parallel()

	type tc struct {
		name         string
		callerID     string
		callerRole   string
		targetUserID string
		setupRepos   func(u *MockUserRepository, a *MockAPIKeyRepository)
		wantStatus   int
		wantLen      int
	}

	tests := []tc{
		{
			name:         "owner lists own keys - returns 200 with items",
			callerID:     "user-1",
			callerRole:   "user",
			targetUserID: "user-1",
			setupRepos: func(u *MockUserRepository, a *MockAPIKeyRepository) {
				seedUser(t, u, "user-1", "alice", "testpass", "user")
				seedAPIKey(t, a, "k1", "user-1", "key-one")
				seedAPIKey(t, a, "k2", "user-1", "key-two")
			},
			wantStatus: http.StatusOK,
			wantLen:    2,
		},
		{
			name:         "admin lists other user keys - returns 200 empty",
			callerID:     "admin-1",
			callerRole:   "admin",
			targetUserID: "user-1",
			setupRepos: func(u *MockUserRepository, a *MockAPIKeyRepository) {
				seedUser(t, u, "user-1", "alice", "testpass", "user")
			},
			wantStatus: http.StatusOK,
			wantLen:    0,
		},
		{
			name:         "non-admin accessing other user returns 403",
			callerID:     "user-1",
			callerRole:   "user",
			targetUserID: "user-2",
			setupRepos:   func(u *MockUserRepository, a *MockAPIKeyRepository) {},
			wantStatus:   http.StatusForbidden,
		},
		{
			name:         "repo list error returns 500",
			callerID:     "admin-1",
			callerRole:   "admin",
			targetUserID: "user-1",
			setupRepos: func(u *MockUserRepository, a *MockAPIKeyRepository) {
				seedUser(t, u, "user-1", "alice", "testpass", "user")
				a.SetListError(errors.New("db failure"))
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			userRepo := NewMockUserRepository()
			apiKeyRepo := NewMockAPIKeyRepository()
			tt.setupRepos(userRepo, apiKeyRepo)

			router := setupAPIKeyRouter(userRepo, apiKeyRepo, tt.callerID, tt.callerRole)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet,
				"/api/v1/users/"+tt.targetUserID+"/api-keys", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusOK {
				var keys []*models.APIKey
				// null body (nil slice from mock) unmarshals to nil without error.
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &keys))
				assert.Len(t, keys, tt.wantLen)
			}
		})
	}
}

// ---- TestDeleteAPIKey ----

func TestDeleteAPIKey(t *testing.T) {
	t.Parallel()

	type tc struct {
		name         string
		callerID     string
		callerRole   string
		targetUserID string
		keyID        string
		setupRepos   func(u *MockUserRepository, a *MockAPIKeyRepository)
		wantStatus   int
	}

	tests := []tc{
		{
			name:         "owner deletes own key - returns 204",
			callerID:     "user-1",
			callerRole:   "user",
			targetUserID: "user-1",
			keyID:        "key-1",
			setupRepos: func(u *MockUserRepository, a *MockAPIKeyRepository) {
				seedAPIKey(t, a, "key-1", "user-1", "ci-key")
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:         "admin deletes key for other user - returns 204",
			callerID:     "admin-1",
			callerRole:   "admin",
			targetUserID: "user-1",
			keyID:        "key-2",
			setupRepos: func(u *MockUserRepository, a *MockAPIKeyRepository) {
				seedAPIKey(t, a, "key-2", "user-1", "ci-key")
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:         "key not found returns 404",
			callerID:     "admin-1",
			callerRole:   "admin",
			targetUserID: "user-1",
			keyID:        "nonexistent-key",
			setupRepos:   func(u *MockUserRepository, a *MockAPIKeyRepository) {},
			wantStatus:   http.StatusNotFound,
		},
		{
			name:         "non-admin accessing other user returns 403",
			callerID:     "user-1",
			callerRole:   "user",
			targetUserID: "user-2",
			keyID:        "key-1",
			setupRepos:   func(u *MockUserRepository, a *MockAPIKeyRepository) {},
			wantStatus:   http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			userRepo := NewMockUserRepository()
			apiKeyRepo := NewMockAPIKeyRepository()
			tt.setupRepos(userRepo, apiKeyRepo)

			router := setupAPIKeyRouter(userRepo, apiKeyRepo, tt.callerID, tt.callerRole)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodDelete,
				"/api/v1/users/"+tt.targetUserID+"/api-keys/"+tt.keyID, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}
