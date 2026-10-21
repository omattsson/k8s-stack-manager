package handlers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/config"
	"backend/internal/models"
	"backend/internal/sessionstore"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// userCtxMarkerKey marks the request context in the #656 propagation tests.
type userCtxMarkerKey struct{}

// hookUserRepo calls hook with the method name and the context before the
// lookups of MockUserRepository run. A hook that cancels the request context
// simulates a client that leaves during the request.
type hookUserRepo struct {
	*MockUserRepository
	mu   sync.Mutex
	hook func(method string, ctx context.Context)
}

func (r *hookUserRepo) setHook(fn func(method string, ctx context.Context)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hook = fn
}

func (r *hookUserRepo) run(method string, ctx context.Context) {
	r.mu.Lock()
	fn := r.hook
	r.mu.Unlock()
	if fn != nil {
		fn(method, ctx)
	}
}

func (r *hookUserRepo) FindByID(ctx context.Context, id string) (*models.User, error) {
	r.run("FindByID", ctx)
	return r.MockUserRepository.FindByID(ctx, id)
}

func (r *hookUserRepo) FindByIDs(ctx context.Context, ids []string) (map[string]*models.User, error) {
	r.run("FindByIDs", ctx)
	return r.MockUserRepository.FindByIDs(ctx, ids)
}

func (r *hookUserRepo) FindByUsername(ctx context.Context, username string) (*models.User, error) {
	r.run("FindByUsername", ctx)
	return r.MockUserRepository.FindByUsername(ctx, username)
}

func (r *hookUserRepo) FindByExternalID(ctx context.Context, provider, externalID string) (*models.User, error) {
	r.run("FindByExternalID", ctx)
	return r.MockUserRepository.FindByExternalID(ctx, provider, externalID)
}

// cancelOnCall returns a hook that cancels the request at call number n of
// method (1-based, counted from now).
func cancelOnCall(method string, n int32, cancel context.CancelFunc) func(string, context.Context) {
	var calls atomic.Int32
	return func(m string, _ context.Context) {
		if m == method && calls.Add(1) == n {
			cancel()
		}
	}
}

func cancelledRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// liveCtxSessionStore fails BlockUser on a done context, as the MySQL store
// does, so a revocation that runs with the request context shows.
type liveCtxSessionStore struct {
	*mockSessionStore
}

func (s *liveCtxSessionStore) BlockUser(ctx context.Context, userID string, until time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.mockSessionStore.BlockUser(ctx, userID, until)
}

