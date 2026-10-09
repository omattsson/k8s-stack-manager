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
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/config"
	"backend/internal/models"
	"backend/internal/sessionstore"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wsRevocationStore is a session store with a token blocklist, user blocks
// with a block time, and an optional error for both checks.
type wsRevocationStore struct {
	*mockSessionStore
	userBlockedAt map[string]time.Time
	blockedJTI    string
	err           error
	tokenChecks   atomic.Int32
}

func (s *wsRevocationStore) IsTokenBlocked(_ context.Context, jti string) (bool, error) {
	s.tokenChecks.Add(1)
	if s.err != nil {
		return false, s.err
	}
	return s.blockedJTI != "" && jti == s.blockedJTI, nil
}

func (s *wsRevocationStore) IsUserBlocked(_ context.Context, userID string, issuedAt time.Time) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	at, ok := s.userBlockedAt[userID]
	return ok && !issuedAt.After(at), nil
}

// signWSToken signs an access token with the given claims for the WebSocket
// tests.
func signWSToken(t *testing.T, userID, jti string, issuedAt time.Time) string {
	t.Helper()
	claims := middleware.Claims{
		UserID:   userID,
		Username: "user-" + userID,
		Role:     "developer",
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(wsTestJWTSecret))
	require.NoError(t, err)
	return token
}

func TestHandleWebSocket_RevocationChecks(t *testing.T) {
	t.Parallel()

	blockTime := time.Now().Add(-time.Minute).Truncate(time.Second)

	tests := []struct {
		name       string
		userID     string
		jti        string
		issuedAt   time.Time
		store      func() *wsRevocationStore // nil: no session store
		noUserRepo bool
		userErr    error
		wantStatus int
		wantError  string
	}{
		{
			name:       "valid token",
			userID:     "u-active",
			jti:        "jti-ok",
			issuedAt:   time.Now(),
			store:      func() *wsRevocationStore { return &wsRevocationStore{mockSessionStore: newMockHandlerSessionStore()} },
			wantStatus: http.StatusSwitchingProtocols,
		},
		{
			name:     "blocked token",
			userID:   "u-active",
			jti:      "jti-logged-out",
			issuedAt: time.Now(),
			store: func() *wsRevocationStore {
				return &wsRevocationStore{mockSessionStore: newMockHandlerSessionStore(), blockedJTI: "jti-logged-out"}
			},
			wantStatus: http.StatusUnauthorized,
			wantError:  "Token has been revoked",
		},
		{
			name:     "blocked user, token issued before the block",
			userID:   "u-active",
			jti:      "jti-old",
			issuedAt: blockTime.Add(-time.Minute),
			store: func() *wsRevocationStore {
				return &wsRevocationStore{mockSessionStore: newMockHandlerSessionStore(), userBlockedAt: map[string]time.Time{"u-active": blockTime}}
			},
			wantStatus: http.StatusUnauthorized,
			wantError:  "Session revoked",
		},
		{
			name:     "blocked user, token issued at the block",
			userID:   "u-active",
			jti:      "jti-same",
			issuedAt: blockTime,
			store: func() *wsRevocationStore {
				return &wsRevocationStore{mockSessionStore: newMockHandlerSessionStore(), userBlockedAt: map[string]time.Time{"u-active": blockTime}}
			},
			wantStatus: http.StatusUnauthorized,
			wantError:  "Session revoked",
		},
		{
			name:     "blocked user, token issued after the block",
			userID:   "u-active",
			jti:      "jti-new",
			issuedAt: blockTime.Add(30 * time.Second),
			store: func() *wsRevocationStore {
				return &wsRevocationStore{mockSessionStore: newMockHandlerSessionStore(), userBlockedAt: map[string]time.Time{"u-active": blockTime}}
			},
			wantStatus: http.StatusSwitchingProtocols,
		},
		{
			name:     "store error fails open",
			userID:   "u-active",
			jti:      "jti-x",
			issuedAt: time.Now(),
			store: func() *wsRevocationStore {
				return &wsRevocationStore{mockSessionStore: newMockHandlerSessionStore(), blockedJTI: "jti-x", err: errors.New("db down")}
			},
			wantStatus: http.StatusSwitchingProtocols,
		},
		{
			name:       "disabled user",
			userID:     "u-disabled",
			jti:        "jti-d",
			issuedAt:   time.Now(),
			wantStatus: http.StatusUnauthorized,
			wantError:  "Session revoked",
		},
		{
			name:       "deleted user",
			userID:     "u-deleted",
			jti:        "jti-del",
			issuedAt:   time.Now(),
			wantStatus: http.StatusUnauthorized,
			wantError:  "Session revoked",
		},
		{
			name:       "user lookup error fails closed",
			userID:     "u-active",
			jti:        "jti-e",
			issuedAt:   time.Now(),
			userErr:    errors.New("connection refused"),
			wantStatus: http.StatusInternalServerError,
			wantError:  msgInternalServerError,
		},
		{
			name:       "no checks configured",
			userID:     "u-deleted",
			jti:        "jti-n",
			issuedAt:   time.Now(),
			noUserRepo: true,
			wantStatus: http.StatusSwitchingProtocols,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)

			hub := websocket.NewHub()
			go hub.Run()
			defer hub.Shutdown()

			userRepo := NewMockUserRepository()
			require.NoError(t, userRepo.Create(&models.User{ID: "u-active", Username: "active"}))
			require.NoError(t, userRepo.Create(&models.User{ID: "u-disabled", Username: "disabled", Disabled: true}))
			if tt.userErr != nil {
				userRepo.SetFindError(tt.userErr)
			}

			handler := NewWebSocketHandler(hub, "*", wsTestJWTSecret)
			switch {
			case tt.noUserRepo:
				// No store and no user repository: only the JWT is checked.
			case tt.store != nil:
				handler.WithRevocationChecks(tt.store(), userRepo)
			default:
				handler.WithRevocationChecks(nil, userRepo)
			}

			router := gin.New()
			router.GET("/ws", handler.HandleWebSocket)
			server := httptest.NewServer(router)
			defer server.Close()

			token := signWSToken(t, tt.userID, tt.jti, tt.issuedAt)
			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
			header := http.Header{"Authorization": []string{"Bearer " + token}}
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

// TestHandleWebSocket_RevokedSocketCloses checks the full path: an open
// socket closes with code 1008 when the hub revokes its token.
func TestHandleWebSocket_RevokedSocketCloses(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()
	defer hub.Shutdown()

	handler := NewWebSocketHandler(hub, "*", wsTestJWTSecret)
	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)
	server := httptest.NewServer(router)
	defer server.Close()

	token := signWSToken(t, "u1", "jti-1", time.Now())
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + token
	conn, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()
	waitForHubClients(t, hub, 1)

	assert.Equal(t, 0, hub.DisconnectToken("jti-other"))
	assert.Equal(t, 1, hub.DisconnectToken("jti-1"))

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, _, err = conn.ReadMessage()
	var closeErr *gorilla.CloseError
	require.True(t, errors.As(err, &closeErr), "want a close frame, got %v", err)
	assert.Equal(t, gorilla.ClosePolicyViolation, closeErr.Code)
	waitForHubClients(t, hub, 0)
}

// fakeRevoker records the calls of websocket.ClientRevoker.
type fakeRevoker struct {
	mu     sync.Mutex
	users  []string
	tokens []string
}

func (f *fakeRevoker) DisconnectUser(userID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users = append(f.users, userID)
	return 1
}

func (f *fakeRevoker) DisconnectToken(tokenID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, tokenID)
	return 1
}

