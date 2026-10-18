package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/config"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// changeRoleSchema is the JSON schema of a successful role change response.
const changeRoleSchema = `{
"type": "object",
"required": ["id", "old_role", "new_role", "changed", "message"],
"properties": {
"id": {"type": "string"},
"old_role": {"type": "string"},
"new_role": {"type": "string", "enum": ["user", "devops", "admin"]},
"changed": {"type": "boolean"},
"message": {"type": "string"}
}
}`

// TestChangeUserRole covers the role validation, the self-change rule, the
// SSO rule, the last-admin rule and the no-op.
func TestChangeUserRole(t *testing.T) {
	t.Parallel()

	const callerID = "admin-caller"

	tests := []struct {
		name        string
		callerID    string
		callerRole  string
		targetID    string
		body        string
		seed        func(t *testing.T, repo *MockUserRepository)
		updateErr   error
		wantStatus  int
		wantError   string
		wantRole    string // role of the target after the request; "" = skip
		wantChanged bool
		wantRevoked bool
		// wantCanonicalID is the stored ID when targetID differs from it in case.
		wantCanonicalID string
	}{
		{
			name: "local user to devops", callerID: callerID, callerRole: "admin",
			targetID: "u1", body: `{"role":"devops"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "u1", "user", "local", false)
			},
			wantStatus: http.StatusOK, wantRole: "devops", wantChanged: true, wantRevoked: true,
		},
		{
			name: "empty auth provider counts as local", callerID: callerID, callerRole: "admin",
			targetID: "u1", body: `{"role":"admin"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "u1", "user", "", false)
			},
			wantStatus: http.StatusOK, wantRole: "admin", wantChanged: true, wantRevoked: true,
		},
		{
			name: "unchanged role is a no-op", callerID: callerID, callerRole: "admin",
			targetID: "u1", body: `{"role":"devops"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "u1", "devops", "local", false)
			},
			wantStatus: http.StatusOK, wantRole: "devops", wantChanged: false, wantRevoked: false,
		},
		{
			name: "demote admin while another enabled admin exists", callerID: callerID, callerRole: "admin",
			targetID: "a1", body: `{"role":"user"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "a1", "admin", "local", false)
				seedRoleUser(t, repo, "a2", "admin", "local", false)
			},
			wantStatus: http.StatusOK, wantRole: "user", wantChanged: true, wantRevoked: true,
		},
		{
			name: "last-admin error from the repository gives 409", callerID: callerID, callerRole: "admin",
			targetID: "a1", body: `{"role":"devops"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "a1", "admin", "local", false)
			},
			updateErr:  models.ErrLastAdmin,
			wantStatus: http.StatusConflict, wantError: msgLastAdmin, wantRole: "admin",
		},
		{
			name: "caller who is no longer an enabled admin gets 403", callerID: callerID, callerRole: "admin",
			targetID: "u1", body: `{"role":"admin"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, callerID, "admin", "local", true) // disabled after the token was issued
				seedRoleUser(t, repo, "u1", "user", "local", false)
			},
			wantStatus: http.StatusForbidden, wantError: msgCallerNotAdmin, wantRole: "user",
		},
		{
			name: "caller demoted after the token was issued gets 403", callerID: callerID, callerRole: "admin",
			targetID: "u1", body: `{"role":"devops"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, callerID, "devops", "local", false)
				seedRoleUser(t, repo, "u1", "user", "local", false)
			},
			wantStatus: http.StatusForbidden, wantError: msgCallerNotAdmin, wantRole: "user",
		},
		{
			name: "own role cannot be changed", callerID: "a1", callerRole: "admin",
			targetID: "a1", body: `{"role":"user"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "a1", "admin", "local", false)
				seedRoleUser(t, repo, "a2", "admin", "local", false)
			},
			wantStatus: http.StatusForbidden, wantError: msgOwnRole, wantRole: "admin",
		},
		{
			name: "own role in another case is refused as self", callerID: "admin-a1", callerRole: "admin",
			targetID: "ADMIN-A1", body: `{"role":"user"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "admin-a1", "admin", "local", false)
				seedRoleUser(t, repo, "a2", "admin", "local", false)
			},
			wantStatus: http.StatusForbidden, wantError: msgOwnRole,
		},
		{
			name: "other user in another case uses the stored ID", callerID: callerID, callerRole: "admin",
			targetID: "USER-U9", body: `{"role":"devops"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "user-u9", "user", "local", false)
			},
			wantStatus: http.StatusOK, wantRole: "devops", wantChanged: true, wantRevoked: true,
			wantCanonicalID: "user-u9",
		},
		{
			name: "SSO user role is managed by the identity provider", callerID: callerID, callerRole: "admin",
			targetID: "s1", body: `{"role":"devops"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "s1", "user", "oidc", false)
			},
			wantStatus: http.StatusConflict, wantError: msgRoleManagedByIdP, wantRole: "user",
		},
		{
			name: "unknown role", callerID: callerID, callerRole: "admin",
			targetID: "u1", body: `{"role":"superuser"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "u1", "user", "local", false)
			},
			wantStatus: http.StatusBadRequest, wantError: msgInvalidRole, wantRole: "user",
		},
		{
			name: "role with wrong case", callerID: callerID, callerRole: "admin",
			targetID: "u1", body: `{"role":"Admin"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "u1", "user", "local", false)
			},
			wantStatus: http.StatusBadRequest, wantError: msgInvalidRole, wantRole: "user",
		},
		{
			name: "missing role", callerID: callerID, callerRole: "admin",
			targetID: "u1", body: `{}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "u1", "user", "local", false)
			},
			wantStatus: http.StatusBadRequest, wantError: msgInvalidRequestFormat, wantRole: "user",
		},
		{
			name: "malformed JSON", callerID: callerID, callerRole: "admin",
			targetID: "u1", body: `{"role":`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "u1", "user", "local", false)
			},
			wantStatus: http.StatusBadRequest, wantError: msgInvalidRequestFormat, wantRole: "user",
		},
		{
			name: "unknown user", callerID: callerID, callerRole: "admin",
			targetID: "ghost", body: `{"role":"devops"}`,
			seed:       func(t *testing.T, repo *MockUserRepository) {},
			wantStatus: http.StatusNotFound, wantError: "User not found",
		},
		{
			name: "non-admin caller", callerID: "u2", callerRole: "devops",
			targetID: "u1", body: `{"role":"admin"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "u1", "user", "local", false)
			},
			wantStatus: http.StatusForbidden, wantRole: "user",
		},
		{
			name: "repository error is not exposed", callerID: callerID, callerRole: "admin",
			targetID: "u1", body: `{"role":"devops"}`,
			seed: func(t *testing.T, repo *MockUserRepository) {
				seedRoleUser(t, repo, "u1", "user", "local", false)
			},
			updateErr:  errors.New("dial tcp 10.0.0.5:3306: connection refused"),
			wantStatus: http.StatusInternalServerError, wantError: msgInternalServerError, wantRole: "user",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newRevokeFixture(t)
			userRepo := NewMockUserRepository()
			tt.seed(t, userRepo)
			stored := tt.targetID
			if tt.wantCanonicalID != "" {
				stored = tt.wantCanonicalID
			}
			if tt.updateErr != nil {
				userRepo.mu.Lock()
				userRepo.updateErr = tt.updateErr
				userRepo.mu.Unlock()
			}
			// Refresh tokens of the target, to check the revocation.
			require.NoError(t, f.refreshRepo.Create(&models.RefreshToken{
				ID: "rt-role-target", UserID: stored, TokenHash: "hr", ExpiresAt: time.Now().Add(time.Hour),
			}))

			router := setupUserRouterFull(userRepo, tt.callerID, tt.callerRole, f.store, f.refreshRepo, f.apiKeyRepo)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/users/"+tt.targetID+"/role", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())

			if tt.wantStatus == http.StatusOK {
				assert.True(t, validateJSONSchema(t, changeRoleSchema, w.Body.Bytes()))
				var resp ChangeRoleResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, stored, resp.ID)
				assert.Equal(t, tt.wantChanged, resp.Changed)
				assert.Equal(t, tt.wantRole, resp.NewRole)
			} else {
				assert.True(t, validateJSONSchema(t, errorSchema, w.Body.Bytes()))
				if tt.wantError != "" {
					var resp map[string]string
					require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
					assert.Equal(t, tt.wantError, resp["error"])
				}
			}

			if tt.wantRole != "" {
				u, err := userRepo.FindByID(stored)
				require.NoError(t, err)
				assert.Equal(t, tt.wantRole, u.Role)
			}

			assert.Equal(t, tt.wantRevoked, f.store.wasBlockUserCalledFor(stored), "BlockUser")
			assert.Equal(t, tt.wantRevoked, f.tokenRevoked("rt-role-target"), "refresh token revoked")
		})
	}
}

// TestChangeUserRole_KeepsAPIKeys checks that a role change keeps the API
// keys of the user: API-key auth reads the current role from the database.
func TestChangeUserRole_KeepsAPIKeys(t *testing.T) {
	t.Parallel()

	f := newRevokeFixture(t)
	router := setupUserRouterFull(f.userRepo, "admin-1", "admin", f.store, f.refreshRepo, f.apiKeyRepo)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/users/"+revokeTargetID+"/role", bytes.NewBufferString(`{"role":"devops"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.ElementsMatch(t, []string{"key-target-1", "key-target-2"}, f.apiKeyIDs(t, revokeTargetID))
}

// seedRoleUser adds a user without a password hash (bcrypt is not needed).
func seedRoleUser(t *testing.T, repo *MockUserRepository, id, role, provider string, disabled bool) {
	t.Helper()
	require.NoError(t, repo.Create(&models.User{
		ID: id, Username: id + "-name", Role: role, AuthProvider: provider, Disabled: disabled,
	}))
}

// TestLogin_CacheUsesCurrentRole checks that a login cache hit does not
// reuse the cached user record: after a role change within LOGIN_CACHE_TTL,
// the next login gets a token with the new role.
func TestLogin_CacheUsesCurrentRole(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	userRepo := NewMockUserRepository()
	seedUser(t, userRepo, "u1", "alice", "testpass1", "user")
	cfg := testAuthConfig(false)
	cfg.LoginCacheTTL = time.Minute
	h := NewAuthHandler(userRepo, cfg, &config.OIDCConfig{})
	r := gin.New()
	r.POST("/api/v1/auth/login", h.Login)

	login := func() *middleware.Claims {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"alice","password":"testpass1"}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var resp LoginResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		claims, err := middleware.ValidateJWT(resp.Token, testJWTSecret)
		require.NoError(t, err)
		return claims
	}

	assert.Equal(t, "user", login().Role)
	seedRoleUser(t, userRepo, "admin-x", "admin", "local", false)
	_, err := userRepo.UpdateRole("admin-x", "u1", "devops")
	require.NoError(t, err)
	assert.Equal(t, "devops", login().Role, "cache hit must use the current role")
}

