package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/models"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wsKeyFixture holds the repositories and the raw keys of the API-key
// WebSocket tests.
type wsKeyFixture struct {
	users *MockUserRepository
	keys  *MockAPIKeyRepository
	raw   map[string]string // key ID -> "sk_<raw>"
}

// newWSKeyFixture creates the users u-active, u-disabled and the keys:
// key-ok (u-active, no expiry), key-other (u-active), key-expired
// (u-active, expired), key-disabled (u-disabled) and key-orphan (user
// u-deleted, which does not exist).
func newWSKeyFixture(t *testing.T) *wsKeyFixture {
	t.Helper()
	f := &wsKeyFixture{users: NewMockUserRepository(), keys: NewMockAPIKeyRepository(), raw: map[string]string{}}
	require.NoError(t, f.users.Create(&models.User{ID: "u-active", Username: "active", Role: "developer"}))
	require.NoError(t, f.users.Create(&models.User{ID: "u-disabled", Username: "disabled", Role: "developer", Disabled: true}))
	past := time.Now().Add(-time.Hour)
	for _, k := range []struct {
		expires *time.Time
		id      string
		userID  string
	}{
		{id: "key-ok", userID: "u-active"},
		{id: "key-other", userID: "u-active"},
		{id: "key-expired", userID: "u-active", expires: &past},
		{id: "key-disabled", userID: "u-disabled"},
		{id: "key-orphan", userID: "u-deleted"},
	} {
		f.addKey(t, k.id, k.userID, k.expires)
	}
	return f
}

func (f *wsKeyFixture) addKey(t *testing.T, id, userID string, expires *time.Time) {
	t.Helper()
	raw, prefix, hash, err := models.GenerateAPIKey()
	require.NoError(t, err)
	require.NoError(t, f.keys.Create(&models.APIKey{ID: id, UserID: userID, Name: id, KeyHash: hash, Prefix: prefix, ExpiresAt: expires}))
	f.raw[id] = "sk_" + raw
}

// startWSKeyServer starts a test server with /ws (API keys on) and the API
// key delete route of the caller u-active.
func startWSKeyServer(t *testing.T, hub *websocket.Hub, f *wsKeyFixture) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler := NewWebSocketHandler(hub, "*", wsTestJWTSecret).
		WithRevocationChecks(nil, f.users).
		WithAPIKeys(f.keys)
	keyHandler := NewAPIKeyHandler(f.keys, f.users, nil)
	keyHandler.SetWebSocketRevoker(hub)

	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)
	api := router.Group("/api/v1", injectAuthContext("u-active", "developer"))
	api.DELETE("/users/:id/api-keys/:keyId", keyHandler.DeleteAPIKey)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

func dialWSWithKey(t *testing.T, server *httptest.Server, rawKey string) *gorilla.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, resp, err := gorilla.DefaultDialer.Dial(wsURL, http.Header{"X-API-Key": []string{rawKey}})
	require.NoError(t, err)
	_ = resp.Body.Close()
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readWSClose reads until the close frame and returns it.
func readWSClose(t *testing.T, conn *gorilla.Conn) *gorilla.CloseError {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		var closeErr *gorilla.CloseError
		require.True(t, errors.As(err, &closeErr), "want a close frame, got %v", err)
		return closeErr
	}
}

// assertWSOpen checks that no close frame arrives within a short time. The
// read timeout breaks the connection: call it only as the last check.
func assertWSOpen(t *testing.T, conn *gorilla.Conn) {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(200*time.Millisecond)))
	_, _, err := conn.ReadMessage()
	var closeErr *gorilla.CloseError
	assert.False(t, errors.As(err, &closeErr), "socket closed: %v", err)
}

