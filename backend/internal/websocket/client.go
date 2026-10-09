package websocket

import (
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
)

// errChanClosed is a sentinel used internally when the hub closes a client's send channel.
var errChanClosed = errors.New("send channel closed")

const (
	// writeWait is the time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// pongWait is the time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// pingPeriod sends pings at this interval. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// maxMessageSize is the maximum message size allowed from peer.
	maxMessageSize = 512

	// sendBufferSize is the buffer size for the client send channel.
	sendBufferSize = 256
)

// Close frame values for sockets that the server closes on purpose.
const (
	// closeReasonRevoked is the close reason when a logout or a user
	// revocation closes the socket (see Hub.DisconnectUser, DisconnectToken).
	closeReasonRevoked = "session revoked"
	// closeReasonExpired is the close reason when the access token that
	// opened the socket expires. The client reconnects with a fresh token.
	closeReasonExpired = "token expired"
)

// ClientIdentity identifies the access token that opened a socket. The hub
// uses it to close the sockets of a revoked user or token. The client uses
// ExpiresAt to close the socket when the token expires.
type ClientIdentity struct {
	ExpiresAt time.Time // zero: the socket does not expire
	IssuedAt  time.Time // iat_ms claim (ms) or iat (whole seconds); the user blocklist compares it with the block time
	UserID    string
	TokenID   string // jti claim
}

// Client is a middleman between the WebSocket connection and the hub.
type Client struct {
	identity ClientIdentity
	hub      *Hub
	conn     *websocket.Conn
	send     chan []byte
	// closeReason is the reason of the close frame that writePump sends when
	// the hub closes send. The hub sets it under h.mu before it closes send.
	// writePump reads it only after the receive on the closed channel, so
	// the channel close orders the two accesses. Empty: no reason.
	closeReason string
}

// NewClient creates a new Client attached to the given hub and connection,
// registers it with the hub, and starts the read/write pumps.
// The caller should not interact with conn after calling NewClient.
// Returns an error if the hub has already been shut down.
func NewClient(hub *Hub, conn *websocket.Conn) (*Client, error) {
	return NewClientWithIdentity(hub, conn, ClientIdentity{})
}

// NewClientWithIdentity is NewClient for a socket opened with an access
// token. The hub can close the socket by user ID or token ID, and the socket
// closes when the token expires.
func NewClientWithIdentity(hub *Hub, conn *websocket.Conn, identity ClientIdentity) (*Client, error) {
	client := &Client{
		identity: identity,
		hub:      hub,
		conn:     conn,
		send:     make(chan []byte, sendBufferSize),
	}
	if err := hub.Register(client); err != nil {
		conn.Close() //nolint:gosec // G104: close errors during cleanup are non-critical
		return nil, err
	}
	go client.writePump()
	go client.readPump()
	return client, nil
}

// readPump pumps messages from the WebSocket connection to the hub.
// It runs in its own goroutine. When the connection is closed (or an
// error occurs), the client unregisters from the hub.
func (c *Client) readPump() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in WebSocket readPump", "recover", r)
		}
		c.hub.Unregister(c)
		c.conn.Close() //nolint:gosec // G104: close errors during cleanup are non-critical
	}()

	c.conn.SetReadLimit(maxMessageSize)
	if err := c.conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		slog.Error("Failed to set read deadline", "error", err)
		return
	}
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Warn("WebSocket unexpected close", "error", err)
			}
			return
		}
		c.handleInbound(data)
	}
}

// writePump pumps messages from the hub to the WebSocket connection.
// It runs in its own goroutine. A ticker sends periodic pings to detect
// dead connections.
//
// When the client has a token expiry, writePump sends a close frame and
// returns at that time. The client then reconnects with a fresh token.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	// A nil channel blocks forever: no expiry timer for a zero ExpiresAt.
	var expired <-chan time.Time
	if !c.identity.ExpiresAt.IsZero() {
		timer := time.NewTimer(time.Until(c.identity.ExpiresAt))
		defer timer.Stop()
		expired = timer.C
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in WebSocket writePump", "recover", r)
		}
		ticker.Stop()
		c.conn.Close() //nolint:gosec // G104: close errors during cleanup are non-critical
	}()

	for {
		select {
		case <-expired:
			c.writeClose(closeReasonExpired)
			return
		case message, ok := <-c.send:
			if err := c.handleSend(message, ok); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.writePing(); err != nil {
				return
			}
		}
	}
}

// handleSend processes a message (or channel close) from the hub.
func (c *Client) handleSend(message []byte, ok bool) error {
	if !ok {
		// Hub closed the channel — send a close frame.
		c.writeClose(c.closeReason)
		return errChanClosed
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		slog.Error("Failed to set write deadline", "error", err)
		return err
	}

	if err := c.writeMessage(message); err != nil {
		return err
	}

	// Drain queued messages, sending each as its own WS frame
	// so every frame contains a single valid JSON object.
	n := len(c.send)
	for i := 0; i < n; i++ {
		if err := c.writeMessage(<-c.send); err != nil {
			return err
		}
	}
	return nil
}

// writeClose sends a close frame. An empty reason sends an empty frame (hub
// shutdown, slow client). A reason sends code 1008 (policy violation) with the
// reason: the server closed the socket on purpose (revocation, token expiry).
// Errors are ignored: the caller closes the connection next.
func (c *Client) writeClose(reason string) {
	data := []byte{}
	if reason != "" {
		data = websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason)
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	_ = c.conn.WriteMessage(websocket.CloseMessage, data)
}

// writePing sends a ping frame with the configured write deadline.
func (c *Client) writePing() error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		slog.Error("Failed to set write deadline for ping", "error", err)
		return err
	}
	return c.conn.WriteMessage(websocket.PingMessage, nil)
}

// writeMessage sends a single text message as one WebSocket frame.
func (c *Client) writeMessage(data []byte) error {
	w, err := c.conn.NextWriter(websocket.TextMessage)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	return w.Close()
}

// inboundMsg is the minimal envelope parsed from client-sent JSON.
type inboundMsg struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type subscribePayload struct {
	InstanceID string `json:"instance_id"`
}

// handleInbound routes inbound client messages. Currently supports:
//   - subscribe: register interest in an instance's deployment logs
//   - unsubscribe: remove interest
func (c *Client) handleInbound(data []byte) {
	var msg inboundMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	switch msg.Type {
	case "subscribe":
		var p subscribePayload
		if json.Unmarshal(msg.Payload, &p) == nil && p.InstanceID != "" {
			c.hub.Subscribe(c, p.InstanceID)
		}
	case "unsubscribe":
		var p subscribePayload
		if json.Unmarshal(msg.Payload, &p) == nil && p.InstanceID != "" {
			c.hub.Unsubscribe(c, p.InstanceID)
		}
	}
}
