package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/api/middleware"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const wsTestJWTSecret = "test-secret-key-for-websocket-tests"

// generateTestToken creates a valid JWT token for testing.
func generateTestToken(t *testing.T) string {
	t.Helper()
	token, err := middleware.GenerateToken("1", "testuser", "developer", wsTestJWTSecret, time.Hour)
	require.NoError(t, err)
	return token
}

func TestCheckOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		allowedOrigins string
		origin         string
		want           bool
	}{
		{
			name:           "wildcard allows any origin",
			allowedOrigins: "*",
			origin:         "http://evil.com",
			want:           true,
		},
		{
			name:           "empty string allows any origin",
			allowedOrigins: "",
			origin:         "http://evil.com",
			want:           true,
		},
		{
			name:           "specific origin allows matching request",
			allowedOrigins: "http://example.com",
			origin:         "http://example.com",
			want:           true,
		},
		{
			name:           "specific origin rejects non-matching request",
			allowedOrigins: "http://example.com",
			origin:         "http://evil.com",
			want:           false,
		},
		{
			name:           "multiple comma-separated origins allow first match",
			allowedOrigins: "http://example.com,http://other.com",
			origin:         "http://example.com",
			want:           true,
		},
		{
			name:           "multiple comma-separated origins allow second match",
			allowedOrigins: "http://example.com,http://other.com",
			origin:         "http://other.com",
			want:           true,
		},
		{
			name:           "multiple origins reject non-matching request",
			allowedOrigins: "http://example.com,http://other.com",
			origin:         "http://evil.com",
			want:           false,
		},
		{
			name:           "comma-separated with spaces trims correctly",
			allowedOrigins: "http://example.com, http://other.com",
			origin:         "http://other.com",
			want:           true,
		},
		{
			name:           "no origin header allows request (same-origin)",
			allowedOrigins: "http://example.com",
			origin:         "",
			want:           true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewWebSocketHandler(nil, tt.allowedOrigins, wsTestJWTSecret)
			req, err := http.NewRequest("GET", "/ws", nil)
			require.NoError(t, err)

			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}

			got := handler.checkOrigin(req)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewWebSocketHandler(t *testing.T) {
	t.Parallel()

	hub := websocket.NewHub()
	handler := NewWebSocketHandler(hub, "http://example.com", wsTestJWTSecret)

	assert.NotNil(t, handler)
	assert.Equal(t, "http://example.com", handler.allowedOrigins)
}

// waitForHubClients polls hub.ClientCount until it equals want or timeout.
func waitForHubClients(t *testing.T, hub *websocket.Hub, want int) {
	t.Helper()
	assert.Eventually(t, func() bool {
		return hub.ClientCount() == want
	}, 2*time.Second, 10*time.Millisecond, "expected %d clients", want)
}

func TestHandleWebSocket_NoAuth(t *testing.T) {
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

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	_, resp, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	assert.Error(t, err, "dial without token should fail")
	if resp != nil {
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}
}

func TestHandleWebSocket_InvalidToken(t *testing.T) {
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

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=invalid-jwt-token"

	_, resp, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	assert.Error(t, err, "dial with invalid token should fail")
	if resp != nil {
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}
}

func TestHandleWebSocket_ExpiredToken(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()
	defer hub.Shutdown()

	// Generate an already-expired token
	expiredToken, err := middleware.GenerateToken("1", "testuser", "developer", wsTestJWTSecret, -time.Hour)
	require.NoError(t, err)

	handler := NewWebSocketHandler(hub, "*", wsTestJWTSecret)

	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + expiredToken

	_, resp, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	assert.Error(t, err, "dial with expired token should fail")
	if resp != nil {
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}
}

func TestHandleWebSocket_WrongSecret(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()
	defer hub.Shutdown()

	// Generate token with a different secret
	wrongToken, err := middleware.GenerateToken("1", "testuser", "developer", "wrong-secret", time.Hour)
	require.NoError(t, err)

	handler := NewWebSocketHandler(hub, "*", wsTestJWTSecret)

	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + wrongToken

	_, resp, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	assert.Error(t, err, "dial with wrong-secret token should fail")
	if resp != nil {
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}
}

func TestHandleWebSocket_ValidTokenQueryParam(t *testing.T) {
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

	token := generateTestToken(t)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + token

	conn, resp, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	waitForHubClients(t, hub, 1)
}

func TestHandleWebSocket_ValidTokenBearerHeader(t *testing.T) {
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

	token := generateTestToken(t)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)

	conn, resp, err := gorilla.DefaultDialer.Dial(wsURL, header)
	require.NoError(t, err)
	defer conn.Close()

	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	waitForHubClients(t, hub, 1)
}

