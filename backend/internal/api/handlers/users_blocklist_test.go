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
// session store and refresh token repository. It mirrors setupUserRouter but allows
// testing the blocklist integration.
func setupUserRouterFull(
	userRepo *MockUserRepository,
	callerID, callerRole string,
	store *mockSessionStore,
	refreshRepo *MockRefreshTokenRepository,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))
	h := NewUserHandler(userRepo)
	if store != nil {
		h.SetSessionStore(store)
	}
	if refreshRepo != nil {
		h.SetRefreshTokenRepo(refreshRepo)
		h.SetAccessTokenExpiration(15 * time.Minute)
	}
	adminMW := middleware.RequireAdmin()
	users := r.Group("/api/v1/users")
	{
		users.GET("", adminMW, h.ListUsers)
		users.DELETE("/:id", adminMW, h.DeleteUser)
		users.PUT("/:id/disable", adminMW, h.DisableUser)
		users.PUT("/:id/enable", adminMW, h.EnableUser)
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

			router := setupUserRouterFull(userRepo, "admin-1", "admin", store, refreshArg)
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

// ---- TestEnableUser_UnblockCalled ----

func TestEnableUser_UnblockCalled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		withStore         bool
		wantStatus        int
		wantUnblockCalled bool
	}{
		{
			name:              "enable calls UnblockUser",
			withStore:         true,
			wantStatus:        http.StatusOK,
			wantUnblockCalled: true,
		},
		{
			name:       "enable with nil session store succeeds",
			withStore:  false,
			wantStatus: http.StatusOK,
		},
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

			router := setupUserRouterFull(userRepo, "admin-1", "admin", store, nil)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/users/"+targetID+"/enable", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			if tt.wantUnblockCalled {
				require.NotNil(t, store)
				assert.True(t, store.wasUnblockUserCalledFor(targetID),
					"UnblockUser should be called for the re-enabled user ID")
			}
		})
	}
}
