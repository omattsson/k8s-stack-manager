package handlers

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/config"
	"backend/internal/models"
	"backend/internal/sessionstore"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Request-context rules of the refresh token paths (#656): the reads that
// decide a request use the request context; every write and the reads after
// the first write use a detached context, so a client that leaves cannot skip
// a revoke, half-finish a rotation or keep a session alive after logout.

// rtCtxKey marks the request context in these tests.
type rtCtxKey struct{}

// rtCall is one MockRefreshTokenRepository call seen by rtCallLog.
type rtCall struct {
	ctx    context.Context
	method string
}

// rtCallLog records the refresh token calls and cancels the request at call
// cancelN (default 1) of cancelAt (before the call checks its context).
type rtCallLog struct {
	cancel     context.CancelFunc
	cancelAt   string
	calls      []rtCall
	cancelN    int
	cancelSeen int
	mu         sync.Mutex
}

func (l *rtCallLog) hook(method string, ctx context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, rtCall{method: method, ctx: ctx})
	if method == l.cancelAt && l.cancel != nil {
		l.cancelSeen++
		if l.cancelSeen >= max(l.cancelN, 1) {
			l.cancel()
			l.cancel = nil
		}
	}
}

func (l *rtCallLog) methods() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.calls))
	for _, c := range l.calls {
		out = append(out, c.method)
	}
	return out
}

// assertDetachedFrom checks that every call from the first call of method on
// ran with a live context that keeps the request values.
func (l *rtCallLog) assertDetachedFrom(t *testing.T, method string) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	seen := false
	for _, c := range l.calls {
		if c.method == method {
			seen = true
		}
		if !seen {
			continue
		}
		assert.NoError(t, c.ctx.Err(), "%s runs with a context that the client cannot cancel", c.method)
		assert.Equal(t, "req", c.ctx.Value(rtCtxKey{}), "%s keeps the request values", c.method)
	}
	assert.True(t, seen, "%s was called", method)
}

// ctxSessionStore records BlockToken and fails it on a done context, as the
// MySQL store does.
type ctxSessionStore struct {
	*mockSessionStore
	blocked map[string]context.Context
	mu      sync.Mutex
}

func newCtxSessionStore() *ctxSessionStore {
	return &ctxSessionStore{mockSessionStore: newMockHandlerSessionStore(), blocked: map[string]context.Context{}}
}

func (s *ctxSessionStore) BlockToken(ctx context.Context, jti string, _ time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocked[jti] = ctx
	return nil
}

func (s *ctxSessionStore) blockCtx(jti string) (context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, ok := s.blocked[jti]
	return ctx, ok
}

type rtCtxEnv struct {
	cfg       *config.AuthConfig
	users     *MockUserRepository
	userHooks *hookUserRepo // the handler's user repository (wraps users)
	refresh   *MockRefreshTokenRepository
	store     *ctxSessionStore
	router    *gin.Engine
}

// newRTCtxEnv wires an AuthHandler with refresh tokens for user uid-1
// (alice, password "secret"). logout-all runs as uid-1 with jti "jti-all".
func newRTCtxEnv(t *testing.T) *rtCtxEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	env := &rtCtxEnv{
		cfg:     sessionTestConfig(),
		users:   NewMockUserRepository(),
		refresh: NewMockRefreshTokenRepository(),
		store:   newCtxSessionStore(),
	}
	env.userHooks = &hookUserRepo{MockUserRepository: env.users}
	seedUser(t, env.users, "uid-1", "alice", "secret", "user")
	h := NewAuthHandler(env.userHooks, env.cfg, &config.OIDCConfig{})
	h.SetRefreshTokenRepo(env.refresh)
	h.SetSessionStore(env.store)

	r := gin.New()
	r.POST("/login", h.Login)
	r.POST("/refresh", h.Refresh)
	r.POST("/logout", h.Logout)
	r.POST("/logout-all", func(c *gin.Context) {
		c.Set("userID", "uid-1")
		c.Set("role", "user")
		c.Set("jti", "jti-all")
		c.Next()
	}, h.LogoutAll)
	env.router = r
	return env
}