// TestUserChanges_CancelledRequestStillCommit checks that a client that left
// cannot commit a user change and then skip the revocation or the audit
// entry (#656): with an already cancelled request context, delete, disable,
// enable, password reset and role change still commit, revoke the access of
// the user, answer with the normal status and write the audit entry.
func TestUserChanges_CancelledRequestStillCommit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		check      func(t *testing.T, repo *MockUserRepository)
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		disabled   bool // the target starts disabled
		wantRevoke bool // refresh tokens revoked and sockets closed
		wantNoKeys bool // the API keys of the user are deleted
	}{
		{
			name: "delete", method: http.MethodDelete, path: "/api/v1/users/" + revokeTargetID,
			wantStatus: http.StatusNoContent, wantRevoke: true, wantNoKeys: true,
			check: func(t *testing.T, repo *MockUserRepository) {
				_, err := repo.FindByID(context.Background(), revokeTargetID)
				assert.Error(t, err, "the user is deleted")
			},
		},
		{
			name: "disable", method: http.MethodPut, path: "/api/v1/users/" + revokeTargetID + "/disable",
			wantStatus: http.StatusOK, wantRevoke: true,
			check: func(t *testing.T, repo *MockUserRepository) {
				u, err := repo.FindByID(context.Background(), revokeTargetID)
				require.NoError(t, err)
				assert.True(t, u.Disabled)
			},
		},
		{
			name: "enable", method: http.MethodPut, path: "/api/v1/users/" + revokeTargetID + "/enable",
			wantStatus: http.StatusOK, disabled: true,
			check: func(t *testing.T, repo *MockUserRepository) {
				u, err := repo.FindByID(context.Background(), revokeTargetID)
				require.NoError(t, err)
				assert.False(t, u.Disabled)
			},
		},
		{
			name: "password reset", method: http.MethodPut, path: "/api/v1/users/" + revokeTargetID + "/password",
			body: `{"password":"a-new-password-1"}`, wantStatus: http.StatusOK, wantRevoke: true,
			check: func(t *testing.T, repo *MockUserRepository) {
				u, err := repo.FindByID(context.Background(), revokeTargetID)
				require.NoError(t, err)
				assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("a-new-password-1")))
			},
		},
		{
			name: "role change", method: http.MethodPut, path: "/api/v1/users/" + revokeTargetID + "/role",
			body: `{"role":"devops"}`, wantStatus: http.StatusOK, wantRevoke: true,
			check: func(t *testing.T, repo *MockUserRepository) {
				u, err := repo.FindByID(context.Background(), revokeTargetID)
				require.NoError(t, err)
				assert.Equal(t, models.RoleDevOps, u.Role)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRevokeFixture(t)
			if tt.disabled {
				f.userRepo.mu.Lock()
				f.userRepo.users[revokeTargetID].Disabled = true
				f.userRepo.mu.Unlock()
			}
			revoker := &fakeRevoker{}
			audit := NewMockAuditLogRepository()

			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(injectAuthContext("admin-1", "admin"), middleware.NewAuditMiddleware(audit))
			seedCallerAdmin(f.userRepo, "admin-1", "admin")
			h := NewUserHandler(f.userRepo, f.refreshRepo, f.apiKeyRepo)
			h.SetSessionStore(&liveCtxSessionStore{mockSessionStore: f.store})
			h.SetAccessTokenExpiration(15 * time.Minute)
			h.SetWebSocketRevoker(revoker)
			r.DELETE("/api/v1/users/:id", h.DeleteUser)
			r.PUT("/api/v1/users/:id/disable", h.DisableUser)
			r.PUT("/api/v1/users/:id/enable", h.EnableUser)
			r.PUT("/api/v1/users/:id/password", h.ResetUserPassword)
			r.PUT("/api/v1/users/:id/role", h.ChangeUserRole)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, cancelledRequest(t, tt.method, tt.path, tt.body))

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			tt.check(t, f.userRepo)
			assert.True(t, f.store.wasBlockUserCalledFor(revokeTargetID), "the earlier tokens are blocked")
			users, _ := revoker.calls()
			if tt.wantRevoke {
				assert.True(t, f.tokenRevoked("rt-target-1"))
				assert.True(t, f.tokenRevoked("rt-target-2"))
				assert.Equal(t, []string{revokeTargetID}, users, "the sockets of the user are closed")
			} else {
				assert.False(t, f.tokenRevoked("rt-target-1"))
				assert.Empty(t, users)
			}
			assert.False(t, f.tokenRevoked("rt-other-1"))
			if tt.wantNoKeys {
				assert.Empty(t, f.apiKeyIDs(t, revokeTargetID), "the API keys of the user are deleted")
			} else {
				assert.Len(t, f.apiKeyIDs(t, revokeTargetID), 2)
			}
			assert.Len(t, f.apiKeyIDs(t, revokeOtherID), 1)
			assert.Eventually(t, func() bool {
				entries := audit.snapshot()
				return len(entries) == 1 && entries[0].EntityID == revokeTargetID
			}, 2*time.Second, 10*time.Millisecond, "one audit entry for the change")
		})
	}
}

