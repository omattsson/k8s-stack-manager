package handlers

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/ctxutil"
	"backend/internal/models"
	"backend/internal/sessionstore"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	revokeTargetID = "user-target"
	revokeOtherID  = "user-other"
)

// revokeFixture holds the repositories seeded for the revocation tests.
type revokeFixture struct {
	userRepo    *MockUserRepository
	store       *mockSessionStore
	refreshRepo *MockRefreshTokenRepository
	apiKeyRepo  *MockAPIKeyRepository
}

// newRevokeFixture seeds a target user and a bystander user, each with refresh
// tokens and API keys, so tests can check that only the target loses access.
func newRevokeFixture(t *testing.T) *revokeFixture {
	t.Helper()
	f := &revokeFixture{
		userRepo:    NewMockUserRepository(),
		store:       newMockHandlerSessionStore(),
		refreshRepo: NewMockRefreshTokenRepository(),
		apiKeyRepo:  NewMockAPIKeyRepository(),
	}
	seedUser(t, f.userRepo, revokeTargetID, "alice", "testpass1", "user")
	seedUser(t, f.userRepo, revokeOtherID, "bob", "testpass2", "user")

	exp := time.Now().Add(7 * 24 * time.Hour)
	for _, rt := range []*models.RefreshToken{
		{ID: "rt-target-1", UserID: revokeTargetID, TokenHash: "h1", ExpiresAt: exp},
		{ID: "rt-target-2", UserID: revokeTargetID, TokenHash: "h2", ExpiresAt: exp},
		{ID: "rt-other-1", UserID: revokeOtherID, TokenHash: "h3", ExpiresAt: exp},
	} {
		require.NoError(t, f.refreshRepo.Create(context.Background(), rt))
	}
	for _, k := range []*models.APIKey{
		{ID: "key-target-1", UserID: revokeTargetID, Name: "ci", Prefix: "aaaaaaaaaaaaaaaa"},
		{ID: "key-target-2", UserID: revokeTargetID, Name: "cli", Prefix: "bbbbbbbbbbbbbbbb"},
		{ID: "key-other-1", UserID: revokeOtherID, Name: "ci", Prefix: "cccccccccccccccc"},
	} {
		require.NoError(t, f.apiKeyRepo.Create(context.Background(), k))
	}
	return f
}

func (f *revokeFixture) tokenRevoked(id string) bool {
	f.refreshRepo.mu.RLock()
	defer f.refreshRepo.mu.RUnlock()
	tok, ok := f.refreshRepo.tokens[id]
	return ok && tok.Revoked
}

func (f *revokeFixture) apiKeyIDs(t *testing.T, userID string) []string {
	t.Helper()
	keys, err := f.apiKeyRepo.ListByUser(context.Background(), userID)
	require.NoError(t, err)
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	return ids
}

