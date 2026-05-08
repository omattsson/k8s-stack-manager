package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/config"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

const (
	testJWTSecret = "test-secret-key"
)

// testAuthConfig returns a minimal AuthConfig for testing.
func testAuthConfig(selfReg bool) *config.AuthConfig {
	return &config.AuthConfig{
		JWTSecret:        testJWTSecret,
		JWTExpiration:    time.Hour,
		SelfRegistration: selfReg,
	}
}

// injectAuthContext returns middleware that sets userID and role into the Gin context.
// This simulates what AuthRequired does after validating a real JWT.
func injectAuthContext(userID, role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if userID != "" {
			c.Set("userID", userID)
		}
		if role != "" {
			c.Set("role", role)
		}
		c.Next()
	}
}

// setupAuthRouter creates a gin engine with the AuthHandler routes plus optional
// context-injecting middleware to simulate prior JWT validation.
func setupAuthRouter(userRepo *MockUserRepository, selfReg bool, callerUserID, callerRole string) (*gin.Engine, *MockUserRepository) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(injectAuthContext(callerUserID, callerRole))

	h := NewAuthHandler(userRepo, testAuthConfig(selfReg), &config.OIDCConfig{})

	auth := r.Group("/api/v1/auth")
	{
		auth.POST("/login", h.Login)
		auth.POST("/register", h.Register)
		auth.GET("/me", h.GetCurrentUser)
	}
	return r, userRepo
}

// hashPassword is a helper that creates a bcrypt hash for test user passwords.
func hashPassword(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	return string(hash)
}