func TestHandleWebSocket_APIKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		keyID      string // key whose raw value goes in X-API-Key
		rawKey     string // literal X-API-Key value (when keyID is empty)
		query      bool   // send the key as ?api_key= instead of the header
		bearer     bool   // also send a valid JWT in Authorization
		noAPIKeys  bool   // handler without WithAPIKeys
		lookupErr  error
		deleted    bool // delete the key before the upgrade (revoked key)
		wantStatus int
		wantError  string
	}{
		{name: "valid key", keyID: "key-ok", wantStatus: http.StatusSwitchingProtocols},
		{name: "revoked (deleted) key", keyID: "key-ok", deleted: true, wantStatus: http.StatusUnauthorized, wantError: "Invalid API key"},
		{name: "expired key", keyID: "key-expired", wantStatus: http.StatusUnauthorized, wantError: "API key expired"},
		{name: "disabled user", keyID: "key-disabled", wantStatus: http.StatusUnauthorized, wantError: "Account disabled"},
		{name: "deleted user", keyID: "key-orphan", wantStatus: http.StatusUnauthorized, wantError: "Invalid API key"},
		{name: "unknown key", rawKey: "sk_0123456789abcdef0123456789abcdef", wantStatus: http.StatusUnauthorized, wantError: "Invalid API key"},
		{name: "short key", rawKey: "sk_abc", wantStatus: http.StatusUnauthorized, wantError: "Invalid API key"},
		{name: "lookup error does not leak", keyID: "key-ok", lookupErr: errors.New("dial tcp 10.0.0.1:3306: refused"), wantStatus: http.StatusUnauthorized, wantError: "Invalid API key"},
		{name: "key in query parameter is ignored", keyID: "key-ok", query: true, wantStatus: http.StatusUnauthorized, wantError: "Authentication required"},
		{name: "API keys not enabled", keyID: "key-ok", noAPIKeys: true, wantStatus: http.StatusUnauthorized, wantError: "Authentication required"},
		{name: "JWT in Authorization wins over the key", keyID: "key-expired", bearer: true, wantStatus: http.StatusSwitchingProtocols},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)

			hub := websocket.NewHub()
			go hub.Run()
			defer hub.Shutdown()

			f := newWSKeyFixture(t)
			raw := tt.rawKey
			if tt.keyID != "" {
				raw = f.raw[tt.keyID]
			}
			if tt.deleted {
				require.NoError(t, f.keys.Delete("u-active", tt.keyID))
			}
			if tt.lookupErr != nil {
				f.keys.SetFindError(tt.lookupErr)
			}

			handler := NewWebSocketHandler(hub, "*", wsTestJWTSecret).WithRevocationChecks(nil, f.users)
			if !tt.noAPIKeys {
				handler.WithAPIKeys(f.keys)
			}
			router := gin.New()
			router.GET("/ws", handler.HandleWebSocket)
			server := httptest.NewServer(router)
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
			header := http.Header{}
			if tt.query {
				wsURL += "?api_key=" + raw
			} else {
				header.Set("X-API-Key", raw)
			}
			if tt.bearer {
				header.Set("Authorization", "Bearer "+signWSToken(t, "u-active", "jti-1", time.Now()))
			}

			conn, resp, err := gorilla.DefaultDialer.Dial(wsURL, header)
			if conn != nil {
				defer conn.Close()
			}
			require.NotNil(t, resp)
			defer resp.Body.Close()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			if tt.wantStatus == http.StatusSwitchingProtocols {
				require.NoError(t, err)
				waitForHubClients(t, hub, 1)
				return
			}
			require.Error(t, err)
			var body map[string]string
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
			assert.Equal(t, tt.wantError, body["error"])
			assert.Equal(t, 0, hub.ClientCount())
		})
	}
}