// TestUserReads_CancelledRequest checks that a read for a client that left
// answers 499, not a logged 500, and that a passed deadline stays 500 (#656).
func TestUserReads_CancelledRequest(t *testing.T) {
	t.Parallel()

	usersRouter := func() *gin.Engine {
		return setupUserRouter(NewMockUserRepository(), "admin-1", "admin")
	}
	meRouter := func() *gin.Engine {
		repo := NewMockUserRepository()
		seedCallerAdmin(repo, "admin-1", "admin")
		r, _ := setupAuthRouter(repo, false, "admin-1", "admin")
		return r
	}
	analyticsRouter := func() *gin.Engine {
		return setupAnalyticsRouter(NewMockStackTemplateRepository(), NewMockStackDefinitionRepository(),
			NewMockStackInstanceRepository(), newMockDeployLogRepo(), NewMockUserRepository())
	}
	apiKeysRouter := func() *gin.Engine {
		repo := NewMockUserRepository()
		seedUser(t, repo, "user-1", "alice", "testpass", "user")
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(injectAuthContext("user-1", "user"))
		h := NewAPIKeyHandler(NewMockAPIKeyRepository(), repo, &config.AuthConfig{})
		r.GET("/api/v1/users/:id/api-keys", h.ListAPIKeys)
		return r
	}

	tests := []struct {
		router func() *gin.Engine
		name   string
		path   string
	}{
		{name: "list users", router: usersRouter, path: "/api/v1/users"},
		{name: "current user", router: meRouter, path: "/api/v1/auth/me"},
		{name: "analytics overview", router: analyticsRouter, path: "/api/v1/analytics/overview"},
		{name: "analytics user stats", router: analyticsRouter, path: "/api/v1/analytics/users"},
		{name: "API keys of a user", router: apiKeysRouter, path: "/api/v1/users/user-1/api-keys"},
	}

	ends := []struct {
		req        func(t *testing.T, path string) *http.Request
		name       string
		wantBody   string
		wantStatus int
	}{
		{
			name: "cancelled", wantStatus: statusClientClosedRequest, wantBody: `{"error":"Client closed request"}`,
			req: func(t *testing.T, path string) *http.Request {
				return cancelledRequest(t, http.MethodGet, path, "")
			},
		},
		{
			name: "deadline exceeded", wantStatus: http.StatusInternalServerError, wantBody: `{"error":"Internal server error"}`,
			req: func(t *testing.T, path string) *http.Request {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				return httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
			},
		},
	}

	for _, tt := range tests {
		for _, end := range ends {
			tt, end := tt, end
			t.Run(tt.name+", "+end.name, func(t *testing.T) {
				t.Parallel()
				w := httptest.NewRecorder()
				tt.router().ServeHTTP(w, end.req(t, tt.path))

				assert.Equal(t, end.wantStatus, w.Code, w.Body.String())
				assert.JSONEq(t, end.wantBody, w.Body.String())
			})
		}
	}
}

// TestRegister_CancelledRequestStillCreates checks that a register request
// whose client left still creates the user and answers 201 (#656).
func TestRegister_CancelledRequestStillCreates(t *testing.T) {
	t.Parallel()
	repo := NewMockUserRepository()
	r, _ := setupAuthRouter(repo, false, "admin-1", "admin")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, cancelledRequest(t, http.MethodPost, "/api/v1/auth/register",
		`{"username":"carol","password":"carol-pass-1"}`))

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	u, err := repo.FindByUsername(context.Background(), "carol")
	require.NoError(t, err)
	assert.Equal(t, models.RoleUser, u.Role)
}