// TestRevokeUserAccess_AllPaths checks that delete, disable, reset-password
// and role change all use revokeUserAccess: block in the session store and revoke all refresh
// tokens of the target user, and only of that user. Only delete removes the
// API keys; disable and reset-password keep them. A client cancel does not
// stop the revocation (the mock API key repository fails on a done ctx).
func TestRevokeUserAccess_AllPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		method          string
		path            string
		body            string
		wantStatus      int
		wantUserExists  bool
		wantKeysDeleted bool
		cancelled       bool // the request context is cancelled before the call
	}{
		{
			name:            "delete user",
			method:          http.MethodDelete,
			path:            "/api/v1/users/" + revokeTargetID,
			wantStatus:      http.StatusNoContent,
			wantUserExists:  false,
			wantKeysDeleted: true,
		},
		{
			name:            "delete user with a cancelled request",
			method:          http.MethodDelete,
			path:            "/api/v1/users/" + revokeTargetID,
			wantStatus:      http.StatusNoContent,
			wantUserExists:  false,
			wantKeysDeleted: true,
			cancelled:       true,
		},
		{
			name:           "disable user",
			method:         http.MethodPut,
			path:           "/api/v1/users/" + revokeTargetID + "/disable",
			wantStatus:     http.StatusOK,
			wantUserExists: true,
		},
		{
			name:           "reset password",
			method:         http.MethodPut,
			path:           "/api/v1/users/" + revokeTargetID + "/password",
			body:           `{"password":"a-new-password-1"}`,
			wantStatus:     http.StatusOK,
			wantUserExists: true,
		},
		{
			name:           "change role",
			method:         http.MethodPut,
			path:           "/api/v1/users/" + revokeTargetID + "/role",
			body:           `{"role":"devops"}`,
			wantStatus:     http.StatusOK,
			wantUserExists: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newRevokeFixture(t)
			router := setupUserRouterFull(f.userRepo, "admin-1", "admin", f.store, f.refreshRepo, f.apiKeyRepo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.body))
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			if tt.cancelled {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				req = req.WithContext(ctx)
			}
			router.ServeHTTP(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())

			_, err := f.userRepo.FindByID(context.Background(), revokeTargetID)
			assert.Equal(t, tt.wantUserExists, err == nil)

			// Access tokens: user is blocked in the session store.
			assert.True(t, f.store.wasBlockUserCalledFor(revokeTargetID), "BlockUser must be called")
			assert.False(t, f.store.wasBlockUserCalledFor(revokeOtherID))

			// Refresh tokens: all of the target revoked, bystander untouched.
			assert.True(t, f.tokenRevoked("rt-target-1"))
			assert.True(t, f.tokenRevoked("rt-target-2"))
			assert.False(t, f.tokenRevoked("rt-other-1"))

			// API keys: deleted only on user delete; bystander always untouched.
			if tt.wantKeysDeleted {
				assert.Empty(t, f.apiKeyIDs(t, revokeTargetID))
			} else {
				assert.ElementsMatch(t, []string{"key-target-1", "key-target-2"}, f.apiKeyIDs(t, revokeTargetID))
			}
			assert.Equal(t, []string{"key-other-1"}, f.apiKeyIDs(t, revokeOtherID))
		})
	}
}

// TestRevokeUserAccess_ErrorPolicy checks the log-and-continue policy: a
// failing step does not stop the other steps and does not fail the request.
func TestRevokeUserAccess_ErrorPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		refreshErr      bool
		apiKeyDeleteErr bool
		wantKeysDeleted bool
		wantRTRevoked   bool
	}{
		{
			name:            "refresh token revoke fails",
			refreshErr:      true,
			wantKeysDeleted: true,
			wantRTRevoked:   false,
		},
		{
			name:            "API key delete fails",
			apiKeyDeleteErr: true,
			wantKeysDeleted: false,
			wantRTRevoked:   true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newRevokeFixture(t)
			if tt.refreshErr {
				f.refreshRepo.revokeAllErr = errors.New("db down")
			}
			if tt.apiKeyDeleteErr {
				f.apiKeyRepo.SetDeleteError(errors.New("db down"))
			}
			router := setupUserRouterFull(f.userRepo, "admin-1", "admin", f.store, f.refreshRepo, f.apiKeyRepo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodDelete, "/api/v1/users/"+revokeTargetID, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusNoContent, w.Code)
			assert.True(t, f.store.wasBlockUserCalledFor(revokeTargetID))
			assert.Equal(t, tt.wantRTRevoked, f.tokenRevoked("rt-target-1"))

			if tt.wantKeysDeleted {
				assert.Empty(t, f.apiKeyIDs(t, revokeTargetID))
			} else {
				assert.Len(t, f.apiKeyIDs(t, revokeTargetID), 2)
			}
		})
	}
}

