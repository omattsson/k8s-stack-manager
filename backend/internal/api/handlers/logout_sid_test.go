package handlers

import (
	"bytes"
	"context"
	"encoding/json"
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
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for #699: a logout ends the session (all its access tokens), also
// without the refresh cookie, and does not touch the other sessions of the
// user. They run the real AuthHandler, UserHandler and JWT middleware on the
// sessionTestEnv stores, so the session block, the revoke order and the races
// are tested through the HTTP surface.

// sidSession is one login: its access token, refresh cookie value and family.
type sidSession struct {
	token  string
	raw    string
	family string
}

// sidStore is a MemoryStore whose BlockSession and BlockUser can fail.
type sidStore struct {
	*sessionstore.MemoryStore
	blockSessionErr error
	blockUserErr    error
	// isBlockedErr makes IsSessionBlocked fail (fail-open tests).
	isBlockedErr error
	mu           sync.Mutex
	untils       map[string]time.Time // last until passed to BlockSession
}

func (s *sidStore) BlockSession(ctx context.Context, sid string, until time.Time) error {
	if s.blockSessionErr != nil {
		return s.blockSessionErr
	}
	s.mu.Lock()
	if s.untils == nil {
		s.untils = make(map[string]time.Time)
	}
	s.untils[sid] = until
	s.mu.Unlock()
	return s.MemoryStore.BlockSession(ctx, sid, until)
}

func (s *sidStore) IsSessionBlocked(ctx context.Context, sid string) (bool, error) {
	if s.isBlockedErr != nil {
		return false, s.isBlockedErr
	}
	return s.MemoryStore.IsSessionBlocked(ctx, sid)
}

// untilOf returns the until of the last BlockSession call for sid.
func (s *sidStore) untilOf(sid string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.untils[sid]
	return u, ok
}

func (s *sidStore) BlockUser(ctx context.Context, userID string, until time.Time) error {
	if s.blockUserErr != nil {
		return s.blockUserErr
	}
	return s.MemoryStore.BlockUser(ctx, userID, until)
}

// sidEnv is a sessionTestEnv with logout-all, a protected route, and the admin
// routes of the UserHandler on the same stores.
type sidEnv struct {
	*sessionTestEnv
	guarded *sidStore
}

// newSIDEnv builds the environment and seeds user uid-1 (alice) and an admin.
// blockSessionErr and blockUserErr make the session store fail.
func newSIDEnv(t *testing.T, blockSessionErr, blockUserErr error) *sidEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	base := &sessionTestEnv{
		cfg:     sessionTestConfig(),
		users:   NewMockUserRepository(),
		refresh: NewMockRefreshTokenRepository(),
		store:   sessionstore.NewMemoryStore(),
		revoker: &fakeRevoker{},
	}
	t.Cleanup(base.store.Stop)
	store := &sidStore{MemoryStore: base.store, blockSessionErr: blockSessionErr, blockUserErr: blockUserErr}

	h := NewAuthHandler(base.users, base.cfg, &config.OIDCConfig{})
	h.SetRefreshTokenRepo(base.refresh)
	h.SetSessionStore(store)
	h.SetWebSocketRevoker(base.revoker)

	uh := NewUserHandler(base.users, base.refresh, NewMockAPIKeyRepository())
	uh.SetSessionStore(store)
	uh.SetAccessTokenExpiration(base.cfg.AccessTokenExpiration)

	authMW := middleware.AuthRequiredWithOptions(middleware.JWTAuthOptions{
		JWTSecret:    base.cfg.JWTSecret,
		SessionStore: store,
		OnSessionActivity: func(ctx context.Context, sessionID string, at time.Time) error {
			return base.refresh.TouchFamily(ctx, sessionID, at)
		},
	})

	r := gin.New()
	auth := r.Group("/api/v1/auth")
	auth.POST("/login", h.Login)
	auth.POST("/refresh", h.Refresh)
	auth.POST("/logout", h.Logout)
	auth.POST("/logout-all", authMW, h.LogoutAll)
	r.GET("/api/v1/ping", authMW, func(c *gin.Context) { c.Status(http.StatusOK) })
	admin := r.Group("/api/v1/users", injectAuthContext("admin-1", "admin"))
	admin.PUT("/:id/password", uh.ResetUserPassword)
	admin.PUT("/:id/role", uh.ChangeUserRole)
	base.router = r

	env := &sidEnv{sessionTestEnv: base, guarded: store}
	env.seedUser(t, "local", "")
	seedCallerAdmin(base.users, "admin-1", "admin")
	return env
}