// TestLogin_CancelledRequest checks that a login whose client left during a
// user lookup answers 499 without a "Login failed" line, a token or a stored
// refresh token (#656).
func TestLogin_CancelledRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cancelAt int32 // FindByID call that cancels the request; 0: cancelled before the lookup
	}{
		{name: "before the username lookup"},
		{name: "at the re-read after the password check", cancelAt: 1},
		{name: "at the re-check after signing", cancelAt: 2},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := NewMockUserRepository()
			seedUser(t, base, "uid-1", "alice", "secret-pass", "user")
			repo := &hookUserRepo{MockUserRepository: base}
			refreshRepo := NewMockRefreshTokenRepository()
			h := NewAuthHandler(repo, testAuthConfigWithRefresh(), &config.OIDCConfig{})
			h.SetRefreshTokenRepo(refreshRepo)
			var logBuf bytes.Buffer
			h.logger = slog.New(slog.NewTextHandler(&logBuf, nil))
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.POST("/api/v1/auth/login", h.Login)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var usernameCtx context.Context
			if tt.cancelAt == 0 {
				cancel()
				repo.setHook(func(method string, c context.Context) {
					if method == "FindByUsername" {
						usernameCtx = c
					}
				})
			} else {
				repo.setHook(cancelOnCall("FindByID", tt.cancelAt, cancel))
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
				bytes.NewBufferString(`{"username":"alice","password":"secret-pass"}`)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"token"`)
			assert.NotContains(t, logBuf.String(), "Login failed")
			active, err := refreshRepo.CountActiveForUser(context.Background(), "uid-1")
			require.NoError(t, err)
			assert.Zero(t, active, "no refresh token stored")
			if tt.cancelAt == 0 {
				require.NotNil(t, usernameCtx)
				assert.ErrorIs(t, usernameCtx.Err(), context.Canceled, "the username lookup gets the request context")
			}
		})
	}
}

// TestRefresh_CancelledRequest checks a refresh whose client left during a
// user lookup: 499, and no revoke of the other sessions of the user (#656).
func TestRefresh_CancelledRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cancelAt    int32 // FindByID call of the refresh that cancels the request
		wantRotated bool  // the presented token was rotated before the cancel
	}{
		{name: "at the user lookup", cancelAt: 1},
		{name: "at the re-check after signing", cancelAt: 2, wantRotated: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := NewMockUserRepository()
			seedUser(t, base, "uid-1", "alice", "secret-pass", "user")
			repo := &hookUserRepo{MockUserRepository: base}
			refreshRepo := NewMockRefreshTokenRepository()
			h := NewAuthHandler(repo, testAuthConfigWithRefresh(), &config.OIDCConfig{})
			h.SetRefreshTokenRepo(refreshRepo)
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.POST("/api/v1/auth/login", h.Login)
			r.POST("/api/v1/auth/refresh", h.Refresh)

			// Two sessions: the refresh uses the first one.
			var cookie *http.Cookie
			for i := 0; i < 2; i++ {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
					bytes.NewBufferString(`{"username":"alice","password":"secret-pass"}`))
				req.Header.Set("Content-Type", "application/json")
				r.ServeHTTP(w, req)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				if cookie == nil {
					cookie = refreshCookieFrom(w)
				}
			}
			require.NotNil(t, cookie)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			repo.setHook(cancelOnCall("FindByID", tt.cancelAt, cancel))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil).WithContext(ctx)
			req.AddCookie(cookie)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"token"`)
			if c := refreshCookieFrom(w); c != nil {
				assert.Empty(t, c.Value, "no new refresh token leaves the server")
			}
			active, err := refreshRepo.CountActiveForUser(context.Background(), "uid-1")
			require.NoError(t, err)
			// The other session stays; after a rotation the rotated session
			// has its (unsent) successor.
			assert.Equal(t, int64(2), active, "the other sessions are not revoked")
			stored, err := refreshRepo.FindByTokenHash(context.Background(), hashRefreshToken(cookie.Value))
			require.NoError(t, err)
			assert.Equal(t, tt.wantRotated, stored.Revoked)
		})
	}
}