func (f *fakeRevoker) calls() (users, tokens []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.users...), append([]string(nil), f.tokens...)
}

func TestUserHandler_ClosesWebSockets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		method    string
		path      string
		body      string
		wantCode  int
		wantUsers []string
	}{
		{name: "delete", method: http.MethodDelete, path: "/api/v1/users/u-target", wantCode: http.StatusNoContent, wantUsers: []string{"u-target"}},
		{name: "disable", method: http.MethodPut, path: "/api/v1/users/u-target/disable", wantCode: http.StatusOK, wantUsers: []string{"u-target"}},
		{name: "password reset", method: http.MethodPut, path: "/api/v1/users/u-target/password", body: `{"password":"new-password-1"}`, wantCode: http.StatusOK, wantUsers: []string{"u-target"}},
		{name: "enable", method: http.MethodPut, path: "/api/v1/users/u-target/enable", wantCode: http.StatusOK},
		{name: "delete of unknown user", method: http.MethodDelete, path: "/api/v1/users/u-missing", wantCode: http.StatusNotFound},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)

			userRepo := NewMockUserRepository()
			require.NoError(t, userRepo.Create(&models.User{ID: "u-target", Username: "target"}))
			revoker := &fakeRevoker{}
			h := NewUserHandler(userRepo, nil, nil)
			h.SetWebSocketRevoker(revoker)

			r := gin.New()
			r.Use(injectAuthContext("u-admin", "admin"))
			r.DELETE("/api/v1/users/:id", h.DeleteUser)
			r.PUT("/api/v1/users/:id/disable", h.DisableUser)
			r.PUT("/api/v1/users/:id/enable", h.EnableUser)
			r.PUT("/api/v1/users/:id/password", h.ResetUserPassword)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			require.Equal(t, tt.wantCode, w.Code, w.Body.String())
			users, tokens := revoker.calls()
			assert.Equal(t, tt.wantUsers, users)
			assert.Empty(t, tokens)
		})
	}
}

