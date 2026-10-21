package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/config"
	"backend/internal/models"
	"backend/pkg/dberrors"

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
	return setupAPIKeyRouterWithMaxDays(userRepo, apiKeyRepo, callerID, callerRole, 0)
}

// setupAPIKeyRouterWithMaxDays creates a gin engine wired to APIKeyHandler routes
// with a configurable max lifetime policy (0 = no limit).
func setupAPIKeyRouterWithMaxDays(
	userRepo *MockUserRepository,
	apiKeyRepo *MockAPIKeyRepository,
	callerID, callerRole string,
	maxDays int,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))
	cfg := &config.AuthConfig{APIKeyMaxLifetimeDays: maxDays}
	h := NewAPIKeyHandler(apiKeyRepo, userRepo, cfg)
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
	require.NoError(t, repo.Create(context.Background(), key))
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
			body:         `{"name":"ci-token","expires_in_days":90}`,
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
			body:         `{"name":"deploy-key","expires_in_days":90}`,
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
			body:         `{"name":"my-key","expires_in_days":90}`,
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
		{
			name:         "cancelled request returns 499",
			callerID:     "admin-1",
			callerRole:   "admin",
			targetUserID: "user-1",
			setupRepos: func(u *MockUserRepository, a *MockAPIKeyRepository) {
				seedUser(t, u, "user-1", "alice", "testpass", "user")
				a.SetListError(dberrors.NewDatabaseError("list_by_user", context.Canceled))
			},
			wantStatus: statusClientClosedRequest,
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

// TestAPIKeyHandler_CancelledRequestContext checks that a client cancel
// does not stop the create and delete writes, and that delete still closes
// the sockets of the key. The list read follows the cancel (499) (#656).
func TestAPIKeyHandler_CancelledRequestContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantKeys   int
		wantClosed []string
	}{
		{
			name:       "create",
			method:     http.MethodPost,
			path:       "/api/v1/users/user-1/api-keys",
			body:       `{"name":"ci-token","expires_in_days":30}`,
			wantStatus: http.StatusCreated,
			wantKeys:   2,
		},
		{
			name:       "delete",
			method:     http.MethodDelete,
			path:       "/api/v1/users/user-1/api-keys/key-1",
			wantStatus: http.StatusNoContent,
			wantKeys:   0,
			wantClosed: []string{"key-1"},
		},
		{
			name:       "list",
			method:     http.MethodGet,
			path:       "/api/v1/users/user-1/api-keys",
			wantStatus: statusClientClosedRequest,
			wantKeys:   1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			userRepo := NewMockUserRepository()
			seedUser(t, userRepo, "user-1", "alice", "testpass", "user")
			mock := NewMockAPIKeyRepository()
			seedAPIKey(t, mock, "key-1", "user-1", "existing")
			revoker := &fakeRevoker{}

			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(injectAuthContext("user-1", "user"))
			h := NewAPIKeyHandler(mock, userRepo, &config.AuthConfig{})
			h.SetWebSocketRevoker(revoker)
			r.POST("/api/v1/users/:id/api-keys", h.CreateAPIKey)
			r.GET("/api/v1/users/:id/api-keys", h.ListAPIKeys)
			r.DELETE("/api/v1/users/:id/api-keys/:keyId", h.DeleteAPIKey)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			req := httptest.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.body)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			keys, err := mock.ListByUser(context.Background(), "user-1")
			require.NoError(t, err)
			assert.Len(t, keys, tt.wantKeys)
			assert.Equal(t, tt.wantClosed, revoker.keyCalls())
		})
	}
}

// ---- TestCreateAPIKeyExpiration ----