func TestHandleWebSocket_SuccessfulUpgrade(t *testing.T) {
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

	token := generateTestToken(t)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + token

	conn, resp, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	waitForHubClients(t, hub, 1)
}

func TestHandleWebSocket_BroadcastReceived(t *testing.T) {
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

	token := generateTestToken(t)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + token

	conn, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	waitForHubClients(t, hub, 1)

	// Broadcast a message through the hub
	msg := []byte(`{"type":"test","payload":"hello"}`)
	hub.Broadcast(msg)

	// Read the message from the WebSocket connection
	err = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	require.NoError(t, err)

	_, received, err := conn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, msg, received)
}

func TestHandleWebSocket_MultipleClients(t *testing.T) {
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

	token := generateTestToken(t)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + token

	conn1, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn1.Close()

	conn2, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn2.Close()

	waitForHubClients(t, hub, 2)

	msg := []byte(`{"type":"broadcast","payload":"all"}`)
	hub.Broadcast(msg)

	for i, conn := range []*gorilla.Conn{conn1, conn2} {
		err = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		require.NoError(t, err, "client %d set deadline", i+1)

		_, received, err := conn.ReadMessage()
		require.NoError(t, err, "client %d read", i+1)
		assert.Equal(t, msg, received, "client %d message", i+1)
	}
}

func TestHandleWebSocket_HubShutdown(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()

	handler := NewWebSocketHandler(hub, "*", wsTestJWTSecret)

	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)

	server := httptest.NewServer(router)
	defer server.Close()

	token := generateTestToken(t)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + token

	// Connect a client first, then shut down the hub
	conn, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	waitForHubClients(t, hub, 1)

	hub.Shutdown()
	waitForHubClients(t, hub, 0)

	// The connected client should receive a close frame or read error
	err = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	require.NoError(t, err)

	_, _, err = conn.ReadMessage()
	assert.Error(t, err, "read after hub shutdown should fail")
}

func TestHandleWebSocket_HubClosedBeforeUpgrade(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()
	hub.Shutdown()

	handler := NewWebSocketHandler(hub, "*", wsTestJWTSecret)

	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)

	server := httptest.NewServer(router)
	defer server.Close()

	token := generateTestToken(t)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + token

	// The upgrade itself succeeds (HTTP → WS), but NewClient fails
	// because the hub is closed. The server closes the connection.
	conn, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		// Connection refused or failed — acceptable when hub is closed
		return
	}
	defer conn.Close()

	// If dial succeeded, the connection should be immediately closed by server
	err = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	require.NoError(t, err)

	_, _, err = conn.ReadMessage()
	assert.Error(t, err, "connection should be closed when hub is shut down")
}

func TestHandleWebSocket_OriginRejected(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()
	defer hub.Shutdown()

	handler := NewWebSocketHandler(hub, "http://allowed.com", wsTestJWTSecret)

	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)

	server := httptest.NewServer(router)
	defer server.Close()

	token := generateTestToken(t)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + token

	// Dial with a disallowed origin
	header := http.Header{}
	header.Set("Origin", "http://evil.com")

	_, resp, err := gorilla.DefaultDialer.Dial(wsURL, header)
	assert.Error(t, err, "dial with rejected origin should fail")
	if resp != nil {
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	}
}

func TestHandleWebSocket_OriginAllowed(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub()
	go hub.Run()
	defer hub.Shutdown()

	handler := NewWebSocketHandler(hub, "http://allowed.com", wsTestJWTSecret)

	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)

	server := httptest.NewServer(router)
	defer server.Close()

	token := generateTestToken(t)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + token

	header := http.Header{}
	header.Set("Origin", "http://allowed.com")

	conn, resp, err := gorilla.DefaultDialer.Dial(wsURL, header)
	require.NoError(t, err)
	defer conn.Close()

	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	waitForHubClients(t, hub, 1)
}