// seed stores a refresh token of uid-1 with the raw value "raw-<id>".
func (e *rtCtxEnv) seed(t *testing.T, id, family string, mutate func(*models.RefreshToken)) string {
	t.Helper()
	now := time.Now().UTC()
	raw := "raw-" + id
	rt := &models.RefreshToken{
		ID:               id,
		UserID:           "uid-1",
		FamilyID:         family,
		TokenHash:        hashRefreshToken(raw),
		ExpiresAt:        now.Add(24 * time.Hour),
		LastActivity:     now,
		CreatedAt:        now.Add(-time.Hour),
		SessionStartedAt: now.Add(-time.Hour),
	}
	if mutate != nil {
		mutate(rt)
	}
	require.NoError(t, e.refresh.Create(context.Background(), rt))
	return raw
}

func (e *rtCtxEnv) token(t *testing.T, id string) models.RefreshToken {
	t.Helper()
	e.refresh.mu.RLock()
	defer e.refresh.mu.RUnlock()
	rt, ok := e.refresh.tokens[id]
	require.True(t, ok, "token %s", id)
	return *rt
}

// serve runs a request with ctx (marked with rtCtxKey).
func (e *rtCtxEnv) serve(ctx context.Context, path, body, rawCookie, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body)).
		WithContext(context.WithValue(ctx, rtCtxKey{}, "req"))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if rawCookie != "" {
		req.AddCookie(&http.Cookie{Name: refreshTokenCookieName, Value: rawCookie})
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

// TestLogin_ClientLeftAfterChecksStillStoresToken checks that a login whose
// client leaves after the password check and the user re-check still stores
// the refresh token and keeps the per-user token cap (#656).
func TestLogin_ClientLeftAfterChecksStillStoresToken(t *testing.T) {
	t.Parallel()
	env := newRTCtxEnv(t)
	// A full set of sessions: the login must revoke the oldest one.
	for i := 0; i < env.cfg.MaxRefreshTokensPerUser; i++ {
		age := time.Duration(env.cfg.MaxRefreshTokensPerUser-i) * time.Minute
		env.seed(t, "old-"+string(rune('a'+i)), "fam-old-"+string(rune('a'+i)), func(rt *models.RefreshToken) {
			rt.CreatedAt = time.Now().UTC().Add(-age)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := &rtCallLog{cancel: cancel, cancelAt: "CountActiveForUser"}
	env.refresh.setCallHook(log.hook)

	w := env.serve(ctx, "/login", `{"username":"alice","password":"secret"}`, "", "")
	env.refresh.setCallHook(nil)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.ErrorIs(t, ctx.Err(), context.Canceled, "the request was cancelled during the login")
	cookie := findRefreshCookie(w)
	require.NotNil(t, cookie)
	require.NotEmpty(t, cookie.Value)
	stored, err := env.refresh.FindByTokenHash(context.Background(), hashRefreshToken(cookie.Value))
	require.NoError(t, err, "the refresh token is stored")
	assert.False(t, stored.Revoked)
	active, err := env.refresh.CountActiveForUser(context.Background(), "uid-1")
	require.NoError(t, err)
	assert.Equal(t, int64(env.cfg.MaxRefreshTokensPerUser), active, "the token cap holds")
	assert.True(t, env.token(t, "old-a").Revoked, "the oldest session is revoked")
	log.assertDetachedFrom(t, "CountActiveForUser")
	assert.Contains(t, log.methods(), "Create")
	assert.Contains(t, log.methods(), "RevokeOldestForUser")
	assert.Contains(t, log.methods(), "CountActiveInFamily", "the session re-check ran")
}

// TestOIDCCallback_ClientLeftStillStoresToken checks the same for the OIDC
// browser login: the refresh token create, the cap and the session re-check
// run when the client left after the identity check (#656).
func TestOIDCCallback_ClientLeftStillStoresToken(t *testing.T) {
	t.Parallel()
	h, stateStore, userRepo := newOIDCHandlerSetup(t, false)
	cfg := sessionTestConfig(func(c *config.AuthConfig) { c.MaxRefreshTokensPerUser = 1 })
	h.authCfg = cfg
	refresh := NewMockRefreshTokenRepository()
	h.SetRefreshTokenRepo(refresh)
	require.NoError(t, userRepo.Create(context.Background(), &models.User{
		ID: "oidc-uid", Username: "oidcuser@example.com", Role: "user",
		AuthProvider: "oidc", ExternalID: stringPtr("oidc-user-001"),
	}))
	now := time.Now().UTC()
	require.NoError(t, refresh.Create(context.Background(), &models.RefreshToken{
		ID: "oidc-old", UserID: "oidc-uid", FamilyID: "oidc-old", TokenHash: hashRefreshToken("raw-oidc-old"),
		ExpiresAt: now.Add(time.Hour), LastActivity: now, CreatedAt: now.Add(-time.Hour), SessionStartedAt: now.Add(-time.Hour),
	}))
	require.NoError(t, stateStore.SaveOIDCState(context.Background(), "state-rt-ctx", sessionstore.OIDCStateData{
		CodeVerifier: "test-verifier", RedirectURL: "/",
	}, 5*time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := &rtCallLog{cancel: cancel, cancelAt: "CountActiveForUser"}
	refresh.setCallHook(log.hook)

	r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=state-rt-ctx", nil).
		WithContext(context.WithValue(ctx, rtCtxKey{}, "req"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	refresh.setCallHook(nil)

	require.Equal(t, http.StatusFound, w.Code, w.Body.String())
	assert.True(t, strings.HasPrefix(w.Header().Get("Location"), "/auth/callback#"), w.Header().Get("Location"))
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	cookie := findRefreshCookie(w)
	require.NotNil(t, cookie)
	stored, err := refresh.FindByTokenHash(context.Background(), hashRefreshToken(cookie.Value))
	require.NoError(t, err, "the refresh token is stored")
	assert.False(t, stored.Revoked)
	old, err := refresh.FindByTokenHash(context.Background(), hashRefreshToken("raw-oidc-old"))
	require.NoError(t, err)
	assert.True(t, old.Revoked, "the token cap revoked the older session")
	active, err := refresh.CountActiveForUser(context.Background(), "oidc-uid")
	require.NoError(t, err)
	assert.Equal(t, int64(cfg.MaxRefreshTokensPerUser), active, "the token cap holds")
	log.assertDetachedFrom(t, "CountActiveForUser")
	assert.Contains(t, log.methods(), "RevokeOldestForUser")
	assert.Contains(t, log.methods(), "Create")
	assert.Contains(t, log.methods(), "CountActiveInFamily", "the session re-check ran")
}

// TestRefresh_ClientLeftStillCommitsWrites checks that every refresh write
// commits when the client leaves at that write: the revokes before the
// rotation, the reuse-detection revoke and the rotation itself (#656).
func TestRefresh_ClientLeftStillCommitsWrites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(t *testing.T, e *rtCtxEnv) string // returns the raw cookie
		cancelAt   string
		wantStatus int
		wantBody   string
		verify     func(t *testing.T, e *rtCtxEnv)
	}{
		{
			name: "expired session revokes the family",
			setup: func(t *testing.T, e *rtCtxEnv) string {
				raw := e.seed(t, "rt-a", "fam-1", func(rt *models.RefreshToken) {
					rt.SessionStartedAt = time.Now().UTC().Add(-e.cfg.SessionMaxLifetime - time.Minute)
				})
				e.seed(t, "rt-b", "fam-1", nil)
				return raw
			},
			cancelAt:   "RevokeFamily",
			wantStatus: http.StatusUnauthorized,
			wantBody:   "Session expired",
			verify: func(t *testing.T, e *rtCtxEnv) {
				assert.True(t, e.token(t, "rt-a").Revoked)
				assert.True(t, e.token(t, "rt-b").Revoked)
			},
		},
		{
			name: "idle session revokes the token",
			setup: func(t *testing.T, e *rtCtxEnv) string {
				return e.seed(t, "rt-a", "fam-1", func(rt *models.RefreshToken) {
					rt.LastActivity = time.Now().UTC().Add(-e.cfg.SessionIdleTimeout - time.Minute)
				})
			},
			cancelAt:   "RevokeByID",
			wantStatus: http.StatusUnauthorized,
			wantBody:   "Session idle timeout exceeded",
			verify: func(t *testing.T, e *rtCtxEnv) {
				assert.True(t, e.token(t, "rt-a").Revoked)
			},
		},
		{
			name: "disabled user revokes all tokens",
			setup: func(t *testing.T, e *rtCtxEnv) string {
				u, err := e.users.FindByID(context.Background(), "uid-1")
				require.NoError(t, err)
				disabled := *u
				disabled.Disabled = true
				require.NoError(t, e.users.Update(&disabled))
				e.seed(t, "rt-other", "fam-2", nil)
				return e.seed(t, "rt-a", "fam-1", nil)
			},
			cancelAt:   "RevokeAllForUser",
			wantStatus: http.StatusForbidden,
			wantBody:   "Account disabled",
			verify: func(t *testing.T, e *rtCtxEnv) {
				assert.True(t, e.token(t, "rt-a").Revoked)
				assert.True(t, e.token(t, "rt-other").Revoked)
			},
		},
		{
			name: "replayed token revokes the family",
			setup: func(t *testing.T, e *rtCtxEnv) string {
				// Revoked without RotatedAt (logout or admin revoke): no grace.
				raw := e.seed(t, "rt-a", "fam-1", func(rt *models.RefreshToken) { rt.Revoked = true })
				e.seed(t, "rt-b", "fam-1", nil)
				e.seed(t, "rt-other", "fam-2", nil)
				return raw
			},
			cancelAt:   "RevokeFamily",
			wantStatus: http.StatusUnauthorized,
			wantBody:   "Invalid refresh token",
			verify: func(t *testing.T, e *rtCtxEnv) {
				assert.True(t, e.token(t, "rt-b").Revoked, "the family of the replayed token is revoked")
				assert.False(t, e.token(t, "rt-other").Revoked, "other sessions stay")
			},
		},
		{
			// The rotation commits; the user re-check after it keeps the
			// request context (PR 2 rule): 499, no new token, no revoke-all.
			name: "rotation commits",
			setup: func(t *testing.T, e *rtCtxEnv) string {
				e.seed(t, "rt-other", "fam-2", nil)
				return e.seed(t, "rt-a", "fam-1", nil)
			},
			cancelAt:   "MarkRotatedIfActive",
			wantStatus: statusClientClosedRequest,
			verify: func(t *testing.T, e *rtCtxEnv) {
				old := e.token(t, "rt-a")
				assert.True(t, old.Revoked, "the presented token is consumed")
				assert.NotNil(t, old.RotatedAt, "by a rotation")
				n, err := e.refresh.CountActiveInFamily(context.Background(), "fam-1")
				require.NoError(t, err)
				assert.Equal(t, int64(1), n, "the successor is stored")
				assert.False(t, e.token(t, "rt-other").Revoked, "no revoke-all")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newRTCtxEnv(t)
			raw := tt.setup(t, env)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			log := &rtCallLog{cancel: cancel, cancelAt: tt.cancelAt}
			env.refresh.setCallHook(log.hook)

			w := env.serve(ctx, "/refresh", "", raw, "")
			env.refresh.setCallHook(nil)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			require.ErrorIs(t, ctx.Err(), context.Canceled, "the request was cancelled at %s", tt.cancelAt)
			if tt.wantBody != "" {
				assert.Contains(t, w.Body.String(), tt.wantBody)
			}
			assert.NotContains(t, w.Body.String(), `"token"`)
			log.assertDetachedFrom(t, tt.cancelAt)
			tt.verify(t, env)
		})
	}
}

// TestRefresh_ConsumedDuringRotationRevokesFamily checks the path after a
// lost rotation: the re-read and the reuse-detection revoke run detached, so
// a client that leaves during the rotation cannot skip the family revoke.
func TestRefresh_ConsumedDuringRotationRevokesFamily(t *testing.T) {
	t.Parallel()
	env := newRTCtxEnv(t)
	raw := env.seed(t, "rt-a", "fam-1", nil)
	env.seed(t, "rt-b", "fam-1", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := &rtCallLog{}
	env.refresh.setCallHook(log.hook)
	// Between the first read and the rotation, a logout revokes the token
	// (no RotatedAt) and the client leaves.
	env.refresh.txHook = func() {
		env.refresh.mu.Lock()
		env.refresh.tokens["rt-a"].Revoked = true
		env.refresh.mu.Unlock()
		cancel()
	}

	w := env.serve(ctx, "/refresh", "", raw, "")
	env.refresh.setCallHook(nil)

	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "Invalid refresh token")
	assert.True(t, env.token(t, "rt-b").Revoked, "the family is revoked")
	assert.Nil(t, env.token(t, "rt-a").RotatedAt, "the lost rotation did not commit")
	log.assertDetachedFrom(t, "MarkRotatedIfActive")
	assert.Contains(t, log.methods(), "RevokeFamily")
}

// TestRefresh_FirstLookupContext checks the first token lookup: a cancelled
// request gives 499 and changes nothing; a passed deadline is a server
// failure (500), not a client cancel.
func TestRefresh_FirstLookupContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ctx        func() (context.Context, context.CancelFunc)
		wantStatus int
	}{
		{name: "cancelled", ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}, wantStatus: statusClientClosedRequest},
		{name: "deadline passed", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}, wantStatus: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newRTCtxEnv(t)
			raw := env.seed(t, "rt-a", "fam-1", nil)
			log := &rtCallLog{}
			env.refresh.setCallHook(log.hook)
			ctx, cancel := tt.ctx()
			defer cancel()

			w := env.serve(ctx, "/refresh", "", raw, "")
			env.refresh.setCallHook(nil)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			assert.Equal(t, []string{"FindByTokenHash"}, log.methods(), "nothing runs after the failed lookup")
			assert.Nil(t, findRefreshCookie(w), "the cookie is not touched")
			rt := env.token(t, "rt-a")
			assert.False(t, rt.Revoked)
			assert.Nil(t, rt.RotatedAt)
		})
	}
}

// TestRefresh_GraceClientLeft checks the reuse grace path when the client
// leaves at one of its reads: 499, the cookie is not touched and no token of
// the user is revoked (#656).
func TestRefresh_GraceClientLeft(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		call   int
		users  bool // method is a user repository call
	}{
		{name: "at the grace user lookup", method: "FindByID", call: 1, users: true},
		{name: "at the grace family check", method: "CountActiveInFamily", call: 1},
		{name: "at the user re-check", method: "FindByID", call: 2, users: true},
		{name: "at the session re-check", method: "CountActiveInFamily", call: 2},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newRTCtxEnv(t)
			rotated := time.Now().UTC().Add(-5 * time.Second)
			raw := env.seed(t, "rt-old", "fam-1", func(rt *models.RefreshToken) {
				rt.Revoked = true
				rt.RotatedAt = &rotated
			})
			env.seed(t, "rt-new", "fam-1", nil)
			env.seed(t, "rt-other", "fam-2", nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			log := &rtCallLog{}
			if tt.users {
				env.userHooks.setHook(cancelOnCall(tt.method, int32(tt.call), cancel))
			} else {
				log.cancel, log.cancelAt, log.cancelN = cancel, tt.method, tt.call
			}
			env.refresh.setCallHook(log.hook)

			w := env.serve(ctx, "/refresh", "", raw, "")
			env.refresh.setCallHook(nil)
			env.userHooks.setHook(nil)

			require.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
			require.ErrorIs(t, ctx.Err(), context.Canceled, "the request was cancelled at %s %d", tt.method, tt.call)
			assert.NotContains(t, w.Body.String(), `"token"`)
			assert.Nil(t, findRefreshCookie(w), "the cookie is not set or cleared")
			assert.False(t, env.token(t, "rt-new").Revoked, "the family stays")
			assert.False(t, env.token(t, "rt-other").Revoked, "other sessions stay")
			assert.NotContains(t, log.methods(), "RevokeFamily")
			assert.NotContains(t, log.methods(), "RevokeAllForUser")
		})
	}
}