// loginSession logs alice in and checks that the access token carries the
// family of the refresh cookie as its session.
func (e *sidEnv) loginSession(t *testing.T) sidSession {
	t.Helper()
	token, raw := e.login(t)
	family := e.tokenByRaw(t, raw).FamilyID
	claims, err := middleware.ValidateJWT(token, e.cfg.JWTSecret)
	require.NoError(t, err)
	require.NotEmpty(t, family)
	require.Equal(t, family, claims.SessionID, "the access token must carry the session family")
	return sidSession{token: token, raw: raw, family: family}
}

// signFor signs another access token (new jti) of uid-1 for the session sid.
func (e *sidEnv) signFor(t *testing.T, sid string) string {
	t.Helper()
	token, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
		UserID: "uid-1", Username: "alice", Role: "user",
		Secret: e.cfg.JWTSecret, Expiration: e.cfg.AccessTokenExpiration, SessionID: sid,
	})
	require.NoError(t, err)
	return token
}

func (e *sidEnv) ping(token string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	e.router.ServeHTTP(w, req)
	return w
}

// doLogout posts a logout. An empty bearer sends no Authorization header, an
// empty raw sends no cookie.
func (e *sidEnv) doLogout(bearer, raw string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if raw != "" {
		req.AddCookie(&http.Cookie{Name: refreshTokenCookieName, Value: raw})
	}
	e.router.ServeHTTP(w, req)
	return w
}

func (e *sidEnv) doLogoutAll(bearer string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/logout-all", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	e.router.ServeHTTP(w, req)
	return w
}

func (e *sidEnv) adminPut(path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	e.router.ServeHTTP(w, req)
	return w
}

// refreshOK refreshes with raw and returns the new access token and the new
// cookie value ("" when the response sets none, as in the reuse grace).
func (e *sidEnv) refreshOK(t *testing.T, raw string) (token, newRaw string) {
	t.Helper()
	w := e.postRefresh(raw)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	token = tokenOfRefresh(t, w)
	if c := findRefreshCookie(w); c != nil && c.MaxAge > 0 {
		newRaw = c.Value
	}
	return token, newRaw
}

func tokenOfRefresh(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp RefreshResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Token)
	return resp.Token
}

func (e *sidEnv) activeIn(t *testing.T, family string) int64 {
	t.Helper()
	n, err := e.refresh.CountActiveInFamily(context.Background(), family)
	require.NoError(t, err)
	return n
}

func (e *sidEnv) sessionBlocked(t *testing.T, family string) bool {
	t.Helper()
	blocked, err := e.store.IsSessionBlocked(context.Background(), family)
	require.NoError(t, err)
	return blocked
}

func (e *sidEnv) jtiBlocked(t *testing.T, token string) bool {
	t.Helper()
	claims, err := middleware.ValidateJWT(token, e.cfg.JWTSecret)
	require.NoError(t, err)
	blocked, err := e.store.IsTokenBlocked(context.Background(), claims.ID)
	require.NoError(t, err)
	return blocked
}

func jtiOf(t *testing.T, e *sidEnv, token string) string {
	t.Helper()
	claims, err := middleware.ValidateJWT(token, e.cfg.JWTSecret)
	require.NoError(t, err)
	return claims.ID
}

func assertRejected(t *testing.T, w *httptest.ResponseRecorder, msg string, what string) {
	t.Helper()
	assert.Equal(t, http.StatusUnauthorized, w.Code, "%s must get 401: %s", what, w.Body.String())
	assert.Contains(t, w.Body.String(), msg, what)
}

func assertCookieCleared(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	cookie := findRefreshCookie(w)
	require.NotNil(t, cookie, "the refresh cookie must be cleared")
	assert.True(t, cookie.MaxAge < 0 && cookie.Value == "", "the refresh cookie must be cleared")
}

func (e *sidEnv) setCountAfterHook(fn func(call int)) {
	e.refresh.mu.Lock()
	defer e.refresh.mu.Unlock()
	e.refresh.countCalls = 0
	e.refresh.countAfterHook = fn
}

