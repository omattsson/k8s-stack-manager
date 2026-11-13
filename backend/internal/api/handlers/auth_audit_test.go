package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// chanAudit sends each entry to a channel. block, when set, holds Create
// until it is closed.
type chanAudit struct {
	ch    chan models.AuditLog
	block chan struct{}
}

func newChanAudit() *chanAudit { return &chanAudit{ch: make(chan models.AuditLog, 16)} }

func (a *chanAudit) Create(_ context.Context, e *models.AuditLog) error {
	if a.block != nil {
		<-a.block
	}
	a.ch <- *e
	return nil
}

func (a *chanAudit) List(context.Context, models.AuditLogFilters) (*models.AuditLogResult, error) {
	return &models.AuditLogResult{}, nil
}

// one waits for one entry.
func (a *chanAudit) one(t *testing.T) models.AuditLog {
	t.Helper()
	select {
	case e := <-a.ch:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no audit entry")
		return models.AuditLog{}
	}
}

// none checks that no entry arrives.
func (a *chanAudit) none(t *testing.T) {
	t.Helper()
	select {
	case e := <-a.ch:
		t.Fatalf("unexpected audit entry %+v", e)
	case <-time.After(150 * time.Millisecond):
	}
}

type authAuditFixture struct {
	router  *gin.Engine
	audit   *chanAudit
	users   *MockUserRepository
	refresh *MockRefreshTokenRepository
}

func newAuthAuditFixture(t *testing.T, mutate func(*config.AuthConfig)) *authAuditFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &authAuditFixture{audit: newChanAudit(), users: NewMockUserRepository(), refresh: NewMockRefreshTokenRepository()}
	seedUser(t, f.users, "uid-1", "alice", "secret", "user")
	cfg := testAuthConfigWithRefresh()
	if mutate != nil {
		mutate(cfg)
	}
	h := NewAuthHandler(f.users, cfg, &config.OIDCConfig{})
	h.SetRefreshTokenRepo(f.refresh)
	store := sessionstore.NewMemoryStore()
	t.Cleanup(store.Stop)
	h.SetSessionStore(store)
	h.SetAuditLogger(f.audit)
	r := gin.New()
	r.Use(middleware.RequestID())
	auth := r.Group("/api/v1/auth")
	auth.POST("/login", h.Login)
	auth.POST("/refresh", h.Refresh)
	auth.POST("/logout", h.Logout)
	auth.POST("/logout-all", func(c *gin.Context) {
		c.Set("userID", "uid-1")
		c.Set("username", "alice")
		c.Set("role", "user")
		c.Set("authMethod", middleware.AuthMethodAPIKey)
		c.Set("apiKeyID", "key-1")
		c.Next()
	}, h.LogoutAll)
	f.router = r
	return f
}

func (f *authAuditFixture) post(t *testing.T, path, body string, mod func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.7:5555"
	if mod != nil {
		mod(req)
	}
	f.router.ServeHTTP(w, req)
	return w
}

// login signs alice in and returns the access token and the refresh cookie.
func (f *authAuditFixture) login(t *testing.T) (string, *http.Cookie) {
	t.Helper()
	w := f.post(t, "/api/v1/auth/login", `{"username":"alice","password":"secret"}`, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp LoginResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	for _, c := range w.Result().Cookies() {
		if c.Name == "refresh_token" {
			return resp.Token, c
		}
	}
	t.Fatal("no refresh cookie")
	return "", nil
}

func TestAuthAudit_Login(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       string
		disable    bool
		wantAction string
		wantUserID string
		wantUser   string
		wantReason string
	}{
		{name: "success", body: `{"username":"alice","password":"secret"}`, wantAction: "login", wantUserID: "uid-1", wantUser: "alice"},
		{name: "wrong password", body: `{"username":"alice","password":"nope"}`, wantAction: "login_failed", wantUserID: "uid-1", wantUser: "alice", wantReason: "password_mismatch"},
		{name: "disabled", body: `{"username":"alice","password":"secret"}`, disable: true, wantAction: "login_failed", wantUserID: "uid-1", wantUser: "alice", wantReason: "disabled"},
		{name: "unknown user", body: `{"username":"mallory","password":"x"}`, wantAction: "login_failed", wantReason: "user_not_found"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newAuthAuditFixture(t, nil)
			if tt.disable {
				u, _ := f.users.FindByID(context.Background(), "uid-1")
				u.Disabled = true
				require.NoError(t, f.users.Update(u))
			}
			w := f.post(t, "/api/v1/auth/login", tt.body, nil)
			e := f.audit.one(t)
			assert.Equal(t, tt.wantAction, e.Action)
			assert.Equal(t, "user", e.EntityType)
			assert.Equal(t, tt.wantUserID, e.UserID)
			assert.Equal(t, tt.wantUserID, e.EntityID)
			assert.Equal(t, tt.wantUser, e.Username)
			assert.Equal(t, tt.wantUser, e.EntityName, "the user entity is named by its username")
			assert.Equal(t, models.AuditAuthPassword, e.AuthMethod)
			assert.Equal(t, "198.51.100.7", e.ClientIP)
			assert.Equal(t, w.Header().Get("X-Request-ID"), e.RequestID)
			if tt.wantReason != "" {
				assert.JSONEq(t, `{"reason":"`+tt.wantReason+`"}`, e.Details)
			}
			f.audit.none(t)
		})
	}
}