// TestDeleteUser_NoRevokeWhenDeleteFails checks that a rejected or failed
// delete revokes nothing.
func TestDeleteUser_NoRevokeWhenDeleteFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		callerID   string
		targetID   string
		wantStatus int
	}{
		{
			name:       "own account is rejected",
			callerID:   revokeTargetID,
			targetID:   revokeTargetID,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:     "unknown user",
			callerID: "admin-1",
			targetID: "user-missing",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newRevokeFixture(t)
			router := setupUserRouterFull(f.userRepo, tt.callerID, "admin", f.store, f.refreshRepo, f.apiKeyRepo)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodDelete, "/api/v1/users/"+tt.targetID, nil)
			router.ServeHTTP(w, req)

			if tt.wantStatus != 0 {
				assert.Equal(t, tt.wantStatus, w.Code)
			}
			assert.GreaterOrEqual(t, w.Code, http.StatusBadRequest)
			assert.False(t, f.store.wasBlockUserCalledFor(tt.targetID))
			assert.False(t, f.tokenRevoked("rt-target-1"))
			assert.Len(t, f.apiKeyIDs(t, revokeTargetID), 2)
		})
	}
}

// adminStep is one admin request in TestRevokeUserAccess_OldTokenRejectedByAuthMiddleware.
type adminStep struct {
	method   string
	path     string
	body     string
	wantCode int
}

// TestRevokeUserAccess_OldTokenRejectedByAuthMiddleware is the end-to-end
// check for issue 433: after delete, disable or password reset, the user's
// existing access token gets 401 from the JWT middleware. Enabling the user
// again does not bring the old token back. A token issued after a reset or
// an enable works.
func TestRevokeUserAccess_OldTokenRejectedByAuthMiddleware(t *testing.T) {
	t.Parallel()

	const secret = "test-secret-for-revoke-e2e"
	userPath := "/api/v1/users/" + revokeTargetID

	tests := []struct {
		name          string
		steps         []adminStep
		checkNewToken bool
	}{
		{
			name:  "delete user",
			steps: []adminStep{{http.MethodDelete, userPath, "", http.StatusNoContent}},
		},
		{
			name:  "disable user",
			steps: []adminStep{{http.MethodPut, userPath + "/disable", "", http.StatusOK}},
		},
		{
			name:          "reset password",
			steps:         []adminStep{{http.MethodPut, userPath + "/password", `{"password":"a-new-password-1"}`, http.StatusOK}},
			checkNewToken: true,
		},
		{
			name: "disable then enable keeps old token revoked",
			steps: []adminStep{
				{http.MethodPut, userPath + "/disable", "", http.StatusOK},
				{http.MethodPut, userPath + "/enable", "", http.StatusOK},
			},
			checkNewToken: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			userRepo := NewMockUserRepository()
			seedUser(t, userRepo, revokeTargetID, "alice", "testpass1", "user")
			store := sessionstore.NewMemoryStore()
			t.Cleanup(store.Stop)

			// Access token the user holds before the admin action.
			oldToken, err := middleware.GenerateToken(revokeTargetID, "alice", "user", secret, 15*time.Minute)
			require.NoError(t, err)

			// Admin action through the real handler.
			seedCallerAdmin(userRepo, "admin-1", "admin")
			h := NewUserHandler(userRepo, NewMockRefreshTokenRepository(), NewMockAPIKeyRepository())
			h.SetSessionStore(store)
			h.SetAccessTokenExpiration(15 * time.Minute)
			gin.SetMode(gin.TestMode)
			adminRouter := gin.New()
			adminRouter.Use(injectAuthContext("admin-1", "admin"))
			adminRouter.DELETE("/api/v1/users/:id", h.DeleteUser)
			adminRouter.PUT("/api/v1/users/:id/disable", h.DisableUser)
			adminRouter.PUT("/api/v1/users/:id/enable", h.EnableUser)
			adminRouter.PUT("/api/v1/users/:id/password", h.ResetUserPassword)

			for _, step := range tt.steps {
				w := httptest.NewRecorder()
				req, _ := http.NewRequest(step.method, step.path, bytes.NewBufferString(step.body))
				if step.body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				adminRouter.ServeHTTP(w, req)
				require.Equal(t, step.wantCode, w.Code, w.Body.String())
			}

			// Protected API behind the real JWT middleware.
			api := gin.New()
			api.Use(middleware.AuthRequiredWithSessionStore(secret, store))
			api.GET("/api/v1/stack-instances", func(c *gin.Context) { c.Status(http.StatusOK) })
			call := func(token string) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				r, _ := http.NewRequest(http.MethodGet, "/api/v1/stack-instances", nil)
				r.Header.Set("Authorization", "Bearer "+token)
				api.ServeHTTP(rec, r)
				return rec
			}

			old := call(oldToken)
			assert.Equal(t, http.StatusUnauthorized, old.Code, "old token must be revoked")
			assert.Contains(t, old.Body.String(), "Session revoked")

			if tt.checkNewToken {
				// A login after the last admin action issues a token with a later iat.
				newToken := signUserToken(t, secret, revokeTargetID, time.Now().Add(2*time.Second))
				assert.Equal(t, http.StatusOK, call(newToken).Code, "token issued after the admin action must work")
			}
		})
	}
}