func (e *sidEnv) setCallHook(fn func(method string, ctx context.Context)) {
	e.refresh.mu.Lock()
	defer e.refresh.mu.Unlock()
	e.refresh.callHook = fn
}

// ---- acceptance 1: logout with a bearer token revokes its session ----

func TestLogoutSID_BearerWithoutUsableCookieRevokesSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cookie string
	}{
		{name: "no cookie", cookie: ""},
		{name: "cookie names no stored token", cookie: "raw-token-unknown-to-the-store"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSIDEnv(t, nil, nil)
			a := env.loginSession(t)
			b := env.loginSession(t)

			w := env.doLogout(a.token, tt.cookie)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			env.assertLogoutSideEffects(t, w, jtiOf(t, env, a.token))

			assert.Equal(t, int64(0), env.activeIn(t, a.family), "the session of the bearer token is revoked")
			assert.True(t, env.sessionBlocked(t, a.family))
			assert.Equal(t, int64(1), env.activeIn(t, b.family), "the other session stays active")
			assert.False(t, env.sessionBlocked(t, b.family))

			rw := env.postRefresh(a.raw)
			assert.Equal(t, http.StatusUnauthorized, rw.Code, "the refresh token of the session must not work: %s", rw.Body.String())
			assert.NotContains(t, rw.Body.String(), `"token"`)

			assertRejected(t, env.ping(env.signFor(t, a.family)), "Session revoked", "another access token of the session")
			assert.Equal(t, http.StatusOK, env.ping(b.token).Code, "the other session keeps working")
			_, _ = env.refreshOK(t, b.raw)
		})
	}
}

func TestLogoutSID_NothingToEnd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// bearer builds the Authorization token for the session a.
		bearer func(t *testing.T, env *sidEnv, a sidSession) string
	}{
		{name: "neither cookie nor bearer", bearer: func(*testing.T, *sidEnv, sidSession) string { return "" }},
		{name: "garbage bearer", bearer: func(*testing.T, *sidEnv, sidSession) string { return "not-a-jwt" }},
		{
			name: "expired bearer with the session of a",
			bearer: func(t *testing.T, env *sidEnv, a sidSession) string {
				token, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
					UserID: "uid-1", Username: "alice", Role: "user",
					Secret: env.cfg.JWTSecret, Expiration: -time.Minute, SessionID: a.family,
				})
				require.NoError(t, err)
				return token
			},
		},
		{
			name: "bearer signed with another secret and the session of a",
			bearer: func(t *testing.T, env *sidEnv, a sidSession) string {
				token, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
					UserID: "uid-1", Username: "alice", Role: "user",
					Secret: "another-secret-key-long-enough", Expiration: time.Hour, SessionID: a.family,
				})
				require.NoError(t, err)
				return token
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSIDEnv(t, nil, nil)
			a := env.loginSession(t)

			w := env.doLogout(tt.bearer(t, env, a), "")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assertCookieCleared(t, w)

			assert.Equal(t, int64(1), env.activeIn(t, a.family), "nothing may be revoked")
			assert.False(t, env.sessionBlocked(t, a.family), "nothing may be blocked")
			users, tokens := env.revoker.calls()
			assert.Empty(t, users)
			assert.Empty(t, tokens, "no socket may be closed")
			assert.Equal(t, http.StatusOK, env.ping(a.token).Code, "the session keeps working")
		})
	}
}