// TestOIDCCallback_ClientLeftDuringProvisioning checks that the OIDC user
// sync runs to the end when the client leaves during it: the provisioning
// uses a detached context (#656).
func TestOIDCCallback_ClientLeftDuringProvisioning(t *testing.T) {
	t.Parallel()

	h, store, base := newOIDCHandlerSetup(t, false)
	repo := &hookUserRepo{MockUserRepository: base}
	h.userRepo = repo
	require.NoError(t, store.SaveOIDCState(context.Background(), "state-left", sessionstore.OIDCStateData{
		CodeVerifier: "test-verifier", RedirectURL: "/",
	}, 5*time.Minute))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var provisionCtx atomic.Value
	repo.setHook(func(method string, c context.Context) {
		if method == "FindByExternalID" {
			cancel()
			provisionCtx.Store(c)
		}
	})

	r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=state-left", nil).
		WithContext(context.WithValue(ctx, userCtxMarkerKey{}, "oidc-req"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusFound, w.Code, w.Body.String())
	assert.True(t, strings.HasPrefix(w.Header().Get("Location"), "/auth/callback#"), w.Header().Get("Location"))
	users, err := base.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, users, 1, "the user is provisioned")
	pc, ok := provisionCtx.Load().(context.Context)
	require.True(t, ok)
	assert.Equal(t, "oidc-req", pc.Value(userCtxMarkerKey{}), "the provisioning keeps the request values")
	assert.NoError(t, pc.Err(), "the request cancel does not reach the provisioning")
}

// TestUserLookups_CancelledLogLevel checks that a display-name or filter
// lookup that a cancelled request ends gives no result and one debug line,
// not a warn or error line (#656).
// Not parallel: it replaces the default slog logger.
func TestUserLookups_CancelledLogLevel(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	repo := NewMockUserRepository()
	seedUser(t, repo, "uid-1", "alice", "secret-pass", "user")
	versions := &TemplateVersionHandler{userRepo: repo}
	channels := &NotificationChannelHandler{userRepo: repo}

	tests := []struct {
		lookup  func(ctx context.Context) int // number of results
		name    string
		wantMsg string
	}{
		{name: "owner usernames", wantMsg: "failed to batch-fetch owner usernames",
			lookup: func(ctx context.Context) int { return len(lookupUsernames(ctx, repo, []string{"uid-1"})) }},
		{name: "template version authors", wantMsg: "failed to batch-fetch template version authors",
			lookup: func(ctx context.Context) int { return len(versions.usernames(ctx, "uid-1")) }},
		{name: "channel filter owners", wantMsg: "Channel filter owner check cancelled",
			lookup: func(ctx context.Context) int {
				return len(channels.filterWarnings(ctx, models.NotificationChannelFilters{OwnerIDs: []string{"uid-gone"}}))
			}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logBuf.Reset()
			require.Equal(t, 1, tt.lookup(context.Background()), "a live request gets the result")

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			assert.Zero(t, tt.lookup(ctx))
			assert.Equal(t, 1, strings.Count(logBuf.String(), "\n"), logBuf.String())
			assert.Contains(t, logBuf.String(), `level=DEBUG msg="`+tt.wantMsg+`"`)
		})
	}
}

