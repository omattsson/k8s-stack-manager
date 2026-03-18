package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupUserRouter creates a gin engine wired to UserHandler routes, mirroring the production
// route configuration: RequireAdmin middleware gates both GET /users and DELETE /users/:id.
func setupUserRouter(userRepo *MockUserRepository, callerID, callerRole string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerID, callerRole))
	h := NewUserHandler(userRepo)
	adminMW := middleware.RequireAdmin()
	users := r.Group("/api/v1/users")
	{
		users.GET("", adminMW, h.ListUsers)
		users.DELETE("/:id", adminMW, h.DeleteUser)
	}
	return r
}

// ---- TestListUsers ----

func TestListUsers(t *testing.T) {
	t.Parallel()

	type tc struct {
		name       string
		callerRole string
		seedUsers  []models.User
		wantStatus int
		wantLen    int
	}

	tests := []tc{
		{
			name:       "admin gets 200 with user list",
			callerRole: "admin",
			seedUsers: []models.User{
				{ID: "u1", Username: "alice", Role: "user"},
				{ID: "u2", Username: "bob", Role: "devops"},
			},
			wantStatus: http.StatusOK,
			wantLen:    2,
		},
		{
			name:       "non-admin gets 403",
			callerRole: "user",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "unauthenticated gets 401",
			callerRole: "",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			userRepo := NewMockUserRepository()
			for i := range tt.seedUsers {
				u := tt.seedUsers[i]
				require.NoError(t, userRepo.Create(&u))
			}

			router := setupUserRouter(userRepo, "admin-caller", tt.callerRole)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/users", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusOK {
				var users []models.User
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &users))
				assert.Len(t, users, tt.wantLen)
				// Verify PasswordHash is never returned.
				var raw []map[string]interface{}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
				for _, u := range raw {
					_, hasHash := u["password_hash"]
					assert.False(t, hasHash, "password_hash must never appear in the response")
				}
			}
		})
	}
}

// ---- TestDeleteUser ----

func TestDeleteUser(t *testing.T) {
	t.Parallel()

	type tc struct {
		name       string
		callerID   string
		callerRole string
		targetID   string
		seedTarget bool
		wantStatus int
	}

	tests := []tc{
		{
			name:       "admin deletes other user - returns 204",
			callerID:   "admin-1",
			callerRole: "admin",
			targetID:   "user-1",
			seedTarget: true,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "admin cannot delete own account - returns 400",
			callerID:   "admin-1",
			callerRole: "admin",
			targetID:   "admin-1",
			seedTarget: true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "non-admin gets 403",
			callerID:   "user-1",
			callerRole: "user",
			targetID:   "user-2",
			seedTarget: true,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "target user not found returns 404",
			callerID:   "admin-1",
			callerRole: "admin",
			targetID:   "ghost-user",
			seedTarget: false,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			userRepo := NewMockUserRepository()
			if tt.seedTarget {
				seedUser(t, userRepo, tt.targetID, "target-user", "testpass", "user")
			}

			router := setupUserRouter(userRepo, tt.callerID, tt.callerRole)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodDelete, "/api/v1/users/"+tt.targetID, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
		})
	}
}