// A valid bearer token without a session (an SSO CLI token) blocks its jti
// and nothing else.
func TestLogoutSID_BearerWithoutSessionBlocksOnlyItsJTI(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	a := env.loginSession(t)
	cli := env.signFor(t, "")

	w := env.doLogout(cli, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	env.assertLogoutSideEffects(t, w, jtiOf(t, env, cli))
	assert.Equal(t, int64(1), env.activeIn(t, a.family))
	assert.False(t, env.sessionBlocked(t, a.family))
	assert.Equal(t, http.StatusOK, env.ping(a.token).Code)
	assertRejected(t, env.ping(cli), "Token has been revoked", "the presented token")
}

// ---- acceptance 2: every access token of the session is rejected ----

func TestLogoutSID_OtherAccessTokensOfSessionGet401(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	a := env.loginSession(t)

	tok1, raw1 := env.refreshOK(t, a.raw) // rotation: new jti, same sid
	tok2, none := env.refreshOK(t, a.raw) // grace refresh with the consumed token
	require.Empty(t, none, "a grace refresh sets no cookie")
	for _, tok := range []string{a.token, tok1, tok2} {
		require.Equal(t, http.StatusOK, env.ping(tok).Code, "control: works before the logout")
	}

	w := env.doLogout(tok1, raw1)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assertRejected(t, env.ping(tok1), "Token has been revoked", "the presented token")
	assertRejected(t, env.ping(a.token), "Session revoked", "the access token of the login")
	assertRejected(t, env.ping(tok2), "Session revoked", "the access token of the grace refresh")
	assertRejected(t, env.ping(env.signFor(t, a.family)), "Session revoked", "a token signed after the logout")
	assert.Equal(t, int64(0), env.activeIn(t, a.family))
}

func TestLogoutSID_WebSocketOfLoggedOutSession(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	a := env.loginSession(t)
	b := env.loginSession(t)
	other := env.signFor(t, a.family)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })
	handler := NewWebSocketHandler(hub, "*", env.cfg.JWTSecret).WithRevocationChecks(env.guarded, env.users)
	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	// Control: before the logout the other token of the session upgrades.
	conn, resp, err := gorilla.DefaultDialer.Dial(wsURL+"?token="+other, nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
	_ = conn.Close()

	require.Equal(t, http.StatusOK, env.doLogout(a.token, a.raw).Code)

	conn, resp, err = gorilla.DefaultDialer.Dial(wsURL+"?token="+other, nil)
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "Session revoked", body["error"])

	// The other session of the user still upgrades.
	conn, resp, err = gorilla.DefaultDialer.Dial(wsURL+"?token="+b.token, nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
	_ = conn.Close()
}

