package notifier

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/websocket"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dialUser opens a real WebSocket connection to a test server that registers
// the socket on hub with the user ID from the "user" query parameter.
func dialUser(t *testing.T, srv *httptest.Server, hub *websocket.Hub, userID string) *gorilla.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/?user=" + userID
	conn, resp, err := gorilla.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readMessage reads one text message or returns "" after the timeout.
func readMessage(conn *gorilla.Conn, timeout time.Duration) string {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := conn.ReadMessage()
	if err != nil {
		return ""
	}
	return string(data)
}

// TestNotify_WebSocketOnlyToNotifiedUser checks that notification.new goes
// only to the sockets of the notified user. It fails when the notifier sends
// with Broadcast (all sockets).
func TestNotify_WebSocketOnlyToNotifiedUser(t *testing.T) {
	t.Parallel()

	hub := websocket.NewHub()
	go hub.Run()
	t.Cleanup(hub.Shutdown)

	upgrader := gorilla.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		identity := websocket.ClientIdentity{UserID: r.URL.Query().Get("user")}
		if _, err := websocket.NewClientWithIdentity(hub, conn, identity); err != nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(srv.Close)

	notified := dialUser(t, srv, hub, "user-1")
	other := dialUser(t, srv, hub, "user-2")
	require.Eventually(t, func() bool { return hub.ClientCount() == 2 }, 2*time.Second, 5*time.Millisecond)

	n := NewNotifier(newMockNotificationRepo(), hub, nil)
	require.NoError(t, n.Notify(context.Background(), "user-1", "deployment", "Deploy Complete", "done", "stack_instance", "inst-1"))

	got := readMessage(notified, 2*time.Second)
	assert.Contains(t, got, `"type":"notification.new"`)
	assert.Contains(t, got, `"user_id":"user-1"`)
	assert.Empty(t, readMessage(other, 300*time.Millisecond), "another user must not get the notification")
}