// TestHandleWebSocket_APIKeyDeleteClosesSocket checks the full path: the
// delete of an API key closes the sockets opened with it (close code 1008)
// and keeps the sockets of other keys open.
func TestHandleWebSocket_APIKeyDeleteClosesSocket(t *testing.T) {
	t.Parallel()

	hub := websocket.NewHub()
	go hub.Run()
	defer hub.Shutdown()

	f := newWSKeyFixture(t)
	server := startWSKeyServer(t, hub, f)
	revoked := dialWSWithKey(t, server, f.raw["key-ok"])
	other := dialWSWithKey(t, server, f.raw["key-other"])
	waitForHubClients(t, hub, 2)

	req, err := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/users/u-active/api-keys/key-ok", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	closeErr := readWSClose(t, revoked)
	assert.Equal(t, gorilla.ClosePolicyViolation, closeErr.Code)
	assert.Equal(t, "session revoked", closeErr.Text)
	waitForHubClients(t, hub, 1)
	assertWSOpen(t, other)
}

// TestHandleWebSocket_APIKeyExpiryClosesSocket checks that a socket opened
// with a key that has an expiry closes at that time.
func TestHandleWebSocket_APIKeyExpiryClosesSocket(t *testing.T) {
	t.Parallel()

	hub := websocket.NewHub()
	go hub.Run()
	defer hub.Shutdown()

	f := newWSKeyFixture(t)
	soon := time.Now().Add(300 * time.Millisecond)
	f.addKey(t, "key-soon", "u-active", &soon)
	server := startWSKeyServer(t, hub, f)
	conn := dialWSWithKey(t, server, f.raw["key-soon"])

	closeErr := readWSClose(t, conn)
	assert.Equal(t, gorilla.ClosePolicyViolation, closeErr.Code)
	assert.Equal(t, "api key expired", closeErr.Text)
	waitForHubClients(t, hub, 0)
}

// TestHandleWebSocket_APIKeyRevalidation checks that the periodic check
// closes a socket whose key or user changed directly in the database (for
// example a delete on another replica without fan-out).
func TestHandleWebSocket_APIKeyRevalidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		change func(t *testing.T, f *wsKeyFixture)
		name   string
	}{
		{name: "key deleted in the database", change: func(t *testing.T, f *wsKeyFixture) {
			require.NoError(t, f.keys.Delete("u-active", "key-ok"))
		}},
		{name: "user disabled in the database", change: func(t *testing.T, f *wsKeyFixture) {
			user, err := f.users.FindByID("u-active")
			require.NoError(t, err)
			user.Disabled = true
			require.NoError(t, f.users.Update(user))
		}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hub := websocket.NewHub()
			go hub.Run()
			defer hub.Shutdown()

			f := newWSKeyFixture(t)
			hub.StartRevalidation(20*time.Millisecond, NewWebSocketRevocationChecker(nil, f.users, f.keys))
			server := startWSKeyServer(t, hub, f)
			conn := dialWSWithKey(t, server, f.raw["key-ok"])
			waitForHubClients(t, hub, 1)
			// Some revalidation runs pass and keep the socket open.
			time.Sleep(100 * time.Millisecond)
			require.Equal(t, 1, hub.ClientCount())

			tt.change(t, f)

			closeErr := readWSClose(t, conn)
			assert.Equal(t, gorilla.ClosePolicyViolation, closeErr.Code)
			waitForHubClients(t, hub, 0)
		})
	}
}

// countingKeyRepo counts the key lookups and can fail FindByIDs.
type countingKeyRepo struct {
	*MockAPIKeyRepository
	batchErr   error
	batchCalls int
	findCalls  int
	prefixHits int
}

func (r *countingKeyRepo) FindByIDs(ids []string) ([]*models.APIKey, error) {
	r.batchCalls++
	if r.batchErr != nil {
		return nil, r.batchErr
	}
	return r.MockAPIKeyRepository.FindByIDs(ids)
}

func (r *countingKeyRepo) FindByID(userID, keyID string) (*models.APIKey, error) {
	r.findCalls++
	return r.MockAPIKeyRepository.FindByID(userID, keyID)
}

func (r *countingKeyRepo) FindByPrefix(prefix string) ([]*models.APIKey, error) {
	r.prefixHits++
	return r.MockAPIKeyRepository.FindByPrefix(prefix)
}