// The hub revalidation closes the open sockets of the logged-out session (also
// those of other access tokens of it) and leaves the sockets of other
// sessions open.
func TestLogoutSID_RevalidationClosesSocketsOfSession(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	a := env.loginSession(t)
	b := env.loginSession(t)
	a2 := env.signFor(t, a.family)

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(func() { hub.Shutdown() })
	handler := NewWebSocketHandler(hub, "*", env.cfg.JWTSecret).WithRevocationChecks(env.guarded, env.users)
	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token="

	dial := func(token string) *gorilla.Conn {
		conn, resp, err := gorilla.DefaultDialer.Dial(wsURL+token, nil)
		require.NoError(t, err)
		_ = resp.Body.Close()
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	connA, connA2, connB := dial(a.token), dial(a2), dial(b.token)
	waitForHubClients(t, hub, 3)

	hub.StartRevalidation(20*time.Millisecond, NewWebSocketRevocationChecker(env.guarded, env.users, nil))

	// The logout closes the socket of the presented token itself
	// (DisconnectToken on the hub). Here the fake revoker records only, so
	// the revalidation alone must close both sockets of the session.
	require.Equal(t, http.StatusOK, env.doLogout(a.token, a.raw).Code)

	for name, conn := range map[string]*gorilla.Conn{"login token": connA, "other token": connA2} {
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
		_, _, err := conn.ReadMessage()
		var closeErr *gorilla.CloseError
		require.True(t, errors.As(err, &closeErr), "%s: want a close frame, got %v", name, err)
		assert.Equal(t, gorilla.ClosePolicyViolation, closeErr.Code, name)
	}
	waitForHubClients(t, hub, 1)

	// The socket of the other session stays open across more passes.
	require.NoError(t, connB.SetReadDeadline(time.Now().Add(200*time.Millisecond)))
	_, _, err := connB.ReadMessage()
	var closeErr *gorilla.CloseError
	assert.False(t, errors.As(err, &closeErr), "the socket of the other session must stay open, got %v", err)
	assert.Equal(t, 1, hub.ClientCount())
}

// ---- acceptance 3: other sessions keep working ----

func TestLogoutSID_OtherSessionKeepsWorking(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	a := env.loginSession(t)
	b := env.loginSession(t)

	w := env.doLogout(a.token, a.raw)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	env.assertLogoutSideEffects(t, w, jtiOf(t, env, a.token))

	assert.Equal(t, http.StatusOK, env.ping(b.token).Code)
	newToken, newRaw := env.refreshOK(t, b.raw)
	assert.Equal(t, http.StatusOK, env.ping(newToken).Code, "a token of a refresh of the other session")
	_, _ = env.refreshOK(t, newRaw)
	assert.False(t, env.userBlockedNow(t), "a logout is not a user block")
}

// ---- acceptance 4: logout-all ----

func TestLogoutAllSID_EndsEverySessionAndAllowsLogin(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	a := env.loginSession(t)
	b := env.loginSession(t)
	c := env.loginSession(t)
	b2, _ := env.refreshOK(t, b.raw)

	w := env.doLogoutAll(a.token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assertCookieCleared(t, w)
	users, _ := env.revoker.calls()
	assert.Equal(t, []string{"uid-1"}, users)

	assertRejected(t, env.ping(a.token), "Token has been revoked", "the presented token")
	for name, tok := range map[string]string{"session b": b.token, "session b, later token": b2, "session c": c.token} {
		assertRejected(t, env.ping(tok), "Session revoked", name)
	}
	for _, s := range []sidSession{a, b, c} {
		assert.Equal(t, int64(0), env.activeIn(t, s.family))
		assert.Equal(t, http.StatusUnauthorized, env.postRefresh(s.raw).Code, "no refresh after logout-all")
	}

	// A user block covers tokens issued up to its millisecond. A login in the
	// same millisecond would count as issued before it, so wait it out.
	time.Sleep(10 * time.Millisecond)
	fresh := env.loginSession(t)
	assert.Equal(t, http.StatusOK, env.ping(fresh.token).Code, "a login after logout-all works")
	_, _ = env.refreshOK(t, fresh.raw)
}

// ---- acceptance 5: cookie and bearer of different sessions ----

func TestLogoutSID_CookieAndBearerOfDifferentSessions(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	a := env.loginSession(t)
	b := env.loginSession(t)
	c := env.loginSession(t)

	w := env.doLogout(b.token, a.raw)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	env.assertLogoutSideEffects(t, w, jtiOf(t, env, b.token))

	for name, s := range map[string]sidSession{"cookie session": a, "bearer session": b} {
		assert.Equal(t, int64(0), env.activeIn(t, s.family), name)
		assert.True(t, env.sessionBlocked(t, s.family), name)
		assert.Equal(t, http.StatusUnauthorized, env.postRefresh(s.raw).Code, name)
		assertRejected(t, env.ping(env.signFor(t, s.family)), "Session revoked", name)
	}
	assert.Equal(t, int64(1), env.activeIn(t, c.family))
	assert.False(t, env.sessionBlocked(t, c.family))
	assert.Equal(t, http.StatusOK, env.ping(c.token).Code, "the third session keeps working")
	_, _ = env.refreshOK(t, c.raw)
}

// ---- acceptance 6: races ----

// A refresh whose session re-check (CountActiveInFamily) has passed, and a
// logout, logout-all or admin revoke that runs right after it: the refresh
// answers 200, and the access token it hands out must still be rejected.
func TestLogoutSID_RefreshRacingRevokeAfterRecheck(t *testing.T) {
	t.Parallel()

	const (
		newPassword = `{"password":"a-new-password-1"}`
		newRole     = `{"role":"devops"}`
	)

	tests := []struct {
		name string
		// graceRefresh: the refresh presents a consumed token (the re-check
		// is then count call 2, after the grace eligibility check).
		graceRefresh bool
		// revoke runs inside the hook. raw is the cookie of a, tok its token.
		revoke func(t *testing.T, env *sidEnv, a sidSession, raw string)
	}{
		{
			name: "logout with the cookie",
			revoke: func(t *testing.T, env *sidEnv, a sidSession, raw string) {
				require.Equal(t, http.StatusOK, env.doLogout("", raw).Code)
			},
		},
		{
			name: "logout with the bearer token only",
			revoke: func(t *testing.T, env *sidEnv, a sidSession, raw string) {
				require.Equal(t, http.StatusOK, env.doLogout(a.token, "").Code)
			},
		},
		{
			name:         "logout, refresh in the reuse grace",
			graceRefresh: true,
			revoke: func(t *testing.T, env *sidEnv, a sidSession, raw string) {
				require.Equal(t, http.StatusOK, env.doLogout("", raw).Code)
			},
		},
		{
			name: "logout-all",
			revoke: func(t *testing.T, env *sidEnv, a sidSession, raw string) {
				require.Equal(t, http.StatusOK, env.doLogoutAll(a.token).Code)
			},
		},
		{
			name:         "logout-all, refresh in the reuse grace",
			graceRefresh: true,
			revoke: func(t *testing.T, env *sidEnv, a sidSession, raw string) {
				require.Equal(t, http.StatusOK, env.doLogoutAll(a.token).Code)
			},
		},
		{
			name: "admin password reset",
			revoke: func(t *testing.T, env *sidEnv, a sidSession, raw string) {
				require.Equal(t, http.StatusOK, env.adminPut("/api/v1/users/uid-1/password", newPassword).Code)
			},
		},
		{
			name: "admin role change",
			revoke: func(t *testing.T, env *sidEnv, a sidSession, raw string) {
				require.Equal(t, http.StatusOK, env.adminPut("/api/v1/users/uid-1/role", newRole).Code)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSIDEnv(t, nil, nil)
			a := env.loginSession(t)

			presented := a.raw
			cookieAfter := a.raw
			recheckCall := 1
			if tt.graceRefresh {
				// Consume the token first; it then refreshes inside the grace.
				_, cookieAfter = env.refreshOK(t, a.raw)
				recheckCall = 2
			}

			fired := false
			env.setCountAfterHook(func(call int) {
				if call != recheckCall || fired {
					return
				}
				fired = true
				tt.revoke(t, env, a, cookieAfter)
			})

			w := env.postRefresh(presented)
			require.True(t, fired, "the revoke must run after the session re-check")
			require.Equal(t, http.StatusOK, w.Code,
				"the re-check passed before the revoke, so the refresh answers 200: %s", w.Body.String())
			token := tokenOfRefresh(t, w)

			assertRejected(t, env.ping(token), "Session revoked", "the access token of the racing refresh")
		})
	}
}

// A refresh that completes right before the revoke of logout-all or of an
// admin action (after the user read, before RevokeAllForUser) signs a token
// before the user block, so the block covers it. With the block written before
// the revoke, this token would be issued after the block and stay valid.
func TestLogoutSID_RefreshCompletesBeforeRevokeIsBlocked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		revoke func(t *testing.T, env *sidEnv, a sidSession)
	}{
		{
			name:   "logout-all",
			method: "RevokeAllForUser",
			revoke: func(t *testing.T, env *sidEnv, a sidSession) {
				require.Equal(t, http.StatusOK, env.doLogoutAll(a.token).Code)
			},
		},
		{
			name:   "admin password reset",
			method: "RevokeAllForUser",
			revoke: func(t *testing.T, env *sidEnv, a sidSession) {
				require.Equal(t, http.StatusOK, env.adminPut("/api/v1/users/uid-1/password", `{"password":"a-new-password-1"}`).Code)
			},
		},
		{
			name:   "admin role change",
			method: "RevokeAllForUser",
			revoke: func(t *testing.T, env *sidEnv, a sidSession) {
				require.Equal(t, http.StatusOK, env.adminPut("/api/v1/users/uid-1/role", `{"role":"devops"}`).Code)
			},
		},
		{
			name:   "logout",
			method: "RevokeFamily",
			revoke: func(t *testing.T, env *sidEnv, a sidSession) {
				require.Equal(t, http.StatusOK, env.doLogout("", a.raw).Code)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSIDEnv(t, nil, nil)
			a := env.loginSession(t)

			var raced string
			env.setCallHook(func(method string, _ context.Context) {
				if method != tt.method || raced != "" {
					return
				}
				// The nested refresh starts at least one millisecond after
				// anything that the revoke wrote before this call, so a block
				// written before the revoke would not cover its token.
				time.Sleep(3 * time.Millisecond)
				raced = "pending"
				w := env.postRefresh(a.raw)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				raced = tokenOfRefresh(t, w)
			})

			tt.revoke(t, env, a)
			require.NotEmpty(t, raced)
			require.NotEqual(t, "pending", raced)
			assertRejected(t, env.ping(raced), "Session revoked", "the token of the refresh that completed before the revoke")
		})
	}
}

