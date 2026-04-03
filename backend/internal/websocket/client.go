package websocket

import (
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

// Client is a middleman between the WebSocket connection and the hub.
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// NewClient creates a new Client attached to the given hub and connection,
// registers it with the hub, and starts the read/write pumps.
// The caller should not interact with conn after calling NewClient.
// Returns an error if the hub has already been shut down.
func NewClient(hub *Hub, conn *websocket.Conn) (*Client, error) {
	client := &Client{
		hub:  hub,
		conn: conn,
		send: make(chan []byte, sendBufferSize),
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
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Warn("WebSocket unexpected close", "error", err)
			}
			return
		}
		// Inbound messages from clients are currently ignored.
		// Future: route client messages through the hub or a handler.
	}
}

// writePump pumps messages from the hub to the WebSocket connection.
// It runs in its own goroutine. A ticker sends periodic pings to detect
// dead connections.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in WebSocket writePump", "recover", r)
		}
		ticker.Stop()
		c.conn.Close() //nolint:gosec // G104: close errors during cleanup are non-critical
	}()

	for {
		select {
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
		_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
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