func TestLogin_AuditUnknownUserNoUsername(t *testing.T) {
	t.Parallel()
	f := newAuthAuditFixture(t, nil)
	f.post(t, "/api/v1/auth/login", `{"username":"hunter2-typed-as-user","password":"x"}`, nil)
	e := f.audit.one(t)
	assert.Empty(t, e.Username)
	assert.Empty(t, e.UserID)
	raw, _ := json.Marshal(e)
	assert.NotContains(t, string(raw), "hunter2-typed-as-user")
}

func TestLogin_AuditNeverHoldsPassword(t *testing.T) {
	t.Parallel()
	for _, pw := range []string{"secret", "Wrong-Password-42"} {
		f := newAuthAuditFixture(t, nil)
		f.post(t, "/api/v1/auth/login", `{"username":"alice","password":"`+pw+`"}`, nil)
		raw, _ := json.Marshal(f.audit.one(t))
		assert.NotContains(t, string(raw), pw)
	}
}

func TestLogin_AuditWriteBlocks(t *testing.T) {
	t.Parallel()
	f := newAuthAuditFixture(t, nil)
	f.audit.block = make(chan struct{})
	start := time.Now()
	w := f.post(t, "/api/v1/auth/login", `{"username":"alice","password":"secret"}`, nil)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Less(t, time.Since(start), time.Second, "a blocked audit write must not delay the login")
	close(f.audit.block)
	assert.Equal(t, "login", f.audit.one(t).Action)
}

func TestAuthAudit_Logout(t *testing.T) {
	t.Parallel()
	t.Run("bearer", func(t *testing.T) {
		t.Parallel()
		f := newAuthAuditFixture(t, nil)
		token, _ := f.login(t)
		f.audit.one(t)
		f.post(t, "/api/v1/auth/logout", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) })
		e := f.audit.one(t)
		assert.Equal(t, "logout", e.Action)
		assert.Equal(t, "uid-1", e.UserID)
		assert.Equal(t, "alice", e.Username)
		assert.Equal(t, middleware.AuthMethodJWT, e.AuthMethod)
	})
	t.Run("refresh cookie only", func(t *testing.T) {
		t.Parallel()
		f := newAuthAuditFixture(t, nil)
		_, cookie := f.login(t)
		f.audit.one(t)
		f.post(t, "/api/v1/auth/logout", "", func(r *http.Request) { r.AddCookie(cookie) })
		e := f.audit.one(t)
		assert.Equal(t, "logout", e.Action)
		assert.Equal(t, "uid-1", e.UserID)
		assert.Equal(t, "alice", e.Username, "username from the lookup")
		assert.Equal(t, "alice", e.EntityName)
	})
	t.Run("no credentials", func(t *testing.T) {
		t.Parallel()
		f := newAuthAuditFixture(t, nil)
		f.post(t, "/api/v1/auth/logout", "", nil)
		f.audit.none(t)
	})
	t.Run("logout-all with an API key", func(t *testing.T) {
		t.Parallel()
		f := newAuthAuditFixture(t, nil)
		f.post(t, "/api/v1/auth/logout-all", "", nil)
		e := f.audit.one(t)
		assert.Equal(t, "logout_all", e.Action)
		assert.Equal(t, "alice", e.Username)
		assert.Equal(t, middleware.AuthMethodAPIKey, e.AuthMethod)
		assert.Equal(t, "key-1", e.APIKeyID)
	})
}