// ---- acceptance 7: replay detection ----

func TestLogoutSID_ReplayBlocksSession(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, nil, nil)
	a := env.loginSession(t)
	b := env.loginSession(t)

	tok1, raw1 := env.refreshOK(t, a.raw)

	// Control: a replay inside the grace is no theft signal.
	_, none := env.refreshOK(t, a.raw)
	require.Empty(t, none)
	require.Equal(t, http.StatusOK, env.ping(tok1).Code)
	require.False(t, env.sessionBlocked(t, a.family))

	// Replay outside the grace.
	ago := time.Now().UTC().Add(-31 * time.Second)
	env.updateToken(t, a.raw, func(rt *models.RefreshToken) { rt.RotatedAt = &ago })
	w := env.postRefresh(a.raw)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())

	assert.Equal(t, int64(0), env.activeIn(t, a.family), "the family is revoked")
	assert.True(t, env.sessionBlocked(t, a.family), "the family is blocked")
	assert.Equal(t, http.StatusUnauthorized, env.postRefresh(raw1).Code, "the successor cannot refresh")
	assertRejected(t, env.ping(tok1), "Session revoked", "an access token of the replayed family")
	assertRejected(t, env.ping(a.token), "Session revoked", "the login token of the replayed family")

	assert.Equal(t, http.StatusOK, env.ping(b.token).Code, "other sessions stay")
	assert.Equal(t, int64(1), env.activeIn(t, b.family))
}