// signUserToken signs an access token for userID with an explicit iat.
func signUserToken(t *testing.T, secret, userID string, iat time.Time) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, middleware.Claims{
		UserID: userID, Username: "alice", Role: "user",
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        "jti-new-" + userID,
			IssuedAt:  jwt.NewNumericDate(iat),
			ExpiresAt: jwt.NewNumericDate(iat.Add(15 * time.Minute)),
			Subject:   userID,
		},
	})
	signed, err := tok.SignedString([]byte(secret))
	require.NoError(t, err)
	return signed
}

// slowRevokeRepo makes RevokeAllForUser take delay, as a stalled database.
type slowRevokeRepo struct {
	*MockRefreshTokenRepository
	delay time.Duration
}

func (r *slowRevokeRepo) RevokeAllForUser(ctx context.Context, userID string) error {
	time.Sleep(r.delay)
	return r.MockRefreshTokenRepository.RevokeAllForUser(ctx, userID)
}

// leftAtBlockStore records how much time the context of BlockUser had left.
type leftAtBlockStore struct {
	*mockSessionStore
	left time.Duration
}

func (s *leftAtBlockStore) BlockUser(ctx context.Context, userID string, until time.Time) error {
	if d, ok := ctx.Deadline(); ok {
		s.left = time.Until(d)
	}
	return s.mockSessionStore.BlockUser(ctx, userID, until)
}

// TestRevokeUserAccess_StepsHaveTheirOwnBudget checks that a slow refresh
// token revoke does not use up the time of the token block: each step of
// revokeUserAccess gets a WriteTimeout budget of its own (#674).
func TestRevokeUserAccess_StepsHaveTheirOwnBudget(t *testing.T) {
	t.Parallel()

	const slowRevoke = 300 * time.Millisecond
	f := newRevokeFixture(t)
	store := &leftAtBlockStore{mockSessionStore: f.store}
	h := NewUserHandler(f.userRepo, &slowRevokeRepo{MockRefreshTokenRepository: f.refreshRepo, delay: slowRevoke}, f.apiKeyRepo)
	h.SetSessionStore(store)
	h.SetAccessTokenExpiration(15 * time.Minute)

	ctx, cancel := ctxutil.Detach(context.Background(), ctxutil.WriteTimeout)
	defer cancel()
	h.revokeUserAccess(ctx, revokeTargetID, revokeOptions{})

	assert.True(t, f.tokenRevoked("rt-target-1"), "the refresh tokens are revoked")
	assert.True(t, f.store.wasBlockUserCalledFor(revokeTargetID), "the earlier tokens are blocked")
	assert.Greater(t, store.left, ctxutil.WriteTimeout-slowRevoke/2,
		"the block starts a fresh WriteTimeout budget after the slow revoke")
}