func TestHandleWebSocket_ClientDisconnect(t *testing.T) {
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

	token := generateTestToken(t)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?token=" + token

	conn, _, err := gorilla.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)

	waitForHubClients(t, hub, 1)

	// Client closes the connection
	conn.Close()

	// Hub should eventually unregister the client
	waitForHubClients(t, hub, 0)
}

// ---------- Sec-WebSocket-Protocol bearer auth (#245) ----------

// TestHandleWebSocket_SubprotocolBearer_Valid covers the documented
// `Sec-WebSocket-Protocol: bearer, <jwt>` auth path: the upgrade
// succeeds, the server acknowledges "bearer" in the response, and the
// hub registers the client.
func TestHandleWebSocket_SubprotocolBearer_Valid(t *testing.T) {
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

	token := generateTestToken(t)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	dialer := *gorilla.DefaultDialer
	dialer.Subprotocols = []string{"bearer", token}
	conn, resp, err := dialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	assert.Equal(t, "bearer", conn.Subprotocol(),
		"server must echo bearer as the selected subprotocol — strict browser "+
			"clients reject the upgrade otherwise")
	waitForHubClients(t, hub, 1)
}

// TestHandleWebSocket_SubprotocolBearer_InvalidToken locks the rejection
// path: a malformed/expired JWT in the subprotocol position fails with
// 401 and never reaches the hub.
func TestHandleWebSocket_SubprotocolBearer_InvalidToken(t *testing.T) {
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

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	dialer := *gorilla.DefaultDialer
	dialer.Subprotocols = []string{"bearer", "not-a-real-jwt"}
	_, resp, err := dialer.Dial(wsURL, nil)
	require.Error(t, err, "invalid token must fail the upgrade")
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestHandleWebSocket_SubprotocolBearer_MissingToken covers the case
// where the client sends just `bearer` with no second subprotocol —
// extractSubprotocolToken correctly flags this as "bearer was offered
// but token missing" so the handler 401s instead of silently falling
// through to other auth methods (which would also fail, but with a
// less informative reason).
func TestHandleWebSocket_SubprotocolBearer_MissingToken(t *testing.T) {
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

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	dialer := *gorilla.DefaultDialer
	dialer.Subprotocols = []string{"bearer"}
	_, resp, err := dialer.Dial(wsURL, nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestExtractSubprotocolToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		header    string
		wantTok   string
		wantViaSp bool
	}{
		{"empty header", "", "", false},
		{"only bearer marker", "bearer", "", true},
		{"bearer + token", "bearer, jwt-here", "jwt-here", true},
		{"bearer + token without space", "bearer,jwt-here", "jwt-here", true},
		{"token + bearer (reversed order)", "jwt-here, bearer", "jwt-here", true},
		{"case-insensitive marker", "BEARER, jwt", "jwt", true},
		{"no bearer marker", "graphql-ws, jwt", "", false},
		{"bearer with extra subprotocols", "bearer, jwt, mqtt", "jwt", true},
		{"multiple bearer markers", "bearer, bearer", "", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotTok, gotViaSp := extractSubprotocolToken(tc.header)
			assert.Equal(t, tc.wantTok, gotTok)
			assert.Equal(t, tc.wantViaSp, gotViaSp)
		})
	}
}

// TestHandleWebSocket_SubprotocolBearer_DoesNotLeakInQuery is a
// belt-and-braces check: subprotocol auth never touches the URL, so
// the query string remains empty regardless of redact middleware
// state.
func TestHandleWebSocket_SubprotocolBearer_DoesNotLeakInQuery(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var capturedURL string
	hub := websocket.NewHub()
	go hub.Run()
	defer hub.Shutdown()

	handler := NewWebSocketHandler(hub, "*", wsTestJWTSecret)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		capturedURL = c.Request.URL.String()
		c.Next()
	})
	router.GET("/ws", handler.HandleWebSocket)
	server := httptest.NewServer(router)
	defer server.Close()

	token := generateTestToken(t)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	dialer := *gorilla.DefaultDialer
	dialer.Subprotocols = []string{"bearer", token}
	conn, _, err := dialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	assert.NotContains(t, capturedURL, token, "subprotocol auth must never expose the token in the URL")
	assert.NotContains(t, capturedURL, "token=", "subprotocol auth must never set a token query param")
}