// TestGuardedUserChanges covers delete, disable and role change: the
// self-check with the stored ID (IDs compare without case, as on MySQL), the
// caller re-check in the repository and the last-admin rule.
func TestGuardedUserChanges(t *testing.T) {
	t.Parallel()

	type op struct {
		name, method, suffix, body string
		okStatus, selfStatus       int
	}
	ops := []op{
		{name: "delete", method: http.MethodDelete, okStatus: http.StatusNoContent, selfStatus: http.StatusBadRequest},
		{name: "disable", method: http.MethodPut, suffix: "/disable", okStatus: http.StatusOK, selfStatus: http.StatusBadRequest},
		{name: "role change", method: http.MethodPut, suffix: "/role", body: `{"role":"devops"}`, okStatus: http.StatusOK, selfStatus: http.StatusForbidden},
	}

	tests := []struct {
		name        string
		pathID      string
		callerSeed  *models.User // nil: enabled admin "admin-c"
		repoErr     error
		wantSelf    bool
		wantStatus  int // 0: the op's okStatus
		wantRevoked string
	}{
		{name: "own ID in upper case is refused as self", pathID: "ADMIN-C", wantSelf: true},
		{name: "other user in upper case uses the stored ID", pathID: "USER-T", wantRevoked: "user-t"},
		{name: "caller disabled after the token was issued", pathID: "user-t",
			callerSeed: &models.User{ID: "admin-c", Username: "c", Role: "admin", Disabled: true}, wantStatus: http.StatusForbidden},
		{name: "caller demoted after the token was issued", pathID: "user-t",
			callerSeed: &models.User{ID: "admin-c", Username: "c", Role: "user"}, wantStatus: http.StatusForbidden},
		{name: "last-admin rule gives 409", pathID: "user-t", repoErr: models.ErrLastAdmin, wantStatus: http.StatusConflict},
		{name: "unknown user", pathID: "ghost", wantStatus: http.StatusNotFound},
	}

	for _, o := range ops {
		for _, tt := range tests {
			o, tt := o, tt
			t.Run(o.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				f := newRevokeFixture(t)
				userRepo := NewMockUserRepository()
				caller := tt.callerSeed
				if caller == nil {
					caller = &models.User{ID: "admin-c", Username: "c", Role: "admin"}
				}
				require.NoError(t, userRepo.Create(caller))
				seedRoleUser(t, userRepo, "user-t", "user", "local", false)
				if tt.repoErr != nil {
					userRepo.mu.Lock()
					userRepo.updateErr = tt.repoErr
					userRepo.mu.Unlock()
				}
				router := setupUserRouterFull(userRepo, "admin-c", "admin", f.store, f.refreshRepo, f.apiKeyRepo)

				w := httptest.NewRecorder()
				req, _ := http.NewRequest(o.method, "/api/v1/users/"+tt.pathID+o.suffix, bytes.NewBufferString(o.body))
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(w, req)

				want := tt.wantStatus
				if tt.wantSelf {
					want = o.selfStatus
				} else if want == 0 {
					want = o.okStatus
				}
				require.Equal(t, want, w.Code, w.Body.String())
				if want >= http.StatusBadRequest {
					assert.True(t, validateJSONSchema(t, errorSchema, w.Body.Bytes()))
					assert.False(t, f.store.wasBlockUserCalledFor("user-t"))
					assert.False(t, f.store.wasBlockUserCalledFor("admin-c"))
					u, err := userRepo.FindByID("user-t")
					require.NoError(t, err)
					assert.False(t, u.Disabled)
					assert.Equal(t, "user", u.Role)
				}
				if tt.wantRevoked != "" {
					assert.True(t, f.store.wasBlockUserCalledFor(tt.wantRevoked), "revoke must use the stored ID")
					assert.False(t, f.store.wasBlockUserCalledFor(tt.pathID))
				}
			})
		}
	}
}

