package websocket

import (
	"errors"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isClosed reports whether ch is closed. It does not block. A test that calls
// it must not send on ch.
func isClosed(ch chan []byte) bool {
	select {
	case _, ok := <-ch:
		return !ok
	default:
		return false
	}
}

func TestHub_Disconnect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		disconnect func(h *Hub) int
		wantClosed []int // indexes into the clients below
	}{
		{name: "user", disconnect: func(h *Hub) int { return h.DisconnectUser("u1") }, wantClosed: []int{0, 1}},
		{name: "token", disconnect: func(h *Hub) int { return h.DisconnectToken("jti-b") }, wantClosed: []int{1}},
		{name: "unknown user", disconnect: func(h *Hub) int { return h.DisconnectUser("nobody") }},
		{name: "empty user ID", disconnect: func(h *Hub) int { return h.DisconnectUser("") }},
		{name: "empty token ID", disconnect: func(h *Hub) int { return h.DisconnectToken("") }},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hub := NewHub()
			go hub.Run()
			defer hub.Shutdown()

			identities := []ClientIdentity{
				{UserID: "u1", TokenID: "jti-a"},
				{UserID: "u1", TokenID: "jti-b"},
				{UserID: "u2", TokenID: "jti-c"},
				{}, // socket without identity
			}
			clients := make([]*Client, len(identities))
			for i, id := range identities {
				clients[i] = &Client{hub: hub, identity: id, send: make(chan []byte, 1)}
				require.NoError(t, hub.Register(clients[i]))
				hub.Subscribe(clients[i], "inst-1")
			}
			waitForClientCount(t, hub, len(clients))

			closed := tt.disconnect(hub)

			assert.Equal(t, len(tt.wantClosed), closed)
			waitForClientCount(t, hub, len(clients)-len(tt.wantClosed))
			want := map[int]bool{}
			for _, i := range tt.wantClosed {
				want[i] = true
			}
			for i, c := range clients {
				assert.Equal(t, want[i], isClosed(c.send), "client %d send closed", i)
				if want[i] {
					assert.Equal(t, closeReasonRevoked, c.closeReason)
				}
			}

			// The revoked clients no longer get instance messages. A send on a
			// closed channel would panic here.
			hub.BroadcastToInstance("inst-1", []byte("log"))
			hub.mu.RLock()
			assert.Len(t, hub.instanceSubs["inst-1"], len(clients)-len(tt.wantClosed))
			hub.mu.RUnlock()
		})
	}
}

func TestHub_DisconnectNilHub(t *testing.T) {
	t.Parallel()

	var hub *Hub
	var revoker ClientRevoker = hub
	assert.Equal(t, 0, revoker.DisconnectUser("u1"))
	assert.Equal(t, 0, revoker.DisconnectToken("jti"))
}

// TestHub_BroadcastDropsSlowClientSubscriptions checks that a slow client
// dropped by Broadcast also leaves its instance subscriptions, so a later
// BroadcastToInstance does not send on its closed channel.
func TestHub_BroadcastDropsSlowClientSubscriptions(t *testing.T) {
	t.Parallel()

	hub := NewHub()
	go hub.Run()
	defer hub.Shutdown()

	slow := &Client{hub: hub, send: make(chan []byte, 1)}
	require.NoError(t, hub.Register(slow))
	hub.Subscribe(slow, "inst-1")
	waitForClientCount(t, hub, 1)

	slow.send <- []byte("fill")
	hub.Broadcast([]byte("overflow"))
	waitForClientCount(t, hub, 0)

	assert.NotPanics(t, func() { hub.BroadcastToInstance("inst-1", []byte("log")) })
}

// readCloseError reads from peer until it gets a close frame and returns it.
func readCloseError(t *testing.T, peer *gorilla.Conn) *gorilla.CloseError {
	t.Helper()
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(3*time.Second)))
	for {
		_, _, err := peer.ReadMessage()
		if err == nil {
			continue
		}
		var closeErr *gorilla.CloseError
		require.True(t, errors.As(err, &closeErr), "want a close frame, got %v", err)
		return closeErr
	}
}

func TestClient_CloseFrames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		identity   func() ClientIdentity
		revoke     func(h *Hub)
		wantReason string
	}{
		{
			name: "token expiry",
			identity: func() ClientIdentity {
				return ClientIdentity{UserID: "u1", TokenID: "jti-1", ExpiresAt: time.Now().Add(150 * time.Millisecond)}
			},
			wantReason: closeReasonExpired,
		},
		{
			name:       "token already expired",
			identity:   func() ClientIdentity { return ClientIdentity{UserID: "u1", ExpiresAt: time.Now().Add(-time.Second)} },
			wantReason: closeReasonExpired,
		},
		{
			name: "user revoked",
			identity: func() ClientIdentity {
				return ClientIdentity{UserID: "u1", TokenID: "jti-1", ExpiresAt: time.Now().Add(time.Hour)}
			},
			revoke:     func(h *Hub) { h.DisconnectUser("u1") },
			wantReason: closeReasonRevoked,
		},
		{
			name:       "token revoked",
			identity:   func() ClientIdentity { return ClientIdentity{UserID: "u1", TokenID: "jti-1"} },
			revoke:     func(h *Hub) { h.DisconnectToken("jti-1") },
			wantReason: closeReasonRevoked,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hub := NewHub()
			go hub.Run()
			defer hub.Shutdown()

			srvConn, peerConn := newTestWSPair(t)
			_, err := NewClientWithIdentity(hub, srvConn, tt.identity())
			require.NoError(t, err)

			if tt.revoke != nil {
				waitForClientCount(t, hub, 1)
				tt.revoke(hub)
			}

			closeErr := readCloseError(t, peerConn)
			assert.Equal(t, gorilla.ClosePolicyViolation, closeErr.Code)
			assert.Equal(t, tt.wantReason, closeErr.Text)
			waitForClientCount(t, hub, 0)
		})
	}
}

func TestClient_NoExpiryWithoutIdentity(t *testing.T) {
	t.Parallel()

	hub := NewHub()
	go hub.Run()
	defer hub.Shutdown()

	srvConn, peerConn := newTestWSPair(t)
	_, err := NewClient(hub, srvConn)
	require.NoError(t, err)
	waitForClientCount(t, hub, 1)

	// A broadcast still arrives: a zero expiry does not close the socket.
	time.Sleep(50 * time.Millisecond)
	hub.Broadcast([]byte(`{"type":"ping"}`))
	require.NoError(t, peerConn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, msg, err := peerConn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, `{"type":"ping"}`, string(msg))
}