// ---- acceptance 8: failures ----

func TestLogoutSID_BlockSessionFailureStillLogsOut(t *testing.T) {
	t.Parallel()
	env := newSIDEnv(t, errors.New("store down"), nil)
	a := env.loginSession(t)

	w := env.doLogout(a.token, a.raw)
	require.Equal(t, http.StatusOK, w.Code, "a failed block alone must not fail the logout: %s", w.Body.String())
	env.assertLogoutSideEffects(t, w, jtiOf(t, env, a.token))
	assert.Equal(t, int64(0), env.activeIn(t, a.family), "the revoke ran")
	assert.False(t, env.sessionBlocked(t, a.family), "the block failed")
	assert.Equal(t, http.StatusUnauthorized, env.postRefresh(a.raw).Code)
}

func TestLogoutSID_RevokeFailureStillBlocks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// cookie and bearer pick the credentials of the sessions a and b.
		cookieOf  func(a, b sidSession) string
		bearerOf  func(a, b sidSession) string
		wantBlock func(a, b sidSession) []string
		wantJTI   bool
	}{
		{
			name:      "bearer only",
			cookieOf:  func(a, b sidSession) string { return "" },
			bearerOf:  func(a, b sidSession) string { return a.token },
			wantBlock: func(a, b sidSession) []string { return []string{a.family} },
			wantJTI:   true,
		},
		{
			name:      "cookie only",
			cookieOf:  func(a, b sidSession) string { return a.raw },
			bearerOf:  func(a, b sidSession) string { return "" },
			wantBlock: func(a, b sidSession) []string { return []string{a.family} },
		},
		{
			name:      "cookie and bearer of different sessions",
			cookieOf:  func(a, b sidSession) string { return a.raw },
			bearerOf:  func(a, b sidSession) string { return b.token },
			wantBlock: func(a, b sidSession) []string { return []string{a.family, b.family} },
			wantJTI:   true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSIDEnv(t, nil, nil)
			a := env.loginSession(t)
			b := env.loginSession(t)
			c := env.loginSession(t)
			env.refresh.mu.Lock()
			env.refresh.revokeAllErr = errors.New("db down")
			env.refresh.mu.Unlock()

			bearer := tt.bearerOf(a, b)
			w := env.doLogout(bearer, tt.cookieOf(a, b))

			require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
			assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
			assertCookieCleared(t, w)
			for _, family := range tt.wantBlock(a, b) {
				assert.True(t, env.sessionBlocked(t, family), "session %s must be blocked", family)
				assertRejected(t, env.ping(env.signFor(t, family)), "Session revoked", "a token of "+family)
			}
			assert.False(t, env.sessionBlocked(t, c.family))
			assert.Equal(t, http.StatusOK, env.ping(c.token).Code, "an unrelated session keeps working")
			if tt.wantJTI {
				assert.True(t, env.jtiBlocked(t, bearer), "the presented jti must be blocked")
				_, tokens := env.revoker.calls()
				assert.Equal(t, []string{jtiOf(t, env, bearer)}, tokens)
			}
		})
	}
}

