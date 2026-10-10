package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupUserRouterFull creates a gin engine with a UserHandler wired with an optional
// session store, refresh token repository and API key repository. It mirrors
// setupUserRouter but allows testing the revocation integration.
func setupUserRouterFull(
	userRepo *MockUserRepository,
	callerID, callerRole string,
	store *mockSessionStore,
	refreshRepo *MockRefreshTokenRepository,
	apiKeyRepo *MockAPIKeyRepository,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))
	// Convert typed nil pointers to nil interfaces so the handler skips the step.
	var rtRepo models.RefreshTokenRepository
	if refreshRepo != nil {
		rtRepo = refreshRepo
	}
	var akRepo models.APIKeyRepository
	if apiKeyRepo != nil {
		akRepo = apiKeyRepo
	}
	seedCallerAdmin(userRepo, callerID, callerRole)
	h := NewUserHandler(userRepo, rtRepo, akRepo)
	if store != nil {
		h.SetSessionStore(store)
	}
	h.SetAccessTokenExpiration(15 * time.Minute)
	adminMW := middleware.RequireAdmin()
	users := r.Group("/api/v1/users")
	{
		users.GET("", adminMW, h.ListUsers)
		users.DELETE("/:id", adminMW, h.DeleteUser)
		users.PUT("/:id/disable", adminMW, h.DisableUser)
		users.PUT("/:id/enable", adminMW, h.EnableUser)
		users.PUT("/:id/password", adminMW, h.ResetUserPassword)
		users.PUT("/:id/role", adminMW, h.ChangeUserRole)
	}
	return r
}

// ---- TestDisableUser_BlocklistCalled ----

func TestDisableUser_BlocklistCalled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		withStore        bool
		withRefreshRepo  bool
		wantStatus       int
		wantBlockCalled  bool
		wantRevokeCalled bool
	}{
		{
			name:             "disable calls BlockUser and RevokeAllForUser",
			withStore:        true,
			withRefreshRepo:  true,
			wantStatus:       http.StatusOK,
			wantBlockCalled:  true,
			wantRevokeCalled: true,
		},
		{
			name:       "disable with nil session store succeeds",
			withStore:  false,
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			const targetID = "user-target"
			userRepo := NewMockUserRepository()
			seedUser(t, userRepo, targetID, "target-user", "testpass", "user")

			var store *mockSessionStore
			if tt.withStore {
				store = newMockHandlerSessionStore()
			}

			refreshRepo := NewMockRefreshTokenRepository()
			rt := &models.RefreshToken{
				ID:        "rt-1",
				UserID:    targetID,
				TokenHash: "somehash",
				ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
			}
			require.NoError(t, refreshRepo.Create(rt))

			var refreshArg *MockRefreshTokenRepository
			if tt.withRefreshRepo {
				refreshArg = refreshRepo
			}

			router := setupUserRouterFull(userRepo, "admin-1", "admin", store, refreshArg, nil)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/users/"+targetID+"/disable", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			if tt.wantBlockCalled {
				require.NotNil(t, store)
				assert.True(t, store.wasBlockUserCalledFor(targetID),
					"BlockUser should be called for the disabled user ID")
			}

			if tt.wantRevokeCalled {
				// Verify RevokeAllForUser was called by checking the seeded token's state.
				refreshRepo.mu.RLock()
				storedToken := refreshRepo.tokens["rt-1"]
				refreshRepo.mu.RUnlock()
				require.NotNil(t, storedToken)
				assert.True(t, storedToken.Revoked, "refresh tokens should be revoked on disable")
			}
		})
	}
}

// ---- TestEnableUser_ReblocksInsteadOfUnblock ----

// Enable must not make tokens issued before the disable valid again: it writes
// a fresh block (BlockUser) and never calls UnblockUser.
func TestEnableUser_ReblocksInsteadOfUnblock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		withStore  bool
		wantStatus int
	}{
		{name: "enable calls BlockUser, not UnblockUser", withStore: true, wantStatus: http.StatusOK},
		{name: "enable with nil session store succeeds", withStore: false, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			const targetID = "user-disabled"
			userRepo := NewMockUserRepository()
			u := seedUser(t, userRepo, targetID, "disabled-user", "testpass", "user")
			u.Disabled = true
			require.NoError(t, userRepo.Update(u))

			var store *mockSessionStore
			if tt.withStore {
				store = newMockHandlerSessionStore()
			}

			router := setupUserRouterFull(userRepo, "admin-1", "admin", store, nil, nil)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/users/"+targetID+"/enable", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			got, err := userRepo.FindByID(targetID)
			require.NoError(t, err)
			assert.False(t, got.Disabled)

			if tt.withStore {
				assert.True(t, store.wasBlockUserCalledFor(targetID), "enable must re-block old tokens")
				assert.False(t, store.wasUnblockUserCalledFor(targetID), "enable must not unblock")
			}
		})
	}
}