// TestRefresh_LostRotationClientLeftKeepsFamily: a concurrent refresh
// rotates the token between the first read and the rotation, and the client
// leaves. The re-read finds the grace case and the user re-check keeps the
// request context: 499, and the session of the concurrent refresh stays.
func TestRefresh_LostRotationClientLeftKeepsFamily(t *testing.T) {
	t.Parallel()
	env := newRTCtxEnv(t)
	raw := env.seed(t, "rt-a", "fam-1", nil)
	env.seed(t, "rt-other", "fam-2", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := &rtCallLog{}
	env.refresh.setCallHook(log.hook)
	env.refresh.txHook = func() {
		now := time.Now().UTC()
		succ := &models.RefreshToken{
			ID: "rt-succ", UserID: "uid-1", FamilyID: "fam-1", TokenHash: hashRefreshToken("raw-rt-succ"),
			ExpiresAt: now.Add(time.Hour), LastActivity: now, CreatedAt: now, SessionStartedAt: now.Add(-time.Hour),
		}
		env.refresh.mu.Lock()
		env.refresh.tokens["rt-a"].Revoked = true
		env.refresh.tokens["rt-a"].RotatedAt = &now
		env.refresh.tokens[succ.ID] = succ
		env.refresh.byHash[succ.TokenHash] = succ
		env.refresh.mu.Unlock()
		cancel()
	}

	w := env.serve(ctx, "/refresh", "", raw, "")
	env.refresh.setCallHook(nil)

	require.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"token"`)
	assert.Nil(t, findRefreshCookie(w), "the cookie is not set or cleared")
	assert.False(t, env.token(t, "rt-succ").Revoked, "the concurrent session stays")
	assert.False(t, env.token(t, "rt-other").Revoked, "other sessions stay")
	assert.NotContains(t, log.methods(), "RevokeFamily")
	assert.NotContains(t, log.methods(), "RevokeAllForUser")
	log.assertDetachedFrom(t, "MarkRotatedIfActive")
}

// TestLogin_ClientLeftStillAbandonsSession checks that a login whose session
// re-check fails revokes the new session also when the client left after the
// token create (#656).
func TestLogin_ClientLeftStillAbandonsSession(t *testing.T) {
	t.Parallel()
	errCount := errors.New("count failed")

	t.Run("local login", func(t *testing.T) {
		t.Parallel()
		env := newRTCtxEnv(t)
		env.refresh.countErr = errCount
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		log := &rtCallLog{cancel: cancel, cancelAt: "Create"}
		env.refresh.setCallHook(log.hook)

		w := env.serve(ctx, "/login", `{"username":"alice","password":"secret"}`, "", "")
		env.refresh.setCallHook(nil)

		require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		assert.NotContains(t, w.Body.String(), `"token"`)
		active, err := env.refresh.CountActiveForUser(context.Background(), "uid-1")
		require.NoError(t, err)
		assert.Zero(t, active, "the abandoned session is revoked")
		assert.Contains(t, log.methods(), "RevokeFamily")
		log.assertDetachedFrom(t, "Create")
	})

	t.Run("OIDC browser login", func(t *testing.T) {
		t.Parallel()
		h, stateStore, _ := newOIDCHandlerSetup(t, false)
		h.authCfg = sessionTestConfig()
		refresh := NewMockRefreshTokenRepository()
		refresh.countErr = errCount
		h.SetRefreshTokenRepo(refresh)
		require.NoError(t, stateStore.SaveOIDCState(context.Background(), "state-abandon", sessionstore.OIDCStateData{
			CodeVerifier: "test-verifier", RedirectURL: "/",
		}, 5*time.Minute))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		log := &rtCallLog{cancel: cancel, cancelAt: "Create"}
		refresh.setCallHook(log.hook)

		r := setupOIDCRouter(h.Callback, http.MethodGet, "/api/v1/auth/oidc/callback")
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state=state-abandon", nil).
			WithContext(context.WithValue(ctx, rtCtxKey{}, "req"))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		refresh.setCallHook(nil)

		require.Equal(t, http.StatusFound, w.Code, w.Body.String())
		assert.Equal(t, "/login?error=auth_failed", w.Header().Get("Location"))
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		refresh.mu.RLock()
		assert.Len(t, refresh.tokens, 1)
		for _, tok := range refresh.tokens {
			assert.True(t, tok.Revoked, "the abandoned session is revoked")
		}
		refresh.mu.RUnlock()
		assert.Contains(t, log.methods(), "RevokeFamily")
		log.assertDetachedFrom(t, "Create")
	})
}

// TestLogout_ClientLeftStillLogsOut checks that logout and logout-all of a
// client that already left still block the access token and revoke the
// refresh tokens (#656).
func TestLogout_ClientLeftStillLogsOut(t *testing.T) {
	t.Parallel()

	t.Run("logout", func(t *testing.T) {
		t.Parallel()
		env := newRTCtxEnv(t)
		raw := env.seed(t, "rt-a", "fam-1", nil)
		access, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
			UserID: "uid-1", Username: "alice", Role: "user",
			Secret: env.cfg.JWTSecret, Expiration: env.cfg.AccessTokenExpiration,
		})
		require.NoError(t, err)
		claims, err := middleware.ValidateJWT(access, env.cfg.JWTSecret)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		w := env.serve(ctx, "/logout", "", raw, access)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.True(t, env.token(t, "rt-a").Revoked, "the refresh token is revoked")
		blockCtx, ok := env.store.blockCtx(claims.ID)
		require.True(t, ok, "the access token is blocked")
		assert.NoError(t, blockCtx.Err())
		assert.Equal(t, "req", blockCtx.Value(rtCtxKey{}))
	})

	t.Run("logout-all", func(t *testing.T) {
		t.Parallel()
		env := newRTCtxEnv(t)
		env.seed(t, "rt-a", "fam-1", nil)
		env.seed(t, "rt-b", "fam-2", nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		w := env.serve(ctx, "/logout-all", "", "", "")

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.True(t, env.token(t, "rt-a").Revoked)
		assert.True(t, env.token(t, "rt-b").Revoked)
		blockCtx, ok := env.store.blockCtx("jti-all")
		require.True(t, ok, "the access token is blocked")
		assert.NoError(t, blockCtx.Err())
		assert.Equal(t, "req", blockCtx.Value(rtCtxKey{}))
	})
}

// TestCleanupExpiredTokens_UsesCallerContext checks that the cleanup deletes
// with the caller context: its values reach the repository, and a done
// context deletes nothing.
func TestCleanupExpiredTokens_UsesCallerContext(t *testing.T) {
	t.Parallel()
	env := newRTCtxEnv(t)
	env.seed(t, "rt-expired", "fam-1", func(rt *models.RefreshToken) {
		rt.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	})
	h := NewAuthHandler(env.users, env.cfg, &config.OIDCConfig{})
	h.SetRefreshTokenRepo(env.refresh)
	log := &rtCallLog{}
	env.refresh.setCallHook(log.hook)

	done, cancel := context.WithCancel(context.Background())
	cancel()
	h.CleanupExpiredTokens(done)
	env.token(t, "rt-expired") // still there

	h.CleanupExpiredTokens(context.WithValue(context.Background(), rtCtxKey{}, "req"))
	env.refresh.mu.RLock()
	_, ok := env.refresh.tokens["rt-expired"]
	env.refresh.mu.RUnlock()
	assert.False(t, ok, "the expired token is deleted")
	log.mu.Lock()
	defer log.mu.Unlock()
	require.Len(t, log.calls, 2)
	assert.Equal(t, "DeleteExpired", log.calls[1].method)
	assert.Equal(t, "req", log.calls[1].ctx.Value(rtCtxKey{}))
}

// TestMockRefreshTokenRepository_DoneContext checks that the mock fails each
// method on a done context and changes nothing, as the GORM repository does.
func TestMockRefreshTokenRepository_DoneContext(t *testing.T) {
	t.Parallel()

	calls := []struct {
		name string
		call func(ctx context.Context, m *MockRefreshTokenRepository) error
	}{
		{"Create", func(ctx context.Context, m *MockRefreshTokenRepository) error {
			return m.Create(ctx, &models.RefreshToken{ID: "new", UserID: "u1", TokenHash: "h-new", ExpiresAt: time.Now().Add(time.Hour)})
		}},
		{"FindByTokenHash", func(ctx context.Context, m *MockRefreshTokenRepository) error {
			_, err := m.FindByTokenHash(ctx, "h-1")
			return err
		}},
		{"RevokeByID", func(ctx context.Context, m *MockRefreshTokenRepository) error { return m.RevokeByID(ctx, "rt-1") }},
		{"MarkRotatedIfActive", func(ctx context.Context, m *MockRefreshTokenRepository) error {
			_, err := m.MarkRotatedIfActive(ctx, "rt-1", time.Now())
			return err
		}},
		{"RevokeFamily", func(ctx context.Context, m *MockRefreshTokenRepository) error { return m.RevokeFamily(ctx, "f1") }},
		{"CountActiveInFamily", func(ctx context.Context, m *MockRefreshTokenRepository) error {
			_, err := m.CountActiveInFamily(ctx, "f1")
			return err
		}},
		{"TouchFamily", func(ctx context.Context, m *MockRefreshTokenRepository) error {
			return m.TouchFamily(ctx, "f1", time.Now().Add(time.Minute))
		}},
		{"RevokeAllForUser", func(ctx context.Context, m *MockRefreshTokenRepository) error { return m.RevokeAllForUser(ctx, "u1") }},
		{"RevokeAllForUserExcept", func(ctx context.Context, m *MockRefreshTokenRepository) error {
			return m.RevokeAllForUserExcept(ctx, "u1", "none")
		}},
		{"RevokeOldestForUser", func(ctx context.Context, m *MockRefreshTokenRepository) error {
			_, err := m.RevokeOldestForUser(ctx, "u1", 0, "")
			return err
		}},
		{"DeleteExpired", func(ctx context.Context, m *MockRefreshTokenRepository) error {
			_, err := m.DeleteExpired(ctx)
			return err
		}},
		{"CountActiveForUser", func(ctx context.Context, m *MockRefreshTokenRepository) error {
			_, err := m.CountActiveForUser(ctx, "u1")
			return err
		}},
		{"WithTx", func(ctx context.Context, m *MockRefreshTokenRepository) error {
			return m.WithTx(ctx, func(tx models.RefreshTokenRepository) error {
				return tx.RevokeAllForUser(context.Background(), "u1")
			})
		}},
	}

	for _, tt := range calls {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := NewMockRefreshTokenRepository()
			now := time.Now()
			require.NoError(t, m.Create(context.Background(), &models.RefreshToken{
				ID: "rt-1", UserID: "u1", FamilyID: "f1", TokenHash: "h-1",
				ExpiresAt: now.Add(time.Hour), LastActivity: now,
			}))
			require.NoError(t, m.Create(context.Background(), &models.RefreshToken{
				ID: "rt-old", UserID: "u1", FamilyID: "f2", TokenHash: "h-old",
				ExpiresAt: now.Add(-time.Hour), LastActivity: now,
			}))
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := tt.call(ctx, m)

			require.ErrorIs(t, err, context.Canceled)
			m.mu.RLock()
			defer m.mu.RUnlock()
			assert.Len(t, m.tokens, 2, "no token created or deleted")
			rt := m.tokens["rt-1"]
			assert.False(t, rt.Revoked)
			assert.Nil(t, rt.RotatedAt)
			assert.True(t, rt.LastActivity.Equal(now))
		})
	}

	t.Run("WithTx cancelled before the commit", func(t *testing.T) {
		t.Parallel()
		m := NewMockRefreshTokenRepository()
		require.NoError(t, m.Create(context.Background(), &models.RefreshToken{
			ID: "rt-1", UserID: "u1", FamilyID: "f1", TokenHash: "h-1", ExpiresAt: time.Now().Add(time.Hour),
		}))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err := m.WithTx(ctx, func(tx models.RefreshTokenRepository) error {
			if err := tx.RevokeAllForUser(ctx, "u1"); err != nil {
				return err
			}
			cancel()
			return nil
		})

		require.ErrorIs(t, err, context.Canceled)
		m.mu.RLock()
		defer m.mu.RUnlock()
		assert.False(t, m.tokens["rt-1"].Revoked, "the transaction committed nothing")
	})
}