func TestNewWebSocketRevocationChecker_APIKeys(t *testing.T) {
	t.Parallel()

	blockTime := time.Now().Add(-time.Minute)
	identities := []websocket.ClientIdentity{
		{UserID: "u-active", APIKeyID: "key-ok", IssuedAt: blockTime.Add(time.Second)},
		{UserID: "u-active", APIKeyID: "key-gone", IssuedAt: blockTime.Add(time.Second)},
		{UserID: "u-active", APIKeyID: "key-expired", IssuedAt: blockTime.Add(time.Second)},
		{UserID: "u-disabled", APIKeyID: "key-disabled", IssuedAt: blockTime.Add(time.Second)},
		{UserID: "u-other", APIKeyID: "key-other", IssuedAt: blockTime.Add(time.Second)},   // key of another user
		{UserID: "u-active", APIKeyID: "key-other", IssuedAt: blockTime.Add(-time.Second)}, // opened before a user block
		{UserID: "u-active", APIKeyID: "key-other", IssuedAt: blockTime.Add(time.Second)},  // opened after it
	}

	tests := []struct {
		name      string
		batchErr  error
		noKeys    bool
		noUsers   bool
		noBlock   bool
		want      []bool
		wantBatch int
	}{
		{name: "all checks, one key query", want: []bool{false, true, true, true, true, true, false}, wantBatch: 1},
		// A failed key lookup stops the run: everything passes (fail open).
		{name: "key lookup error stops the run", batchErr: errors.New("connection refused"), want: make([]bool, 7), wantBatch: 1},
		{name: "no key repository", noKeys: true, want: []bool{false, false, false, true, true, true, false}}, // u-other does not exist
		{name: "no user repository", noUsers: true, want: []bool{false, true, true, false, true, true, false}, wantBatch: 1},
		{name: "no user block", noBlock: true, want: []bool{false, true, true, true, true, false, false}, wantBatch: 1},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWSKeyFixture(t)
			keys := &countingKeyRepo{MockAPIKeyRepository: f.keys, batchErr: tt.batchErr}
			store := &wsRevocationStore{mockSessionStore: newMockHandlerSessionStore()}
			if !tt.noBlock {
				store.userBlockedAt = map[string]time.Time{"u-active": blockTime}
			}

			var keyRepo models.APIKeyRepository = keys
			if tt.noKeys {
				keyRepo = nil
			}
			var userRepo models.UserRepository = f.users
			if tt.noUsers {
				userRepo = nil
			}
			check := NewWebSocketRevocationChecker(store, userRepo, keyRepo)
			require.NotNil(t, check)

			assert.Equal(t, tt.want, check(context.Background(), identities))
			assert.Equal(t, int32(0), store.tokenChecks.Load(), "no token blocklist query for API-key sockets")
			assert.Equal(t, tt.wantBatch, keys.batchCalls, "batched key lookups")
			assert.Zero(t, keys.findCalls, "no per-key lookup")
		})
	}
}

// TestHandleWebSocket_APIKeyRateLimit checks that the per-IP limit runs
// before the key lookup.
func TestHandleWebSocket_APIKeyRateLimit(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()
	defer hub.Shutdown()

	f := newWSKeyFixture(t)
	keys := &countingKeyRepo{MockAPIKeyRepository: f.keys}
	limiter := NewRateLimiter(1, time.Minute)
	defer limiter.Stop()
	handler := NewWebSocketHandler(hub, "*", wsTestJWTSecret).
		WithRevocationChecks(nil, f.users).
		WithAPIKeys(keys).
		WithAPIKeyRateLimiter(limiter)
	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)
	server := httptest.NewServer(router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	header := http.Header{"X-API-Key": []string{f.raw["key-ok"]}}

	conn, resp, err := gorilla.DefaultDialer.Dial(wsURL, header)
	require.NoError(t, err)
	_ = resp.Body.Close()
	defer conn.Close()

	_, resp, err = gorilla.DefaultDialer.Dial(wsURL, header)
	require.Error(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, 1, keys.prefixHits, "the rejected upgrade must not look up the key")
}