func TestCreateAPIKeyExpiration(t *testing.T) {
	t.Parallel()

	type tc struct {
		name       string
		maxDays    int
		body       string
		wantStatus int
		// If non-zero, verify that ExpiresAt is approximately this many days from now.
		wantExpiryDays int
		wantErrSubstr  string
	}

	tests := []tc{
		{
			name:           "expires_in_days 90 - success",
			maxDays:        365,
			body:           `{"name":"ci-key","expires_in_days":90}`,
			wantStatus:     http.StatusCreated,
			wantExpiryDays: 90,
		},
		{
			name:          "expires_in_days 0 - 400",
			maxDays:       0,
			body:          `{"name":"ci-key","expires_in_days":0}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "expires_in_days must be positive",
		},
		{
			name:          "expires_in_days negative - 400",
			maxDays:       0,
			body:          `{"name":"ci-key","expires_in_days":-5}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "expires_in_days must be positive",
		},
		{
			name:          "both expires_at and expires_in_days - 400",
			maxDays:       0,
			body:          `{"name":"ci-key","expires_at":"2099-12-31","expires_in_days":90}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "Cannot specify both",
		},
		{
			name:          "expires_in_days 500 exceeds max 365 - 400",
			maxDays:       365,
			body:          `{"name":"ci-key","expires_in_days":500}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "maximum allowed lifetime of 365 days",
		},
		{
			name:          "no expiry with max 365 - rejected",
			maxDays:       365,
			body:          `{"name":"ci-key"}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "Expiration is required",
		},
		{
			name:       "expires_at within max - success",
			maxDays:    365,
			body:       `{"name":"ci-key","expires_at":"` + time.Now().UTC().AddDate(0, 0, 30).Format("2006-01-02") + `"}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:          "expires_at beyond max - 400",
			maxDays:       365,
			body:          `{"name":"ci-key","expires_at":"` + time.Now().UTC().AddDate(0, 0, 400).Format("2006-01-02") + `"}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "maximum allowed lifetime of 365 days",
		},
		{
			name:          "no expiry without max limit - rejected",
			maxDays:       0,
			body:          `{"name":"ci-key"}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "Expiration is required",
		},
		{
			name:           "expires_in_days exactly at max - success",
			maxDays:        365,
			body:           `{"name":"ci-key","expires_in_days":365}`,
			wantStatus:     http.StatusCreated,
			wantExpiryDays: 365,
		},
		{
			name:          "expires_in_days one day over max - 400",
			maxDays:       365,
			body:          `{"name":"ci-key","expires_in_days":366}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "maximum allowed lifetime of 365 days",
		},
		{
			name:          "expires_at in the past - 400",
			maxDays:       0,
			body:          `{"name":"ci-key","expires_at":"2020-01-01"}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "Expiry date must be in the future",
		},
		{
			name:          "invalid expires_at format - 400",
			maxDays:       0,
			body:          `{"name":"ci-key","expires_at":"not-a-date"}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "Invalid expires_at format",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			userRepo := NewMockUserRepository()
			apiKeyRepo := NewMockAPIKeyRepository()
			seedUser(t, userRepo, "user-1", "alice", "testpass", "user")

			router := setupAPIKeyRouterWithMaxDays(userRepo, apiKeyRepo, "user-1", "user", tt.maxDays)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost,
				"/api/v1/users/user-1/api-keys",
				bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			if tt.wantErrSubstr != "" {
				var resp map[string]interface{}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				errMsg, _ := resp["error"].(string)
				assert.Contains(t, errMsg, tt.wantErrSubstr)
			}

			if tt.wantStatus == http.StatusCreated {
				var resp map[string]interface{}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

				if tt.wantExpiryDays > 0 {
					expiresStr, ok := resp["expires_at"].(string)
					require.True(t, ok, "expires_at should be present")
					parsed, err := time.Parse(time.RFC3339, expiresStr)
					require.NoError(t, err)
					now := time.Now().UTC()
					expectedDate := now.AddDate(0, 0, tt.wantExpiryDays)
					// expires_in_days uses exact offset; date-only expires_at uses end-of-day.
					// Allow up to 24h + 60s tolerance to cover both formats.
					diff := parsed.Sub(expectedDate)
					assert.InDelta(t, 0, diff.Seconds(), 86460, "expires_at should be ~%d days from now", tt.wantExpiryDays)
				}

			}
		})
	}
}