func TestAuthHandler_LogoutClosesWebSockets(t *testing.T) {
	t.Parallel()

	cfg := &config.AuthConfig{JWTSecret: testJWTSecret, JWTExpiration: time.Hour, AccessTokenExpiration: 15 * time.Minute}
	token, err := middleware.GenerateTokenWithOpts(middleware.GenerateTokenOptions{
		UserID: "u1", Username: "one", Role: "developer", Secret: testJWTSecret, Expiration: time.Hour,
	})
	require.NoError(t, err)
	claims, err := middleware.ValidateJWT(token, testJWTSecret)
	require.NoError(t, err)

	tests := []struct {
		name       string
		path       string
		authHeader string
		wantUsers  []string
		wantTokens []string
	}{
		{name: "logout", path: "/api/v1/auth/logout", authHeader: "Bearer " + token, wantTokens: []string{claims.ID}},
		{name: "logout with invalid token", path: "/api/v1/auth/logout", authHeader: "Bearer not-a-jwt"},
		{name: "logout without token", path: "/api/v1/auth/logout"},
		{name: "logout all", path: "/api/v1/auth/logout-all", authHeader: "Bearer " + token, wantUsers: []string{"u1"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gin.SetMode(gin.TestMode)

			revoker := &fakeRevoker{}
			h := NewAuthHandler(NewMockUserRepository(), cfg, &config.OIDCConfig{})
			h.SetSessionStore(newMockHandlerSessionStore())
			h.SetWebSocketRevoker(revoker)

			r := gin.New()
			r.POST("/api/v1/auth/logout", h.Logout)
			r.POST("/api/v1/auth/logout-all", middleware.AuthRequired(testJWTSecret), h.LogoutAll)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			users, tokens := revoker.calls()
			assert.Equal(t, tt.wantUsers, users)
			assert.Equal(t, tt.wantTokens, tokens)
		})
	}
}

// countingUserRepo counts FindByID calls.
type countingUserRepo struct {
	*MockUserRepository
	mu    sync.Mutex
	finds map[string]int
}

func (r *countingUserRepo) FindByID(id string) (*models.User, error) {
	r.mu.Lock()
	r.finds[id]++
	r.mu.Unlock()
	return r.MockUserRepository.FindByID(id)
}