// flipUserRepo returns the user as active on the first lookup and as
// disabled on later lookups: a disable during the upgrade.
type flipUserRepo struct {
	*MockUserRepository
	mu    sync.Mutex
	finds int
}

func (r *flipUserRepo) FindByID(id string) (*models.User, error) {
	r.mu.Lock()
	r.finds++
	n := r.finds
	r.mu.Unlock()
	u, err := r.MockUserRepository.FindByID(id)
	if err == nil && n > 1 {
		u.Disabled = true
	}
	return u, err
}

// TestHandleWebSocket_APIKeyUserRevokedDuringUpgrade checks the check after
// the registration: a user disabled or blocked during the upgrade closes
// the new socket; an older user block does not.
func TestHandleWebSocket_APIKeyUserRevokedDuringUpgrade(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		flipUser  bool
		blockedAt time.Time // zero: no user block
		wantClose bool
	}{
		{name: "user disabled during the upgrade", flipUser: true, wantClose: true},
		{name: "user blocked during the upgrade", blockedAt: time.Now().Add(time.Hour), wantClose: true},
		{name: "user blocked before the upgrade", blockedAt: time.Now().Add(-time.Hour)},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)

			hub := websocket.NewHub()
			go hub.Run()
			defer hub.Shutdown()

			f := newWSKeyFixture(t)
			var users models.UserRepository = f.users
			if tt.flipUser {
				users = &flipUserRepo{MockUserRepository: f.users}
			}
			store := &wsRevocationStore{mockSessionStore: newMockHandlerSessionStore()}
			if !tt.blockedAt.IsZero() {
				store.userBlockedAt = map[string]time.Time{"u-active": tt.blockedAt}
			}
			handler := NewWebSocketHandler(hub, "*", wsTestJWTSecret).
				WithRevocationChecks(store, users).
				WithAPIKeys(f.keys)
			router := gin.New()
			router.GET("/ws", handler.HandleWebSocket)
			server := httptest.NewServer(router)
			defer server.Close()

			conn := dialWSWithKey(t, server, f.raw["key-ok"])
			if tt.wantClose {
				closeErr := readWSClose(t, conn)
				assert.Equal(t, gorilla.ClosePolicyViolation, closeErr.Code)
				waitForHubClients(t, hub, 0)
				return
			}
			waitForHubClients(t, hub, 1)
			assertWSOpen(t, conn)
		})
	}
}

func TestAPIKeyHandler_DeleteClosesWebSockets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		caller    string
		path      string
		wantCode  int
		wantClose []string
	}{
		{name: "delete own key", caller: "u-active", path: "/api/v1/users/u-active/api-keys/key-ok", wantCode: http.StatusNoContent, wantClose: []string{"key-ok"}},
		{name: "unknown key", caller: "u-active", path: "/api/v1/users/u-active/api-keys/key-x", wantCode: http.StatusNotFound},
		{name: "key of another user", caller: "u-other", path: "/api/v1/users/u-active/api-keys/key-ok", wantCode: http.StatusForbidden},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)

			f := newWSKeyFixture(t)
			revoker := &fakeRevoker{}
			h := NewAPIKeyHandler(f.keys, f.users, nil)
			h.SetWebSocketRevoker(revoker)

			r := gin.New()
			r.Use(injectAuthContext(tt.caller, "developer"))
			r.DELETE("/api/v1/users/:id/api-keys/:keyId", h.DeleteAPIKey)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, tt.path, nil))

			require.Equal(t, tt.wantCode, w.Code, w.Body.String())
			assert.Equal(t, tt.wantClose, revoker.keyCalls())
		})
	}
}