// TestValuesBuilder_CancelledOwnerLookupFailsClosed checks that the values
// pipeline fails when the owner lookup is cancelled, instead of rendering
// the owner ID as {{.Owner}} (#656).
func TestValuesBuilder_CancelledOwnerLookupFailsClosed(t *testing.T) {
	t.Parallel()
	repo := NewMockUserRepository()
	seedUser(t, repo, "uid-1", "alice", "secret-pass", "user")
	b := &valuesBuilder{userRepo: repo}
	inst := &models.StackInstance{ID: "inst-1", Name: "demo", OwnerID: "uid-1", Branch: "main"}
	def := &models.StackDefinition{ID: "def-1", Name: "Demo"}

	_, vars, _, err := b.layers(context.Background(), inst, def, nil, "")
	require.NoError(t, err)
	assert.Equal(t, "alice", vars.Owner)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Equal(t, "uid-1", resolveOwnerName(ctx, repo, "uid-1"), "the cancelled lookup falls back to the owner ID")
	_, _, _, err = b.layers(ctx, inst, def, nil, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestListAPIKeys_UserLookupRequestContext checks that the user lookup of
// the API key list gets the request context: its values and its cancel
// (#656).
func TestListAPIKeys_UserLookupRequestContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		wantErr    error
		name       string
		cancelled  bool
		wantStatus int
	}{
		{name: "live request", wantStatus: http.StatusOK},
		{name: "cancelled request", cancelled: true, wantErr: context.Canceled, wantStatus: statusClientClosedRequest},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := NewMockUserRepository()
			seedUser(t, base, "user-1", "alice", "testpass", "user")
			repo := &hookUserRepo{MockUserRepository: base}
			var mu sync.Mutex
			var lookupCtx context.Context
			repo.setHook(func(method string, ctx context.Context) {
				if method == "FindByID" {
					mu.Lock()
					lookupCtx = ctx
					mu.Unlock()
				}
			})
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.Use(injectAuthContext("user-1", "user"))
			h := NewAPIKeyHandler(NewMockAPIKeyRepository(), repo, &config.AuthConfig{})
			r.GET("/api/v1/users/:id/api-keys", h.ListAPIKeys)

			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), userCtxMarkerKey{}, "list-keys"))
			defer cancel()
			if tt.cancelled {
				cancel()
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/users/user-1/api-keys", nil).WithContext(ctx))

			assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			mu.Lock()
			defer mu.Unlock()
			require.NotNil(t, lookupCtx, "the user lookup ran")
			assert.Equal(t, "list-keys", lookupCtx.Value(userCtxMarkerKey{}), "the lookup keeps the request values")
			assert.Equal(t, tt.wantErr, lookupCtx.Err(), "the lookup sees the request cancel")
		})
	}
}

// adminLookupRepo returns lookupErr from FindByUsername and counts the
// Create calls.
type adminLookupRepo struct {
	*MockUserRepository
	lookupErr   error
	createCalls atomic.Int32
}

func (r *adminLookupRepo) FindByUsername(ctx context.Context, username string) (*models.User, error) {
	if r.lookupErr != nil {
		return nil, r.lookupErr
	}
	return r.MockUserRepository.FindByUsername(ctx, username)
}

func (r *adminLookupRepo) Create(ctx context.Context, user *models.User) error {
	r.createCalls.Add(1)
	return r.MockUserRepository.Create(ctx, user)
}

// TestEnsureAdminUser_LookupError checks that the startup admin user is
// created only when the lookup reports a missing user. Another lookup error
// (a timeout, a database error) creates nothing (#656).
func TestEnsureAdminUser_LookupError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		lookupErr   error
		name        string
		wantCreated bool
	}{
		{name: "missing user", wantCreated: true},
		{name: "not found error", lookupErr: dberrors.NewDatabaseError("find_by_username", dberrors.ErrNotFound), wantCreated: true},
		{name: "timeout", lookupErr: dberrors.NewDatabaseError("find_by_username", context.DeadlineExceeded)},
		{name: "cancel", lookupErr: dberrors.NewDatabaseError("find_by_username", context.Canceled)},
		{name: "database error", lookupErr: dberrors.NewDatabaseError("find_by_username", errors.New("connection refused"))},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &adminLookupRepo{MockUserRepository: NewMockUserRepository(), lookupErr: tt.lookupErr}
			h := NewAuthHandler(repo, &config.AuthConfig{
				JWTSecret: testJWTSecret, JWTExpiration: time.Hour,
				AdminUsername: "admin", AdminPassword: "admin-pass",
			}, &config.OIDCConfig{})

			h.EnsureAdminUser(context.Background())

			if tt.wantCreated {
				assert.Equal(t, int32(1), repo.createCalls.Load())
				u, err := repo.MockUserRepository.FindByUsername(context.Background(), "admin")
				require.NoError(t, err)
				assert.Equal(t, models.RoleAdmin, u.Role)
				return
			}
			assert.Zero(t, repo.createCalls.Load(), "no admin user is created")
		})
	}
}