func TestNewWebSocketRevocationChecker(t *testing.T) {
	t.Parallel()

	blockTime := time.Now().Add(-time.Minute).Truncate(time.Second)
	identities := []websocket.ClientIdentity{
		{UserID: "u-active", TokenID: "jti-ok", IssuedAt: time.Now()},
		{UserID: "u-active", TokenID: "jti-logged-out", IssuedAt: time.Now()},
		{UserID: "u-blocked", TokenID: "jti-old", IssuedAt: blockTime.Add(-time.Minute)},
		{UserID: "u-blocked", TokenID: "jti-new", IssuedAt: blockTime.Add(time.Minute)},
		{UserID: "u-disabled", TokenID: "jti-d1", IssuedAt: time.Now()},
		{UserID: "u-disabled", TokenID: "jti-d2", IssuedAt: time.Now()},
		{UserID: "u-deleted", TokenID: "jti-del", IssuedAt: time.Now()},
	}

	tests := []struct {
		name            string
		storeErr        error
		userErr         error
		noStore         bool
		noUsers         bool
		want            []bool
		wantTokenChecks int32
		wantUserFinds   int
	}{
		{name: "all checks", want: []bool{false, true, true, false, true, true, true}, wantTokenChecks: 7, wantUserFinds: 4},
		// A failed check stops the run: the rest passes (fail open) and is
		// not queried. The next run retries.
		{name: "store error stops the run", storeErr: errors.New("db down"), want: []bool{false, false, false, false, false, false, false}, wantTokenChecks: 1, wantUserFinds: 0},
		{name: "user lookup error stops the run", userErr: errors.New("db down"), want: []bool{false, false, false, false, false, false, false}, wantTokenChecks: 1, wantUserFinds: 1},
		{name: "no store", noStore: true, want: []bool{false, false, false, false, true, true, true}, wantTokenChecks: 0, wantUserFinds: 4},
		{name: "no user repository", noUsers: true, want: []bool{false, true, true, false, false, false, false}, wantTokenChecks: 7, wantUserFinds: 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			users := &countingUserRepo{MockUserRepository: NewMockUserRepository(), finds: map[string]int{}}
			require.NoError(t, users.Create(&models.User{ID: "u-active", Username: "active"}))
			require.NoError(t, users.Create(&models.User{ID: "u-blocked", Username: "blocked"}))
			require.NoError(t, users.Create(&models.User{ID: "u-disabled", Username: "disabled", Disabled: true}))
			if tt.userErr != nil {
				users.SetFindError(tt.userErr)
			}
			store := &wsRevocationStore{
				mockSessionStore: newMockHandlerSessionStore(),
				blockedJTI:       "jti-logged-out",
				userBlockedAt:    map[string]time.Time{"u-blocked": blockTime},
				err:              tt.storeErr,
			}

			var check websocket.IdentityChecker
			switch {
			case tt.noStore:
				check = NewWebSocketRevocationChecker(nil, users)
			case tt.noUsers:
				check = NewWebSocketRevocationChecker(store, nil)
			default:
				check = NewWebSocketRevocationChecker(store, users)
			}
			require.NotNil(t, check)

			assert.Equal(t, tt.want, check(context.Background(), identities))
			assert.Equal(t, tt.wantTokenChecks, store.tokenChecks.Load(), "token blocklist queries")
			users.mu.Lock()
			defer users.mu.Unlock()
			total := 0
			for user, n := range users.finds {
				assert.LessOrEqual(t, n, 1, "user %s looked up more than once", user)
				total += n
			}
			assert.Equal(t, tt.wantUserFinds, total, "user lookups")
		})
	}

	assert.Nil(t, NewWebSocketRevocationChecker(nil, nil))
}

// flipStore reports the token as not blocked on the first check and as
// blocked on later checks: a logout that runs during the upgrade.
type flipStore struct {
	*mockSessionStore
	mu    sync.Mutex
	calls int
}

func (s *flipStore) IsTokenBlocked(_ context.Context, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.calls > 1, nil
}

func (s *flipStore) IsUserBlocked(_ context.Context, _ string, _ time.Time) (bool, error) {
	return false, nil
}

// TestHandleWebSocket_RevokedDuringUpgrade checks the second revocation check
// after the registration: the socket closes when the token was revoked
// between the first check and the registration.
func TestHandleWebSocket_RevokedDuringUpgrade(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()
	defer hub.Shutdown()

	handler := NewWebSocketHandler(hub, "*", wsTestJWTSecret).
		WithRevocationChecks(&flipStore{mockSessionStore: newMockHandlerSessionStore()}, nil)
	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)
	server := httptest.NewServer(router)
	defer server.Close()

	token := signWSToken(t, "u1", "jti-1", time.Now())
	conn, resp, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?token="+token, nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	defer conn.Close()

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, _, err = conn.ReadMessage()
	var closeErr *gorilla.CloseError
	require.True(t, errors.As(err, &closeErr), "want a close frame, got %v", err)
	assert.Equal(t, gorilla.ClosePolicyViolation, closeErr.Code)
	waitForHubClients(t, hub, 0)
}

// TestNewWebSocketRevocationChecker_MillisecondIssueTime checks that the
// periodic check keeps the millisecond issue time of a socket (iat_ms): a
// socket opened just after a user block stays open, one opened just before
// it closes (issue #478).
func TestNewWebSocketRevocationChecker_MillisecondIssueTime(t *testing.T) {
	t.Parallel()

	store := sessionstore.NewMemoryStore()
	t.Cleanup(store.Stop)
	before := time.Now()
	require.NoError(t, store.BlockUser(context.Background(), "u1", time.Now().Add(time.Hour)))
	after := time.Now()

	check := NewWebSocketRevocationChecker(store, nil)
	require.NotNil(t, check)
	identities := []websocket.ClientIdentity{
		{UserID: "u1", TokenID: "jti-before", IssuedAt: before.Add(-time.Millisecond)},
		{UserID: "u1", TokenID: "jti-after", IssuedAt: after.Add(time.Millisecond)},
	}
	assert.Equal(t, []bool{true, false}, check(context.Background(), identities))
}