// seedUser inserts a user into the mock repo and returns it.
func seedUser(t *testing.T, repo *MockUserRepository, id, username, password, role string) *models.User {
	t.Helper()
	u := &models.User{
		ID:           id,
		Username:     username,
		PasswordHash: hashPassword(t, password),
		DisplayName:  username,
		Role:         role,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	require.NoError(t, repo.Create(u))
	return u
}

// ---- Login Tests ----

func TestLogin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		setup      func(*MockUserRepository)
		wantStatus int
		wantToken  bool
	}{
		{
			name: "valid credentials",
			body: `{"username":"alice","password":"secret"}`,
			setup: func(repo *MockUserRepository) {
				seedUser(t, repo, "uid-1", "alice", "secret", "user")
			},
			wantStatus: http.StatusOK,
			wantToken:  true,
		},
		{
			name:       "user not found",
			body:       `{"username":"nobody","password":"secret"}`,
			setup:      func(_ *MockUserRepository) {},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "wrong password",
			body: `{"username":"alice","password":"wrong"}`,
			setup: func(repo *MockUserRepository) {
				seedUser(t, repo, "uid-2", "alice", "secret", "user")
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid JSON",
			body:       `{bad json`,
			setup:      func(_ *MockUserRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing username field",
			body:       `{"password":"secret"}`,
			setup:      func(_ *MockUserRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing password field",
			body:       `{"username":"alice"}`,
			setup:      func(_ *MockUserRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "disabled user gets 403",
			body: `{"username":"alice","password":"secret"}`,
			setup: func(repo *MockUserRepository) {
				u := seedUser(t, repo, "uid-dis", "alice", "secret", "user")
				u.Disabled = true
				_ = repo.Update(u)
			},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := NewMockUserRepository()
			tt.setup(repo)
			router, _ := setupAuthRouter(repo, true, "", "")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantToken {
				var resp map[string]interface{}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.NotEmpty(t, resp["token"])
				userObj, ok := resp["user"].(map[string]interface{})
				require.True(t, ok, "response should have user object")
				assert.Equal(t, "alice", userObj["username"])
				// Password hash must not be in response.
				_, hasHash := userObj["password_hash"]
				assert.False(t, hasHash, "password_hash must not be exposed in login response")
			}
		})
	}
}

// ---- Register Tests ----

func TestRegister(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		selfReg    bool
		callerRole string
		setup      func(*MockUserRepository)
		wantStatus int
		wantRole   string
	}{
		{
			name:       "self-registration enabled — creates user role",
			body:       `{"username":"bob","password":"pass123","display_name":"Bob"}`,
			selfReg:    true,
			callerRole: "",
			setup:      func(_ *MockUserRepository) {},
			wantStatus: http.StatusCreated,
			wantRole:   "user",
		},
		{
			name:       "admin creates user with admin role",
			body:       `{"username":"newadmin","password":"pass123","role":"admin"}`,
			selfReg:    false,
			callerRole: "admin",
			setup:      func(_ *MockUserRepository) {},
			wantStatus: http.StatusCreated,
			wantRole:   "admin",
		},
		{
			name:       "non-admin cannot escalate role — becomes user",
			body:       `{"username":"attacker","password":"pass123","role":"admin"}`,
			selfReg:    true,
			callerRole: "user",
			setup:      func(_ *MockUserRepository) {},
			wantStatus: http.StatusCreated,
			wantRole:   "user",
		},
		{
			name:       "self-registration disabled, non-admin rejected",
			body:       `{"username":"bob","password":"pass123"}`,
			selfReg:    false,
			callerRole: "",
			setup:      func(_ *MockUserRepository) {},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "self-registration disabled, admin can register",
			body:       `{"username":"newuser","password":"pass123"}`,
			selfReg:    false,
			callerRole: "admin",
			setup:      func(_ *MockUserRepository) {},
			wantStatus: http.StatusCreated,
		},
		{
			name:       "invalid JSON",
			body:       `{bad json`,
			selfReg:    true,
			callerRole: "",
			setup:      func(_ *MockUserRepository) {},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "duplicate username returns 409",
			body:       `{"username":"alice","password":"pass123"}`,
			selfReg:    true,
			callerRole: "",
			setup: func(repo *MockUserRepository) {
				seedUser(t, repo, "existing-id", "alice", "oldpass", "user")
			},
			wantStatus: http.StatusConflict,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := NewMockUserRepository()
			tt.setup(repo)
			router, _ := setupAuthRouter(repo, tt.selfReg, "caller-id", tt.callerRole)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantRole != "" && w.Code == http.StatusCreated {
				var resp models.User
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, tt.wantRole, resp.Role)
				assert.Empty(t, resp.PasswordHash, "password hash must not be in response")
			}
		})
	}
}

// ---- GetCurrentUser Tests ----

func TestGetCurrentUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		callerID   string
		setup      func(*MockUserRepository)
		wantStatus int
		wantUser   bool
	}{
		{
			name:     "authenticated user gets own profile",
			callerID: "uid-1",
			setup: func(repo *MockUserRepository) {
				seedUser(t, repo, "uid-1", "alice", "secret", "admin")
			},
			wantStatus: http.StatusOK,
			wantUser:   true,
		},
		{
			name:       "no auth context returns 401",
			callerID:   "",
			setup:      func(_ *MockUserRepository) {},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:     "user not found in repository",
			callerID: "uid-missing",
			setup: func(repo *MockUserRepository) {
				// Seed no user with that ID.
				repo.SetFindError(nil) // keep default not-found behaviour
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:     "repository error returns 500",
			callerID: "uid-1",
			setup: func(repo *MockUserRepository) {
				repo.SetFindError(errInternal)
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := NewMockUserRepository()
			tt.setup(repo)
			router, _ := setupAuthRouter(repo, true, tt.callerID, "user")

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantUser {
				var resp models.User
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "alice", resp.Username)
				assert.Empty(t, resp.PasswordHash, "password hash must not be exposed")
			}
		})
	}
}

// errInternal is a generic non-sentinel error to force 500 paths.
var errInternal = &internalError{}

type internalError struct{}

func (e *internalError) Error() string { return "unexpected db failure" }