func TestLogoutAllSID_FailureCases(t *testing.T) {
	t.Parallel()

	t.Run("revoke error answers 500 but blocks the user", func(t *testing.T) {
		t.Parallel()
		env := newSIDEnv(t, nil, nil)
		a := env.loginSession(t)
		b := env.loginSession(t)
		env.refresh.mu.Lock()
		env.refresh.revokeAllErr = errors.New("db down")
		env.refresh.mu.Unlock()

		w := env.doLogoutAll(a.token)
		require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
		assert.JSONEq(t, `{"error":"Internal server error"}`, w.Body.String())
		assertCookieCleared(t, w)
		assert.True(t, env.jtiBlocked(t, a.token), "the presented jti is blocked")
		assertRejected(t, env.ping(b.token), "Session revoked", "another session after the failed revoke")
		users, _ := env.revoker.calls()
		assert.Equal(t, []string{"uid-1"}, users, "the sockets still close")
	})

	t.Run("block error still revokes and answers 200", func(t *testing.T) {
		t.Parallel()
		env := newSIDEnv(t, nil, errors.New("store down"))
		a := env.loginSession(t)
		b := env.loginSession(t)

		w := env.doLogoutAll(a.token)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assertCookieCleared(t, w)
		for _, s := range []sidSession{a, b} {
			assert.Equal(t, int64(0), env.activeIn(t, s.family))
			assert.Equal(t, http.StatusUnauthorized, env.postRefresh(s.raw).Code)
		}
		assert.True(t, env.jtiBlocked(t, a.token))
	})
}

// ---- revoke-before-block order ----

// A call hook on the revoke checks that no block exists yet when the revoke
// starts (logout, logout-all and the admin actions revoke first).
func TestLogoutSID_RevokesBeforeBlocking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		run    func(env *sidEnv, a sidSession) *httptest.ResponseRecorder
		// blocked reports whether the store holds the block that the call writes.
		blocked func(t *testing.T, env *sidEnv, a sidSession) bool
	}{
		{
			name:   "logout",
			method: "RevokeFamily",
			run:    func(env *sidEnv, a sidSession) *httptest.ResponseRecorder { return env.doLogout(a.token, a.raw) },
			blocked: func(t *testing.T, env *sidEnv, a sidSession) bool {
				return env.sessionBlocked(t, a.family)
			},
		},
		{
			name:   "logout-all",
			method: "RevokeAllForUser",
			run:    func(env *sidEnv, a sidSession) *httptest.ResponseRecorder { return env.doLogoutAll(a.token) },
			blocked: func(t *testing.T, env *sidEnv, a sidSession) bool {
				return env.userBlockedNow(t)
			},
		},
		{
			name:   "admin password reset",
			method: "RevokeAllForUser",
			run: func(env *sidEnv, a sidSession) *httptest.ResponseRecorder {
				return env.adminPut("/api/v1/users/uid-1/password", `{"password":"a-new-password-1"}`)
			},
			blocked: func(t *testing.T, env *sidEnv, a sidSession) bool {
				return env.userBlockedNow(t)
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newSIDEnv(t, nil, nil)
			a := env.loginSession(t)

			seen := false
			var blockedAtRevoke bool
			env.setCallHook(func(method string, _ context.Context) {
				if method == tt.method && !seen {
					seen = true
					blockedAtRevoke = tt.blocked(t, env, a)
				}
			})
			w := tt.run(env, a)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.True(t, seen)
			assert.False(t, blockedAtRevoke, "the block must not exist when the revoke starts")
			assert.True(t, tt.blocked(t, env, a), "the block exists after the call")
		})
	}
}

// userBlockedNow reports whether uid-1 has a user block. A token issued long
// ago counts as issued before any block, so the answer does not depend on the
// clock.
func (e *sidEnv) userBlockedNow(t *testing.T) bool {
	t.Helper()
	blocked, err := e.store.IsUserBlocked(context.Background(), "uid-1", time.Unix(1, 0))
	require.NoError(t, err)
	return blocked
}