// changeDuringLoginRepo applies a change to the user record between the
// username lookup and the re-read after the password check.
type changeDuringLoginRepo struct {
	*MockUserRepository
	change func(u *models.User)
}

func (r *changeDuringLoginRepo) FindByID(id string) (*models.User, error) {
	u, err := r.MockUserRepository.FindByID(id)
	if err == nil && r.change != nil {
		r.change(u)
	}
	return u, err
}

// TestLogin_UsesRecordReadAfterPasswordCheck checks that the login re-reads
// the user after bcrypt and uses that record for the checks and the token.
func TestLogin_UsesRecordReadAfterPasswordCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		change     func(u *models.User)
		wantStatus int
		wantRole   string
	}{
		{name: "unchanged", wantStatus: http.StatusOK, wantRole: "user"},
		{name: "role changed during the login", change: func(u *models.User) { u.Role = "devops" }, wantStatus: http.StatusOK, wantRole: "devops"},
		{name: "disabled during the login", change: func(u *models.User) { u.Disabled = true }, wantStatus: http.StatusForbidden},
		{name: "password reset during the login", change: func(u *models.User) { u.PasswordHash = "other-hash" }, wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)
			mock := NewMockUserRepository()
			seedUser(t, mock, "u1", "alice", "testpass1", "user")
			h := NewAuthHandler(&changeDuringLoginRepo{MockUserRepository: mock, change: tt.change}, testAuthConfig(false), &config.OIDCConfig{})
			r := gin.New()
			r.POST("/api/v1/auth/login", h.Login)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"alice","password":"testpass1"}`))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus == http.StatusOK {
				var resp LoginResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				claims, err := middleware.ValidateJWT(resp.Token, testJWTSecret)
				require.NoError(t, err)
				assert.Equal(t, tt.wantRole, claims.Role)
			}
		})
	}
}