func TestAuthAudit_Refresh(t *testing.T) {
	t.Parallel()
	refresh := func(t *testing.T, f *authAuditFixture, c *http.Cookie) *httptest.ResponseRecorder {
		return f.post(t, "/api/v1/auth/refresh", "", func(r *http.Request) { r.AddCookie(c) })
	}
	t.Run("reuse revokes and is audited", func(t *testing.T) {
		t.Parallel()
		f := newAuthAuditFixture(t, nil)
		_, cookie := f.login(t)
		f.audit.one(t)
		require.Equal(t, http.StatusOK, refresh(t, f, cookie).Code, "normal refresh")
		f.audit.none(t)
		require.Equal(t, http.StatusUnauthorized, refresh(t, f, cookie).Code, "replay")
		e := f.audit.one(t)
		assert.Equal(t, "refresh_reuse_revoked", e.Action)
		assert.Equal(t, "uid-1", e.UserID)
		assert.Equal(t, "alice", e.Username)
		assert.Contains(t, e.Details, `"token_id"`)
	})
	t.Run("grace refresh is not audited", func(t *testing.T) {
		t.Parallel()
		f := newAuthAuditFixture(t, func(c *config.AuthConfig) { c.RefreshReuseGrace = 30 * time.Second })
		_, cookie := f.login(t)
		f.audit.one(t)
		require.Equal(t, http.StatusOK, refresh(t, f, cookie).Code)
		require.Equal(t, http.StatusOK, refresh(t, f, cookie).Code, "grace")
		f.audit.none(t)
	})
}

func TestAuthAudit_OIDC(t *testing.T) {
	t.Parallel()
	callback := func(t *testing.T, h *OIDCHandler, store sessionstore.SessionStore, state string) {
		t.Helper()
		_ = store.SaveOIDCState(context.Background(), state, sessionstore.OIDCStateData{CodeVerifier: "test-verifier", RedirectURL: "/"}, 5*time.Minute)
		r := gin.New()
		r.Use(middleware.RequestID())
		r.GET("/api/v1/auth/oidc/callback", h.Callback)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=test-code&state="+state, nil))
		require.Equal(t, http.StatusFound, w.Code)
	}
	t.Run("success", func(t *testing.T) {
		t.Parallel()
		h, store, _ := newOIDCHandlerSetup(t, false)
		a := newChanAudit()
		h.SetAuditLogger(a)
		callback(t, h, store, "st-ok")
		e := a.one(t)
		assert.Equal(t, "oidc_login", e.Action)
		assert.Equal(t, models.AuditAuthOIDC, e.AuthMethod)
		assert.NotEmpty(t, e.UserID)
		assert.Equal(t, "oidcuser@example.com", e.Username, "the local username of the provisioned user")
		a.none(t)
	})
	t.Run("disabled user in provisioning", func(t *testing.T) {
		t.Parallel()
		h, store, users := newOIDCHandlerSetup(t, false)
		ext := "oidc-user-001"
		require.NoError(t, users.Create(context.Background(), &models.User{ID: "u-dis", Username: "oidcuser", AuthProvider: "oidc", ExternalID: &ext, Disabled: true}))
		a := newChanAudit()
		h.SetAuditLogger(a)
		callback(t, h, store, "st-dis")
		e := a.one(t)
		assert.Equal(t, "login_failed", e.Action)
		assert.Equal(t, models.AuditAuthOIDC, e.AuthMethod)
		assert.Equal(t, "oidcuser", e.Username)
		assert.JSONEq(t, `{"reason":"disabled"}`, e.Details)
		a.none(t)
	})
	t.Run("invalid state writes nothing", func(t *testing.T) {
		t.Parallel()
		h, _, _ := newOIDCHandlerSetup(t, false)
		a := newChanAudit()
		h.SetAuditLogger(a)
		r := gin.New()
		r.GET("/cb", h.Callback)
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/cb?code=x&state=nope", nil))
		a.none(t)
	})
}
