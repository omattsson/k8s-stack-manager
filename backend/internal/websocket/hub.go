// Package websocket provides WebSocket hub and client infrastructure for
// real-time communication between the backend and connected clients.
package websocket

import (
	"context"
	"log/slog"
	"sync"
)

// broadcastBufferSize is the capacity of the Hub's broadcast channel.
// Messages exceeding this buffer are dropped with a warning log.
const broadcastBufferSize = 256

// ErrHubClosed is returned when attempting to register a client on a shut-down hub.
var ErrHubClosed = errHubClosed{}

type errHubClosed struct{}

func (errHubClosed) Error() string { return "hub is closed" }

// BroadcastSender is implemented by any type that can broadcast messages
// to all connected WebSocket clients. Use this interface for decoupled
// dependency injection (e.g., handlers broadcast events without importing Hub).
type BroadcastSender interface {
	Broadcast(message []byte)
}

// TargetedSender extends BroadcastSender with the ability to send messages
// only to clients subscribed to a specific instance. Used for high-volume
// streaming (e.g. deployment logs) to avoid pushing data to uninterested clients.
type TargetedSender interface {
	BroadcastSender
	BroadcastToInstance(instanceID string, message []byte)
}

// ClientRevoker closes open WebSocket connections after a revocation. Auth
// and user handlers depend on it, not on *Hub. Each method returns the number
// of closed connections. *Hub implements it; a nil *Hub is safe and closes
// nothing.
//
// The hub only knows the connections of this process. With more than one
// replica, the other replicas close their sockets at token expiry.
type ClientRevoker interface {
	// DisconnectUser closes all connections of the user (user deleted,
	// disabled, password reset, logout of all sessions).
	DisconnectUser(userID string) int
	// DisconnectToken closes all connections opened with the access token
	// that has this jti (logout).
	DisconnectToken(tokenID string) int
}

var _ ClientRevoker = (*Hub)(nil)

// Hub manages the set of active WebSocket clients and broadcasts messages
// to all of them. It is safe for concurrent use.
type Hub struct {
	// clients holds the set of registered clients.
	clients map[*Client]bool

	// instanceSubs maps instance IDs to the set of clients watching them.
	// Used by BroadcastToInstance to send deployment logs only to interested clients.
	instanceSubs map[string]map[*Client]bool

	// broadcast receives messages to send to all clients.
	broadcast chan []byte

	// unregister receives clients requesting removal.
	unregister chan *Client

	// mu protects the clients and instanceSubs maps for reads outside the Run loop.
	mu sync.RWMutex

	// done signals the Run loop to stop.
	done chan struct{}

	// shutdownOnce ensures Shutdown is idempotent and safe to call concurrently.
	shutdownOnce sync.Once

	// revalidateOnce makes StartRevalidation start at most one loop.
	revalidateOnce sync.Once
	// revalidateDone is closed when the revalidation loop exits (tests).
	revalidateDone chan struct{}
}

// NewHub creates a new Hub ready to accept clients.
func NewHub() *Hub {
	return &Hub{
		clients:      make(map[*Client]bool),
		instanceSubs: make(map[string]map[*Client]bool),
		broadcast:    make(chan []byte, broadcastBufferSize),
		unregister:   make(chan *Client),
		done:         make(chan struct{}),

		revalidateDone: make(chan struct{}),
	}
}

// Run starts the hub's event loop. It should be launched as a goroutine.
// It processes unregister and broadcast events until Shutdown is called.
func (h *Hub) Run() {
	for {
		select {
		case <-h.done:
			h.closeAllClients()
			return
		case client := <-h.unregister:
			h.mu.Lock()
			h.removeClientLocked(client, "")
			h.mu.Unlock()
			slog.Info("WebSocket client unregistered", "clients", h.ClientCount())
		case message := <-h.broadcast:
			h.mu.RLock()
			var slow []*Client
			var sent int64
			for client := range h.clients {
				select {
				case client.send <- message:
					sent++
				default:
					slow = append(slow, client)
				}
			}
			h.mu.RUnlock()
			if sent > 0 {
				hubMetrics.messagesSentTotal.Add(context.Background(), sent)
			}
			if len(slow) > 0 {
				h.mu.Lock()
				for _, client := range slow {
					h.removeClientLocked(client, "")
				}
				h.mu.Unlock()
			}
		}
	}
}

// Broadcast sends a message to all connected clients.
// It is safe for concurrent use and implements BroadcastSender.
func (h *Hub) Broadcast(message []byte) {
	select {
	case h.broadcast <- message:
	default:
		slog.Warn("WebSocket broadcast channel full, message dropped")
	}
}

// Shutdown gracefully stops the hub's Run loop and closes all client connections.
// It is safe to call multiple times and concurrently.
func (h *Hub) Shutdown() {
	h.shutdownOnce.Do(func() {
		close(h.done)
	})
}

// Register registers a client with the hub. The client is in the client set
// when Register returns, so a Subscribe or a revoke right after it sees the
// client. It returns ErrHubClosed if the hub has been shut down. The done
// check runs under h.mu: closeAllClients also takes h.mu after done is
// closed, so a client is either refused or closed by the shutdown.
func (h *Hub) Register(c *Client) error {
	h.mu.Lock()
	select {
	case <-h.done:
		h.mu.Unlock()
		return ErrHubClosed
	default:
	}
	h.clients[c] = true
	count := len(h.clients)
	h.mu.Unlock()
	hubMetrics.connectionsActive.Add(context.Background(), 1)
	slog.Info("WebSocket client registered", "clients", count)
	return nil
}

// Unregister safely requests client removal from the hub. If the hub has
// already been shut down the call is a no-op, preventing goroutine leaks.
func (h *Hub) Unregister(c *Client) {
	select {
	case h.unregister <- c:
	case <-h.done:
	}
}

// ClientCount returns the current number of connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Subscribe registers a client's interest in messages for a specific instance.
// It takes h.mu. A client that is not registered (never registered, or
// already removed by a revoke, a slow-client drop or an unregister) is
// ignored: its send channel may be closed, and a subscription would make
// BroadcastToInstance send on it.
func (h *Hub) Subscribe(c *Client, instanceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.clients[c] {
		return
	}
	subs, ok := h.instanceSubs[instanceID]
	if !ok {
		subs = make(map[*Client]bool)
		h.instanceSubs[instanceID] = subs
	}
	subs[c] = true
}

// Unsubscribe removes a client's interest in a specific instance.
func (h *Hub) Unsubscribe(c *Client, instanceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if subs, ok := h.instanceSubs[instanceID]; ok {
		delete(subs, c)
		if len(subs) == 0 {
			delete(h.instanceSubs, instanceID)
		}
	}
}

// BroadcastToInstance sends a message only to clients subscribed to the given
// instance. If no clients are subscribed, the message is silently dropped
// (no point broadcasting deployment logs nobody is watching).
func (h *Hub) BroadcastToInstance(instanceID string, message []byte) {
	h.mu.RLock()
	subs := h.instanceSubs[instanceID]
	if len(subs) == 0 {
		h.mu.RUnlock()
		return
	}
	var slow []*Client
	var sent int64
	for client := range subs {
		// Send only to registered clients. A removed client has a closed
		// send channel; a send on it would panic.
		if !h.clients[client] {
			continue
		}
		select {
		case client.send <- message:
			sent++
		default:
			slow = append(slow, client)
		}
	}
	h.mu.RUnlock()
	if sent > 0 {
		hubMetrics.messagesSentTotal.Add(context.Background(), sent)
	}
	if len(slow) > 0 {
		h.mu.Lock()
		for _, client := range slow {
			h.removeClientLocked(client, "")
		}
		h.mu.Unlock()
	}
}

// DisconnectUser closes all connections of the user. It implements
// ClientRevoker. An empty user ID or a nil hub closes nothing.
func (h *Hub) DisconnectUser(userID string) int {
	if h == nil || userID == "" {
		return 0
	}
	return h.disconnectMatching(func(c *Client) bool { return c.identity.UserID == userID })
}

// DisconnectToken closes all connections opened with the access token that
// has this jti. It implements ClientRevoker. An empty token ID or a nil hub
// closes nothing.
func (h *Hub) DisconnectToken(tokenID string) int {
	if h == nil || tokenID == "" {
		return 0
	}
	return h.disconnectMatching(func(c *Client) bool { return c.identity.TokenID == tokenID })
}

// disconnectMatching removes the matching clients under h.mu and closes their
// send channels. The write pump of each client then sends a close frame with
// the "session revoked" reason and closes the connection; the read pump
// unregisters, which is a no-op because the client is already removed.
// h.mu guards the maps for all callers (the Run loop also takes it), so
// this is safe outside the Run loop.
func (h *Hub) disconnectMatching(match func(*Client) bool) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	closed := 0
	for client := range h.clients {
		if match(client) {
			h.removeClientLocked(client, closeReasonRevoked)
			closed++
		}
	}
	return closed
}

// removeClientLocked removes a registered client from all maps and closes its
// send channel. reason is the close frame reason (empty: no reason). It does
// nothing for a client that is not registered, so a send channel is never
// closed twice. Caller must hold h.mu (write lock).
func (h *Hub) removeClientLocked(c *Client, reason string) {
	if _, ok := h.clients[c]; !ok {
		return
	}
	delete(h.clients, c)
	c.closeReason = reason
	close(c.send)
	h.removeClientSubs(c)
	hubMetrics.connectionsActive.Add(context.Background(), -1)
}

// removeClientSubs removes a client from all instance subscriptions.
// Caller must hold h.mu (write lock).
func (h *Hub) removeClientSubs(c *Client) {
	for id, subs := range h.instanceSubs {
		delete(subs, c)
		if len(subs) == 0 {
			delete(h.instanceSubs, id)
		}
	}
}

// closeAllClients removes all clients and closes their send channels.
func (h *Hub) closeAllClients() {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := int64(len(h.clients))
	for client := range h.clients {
		close(client.send)
		delete(h.clients, client)
	}
	h.instanceSubs = make(map[string]map[*Client]bool)
	if count > 0 {
		hubMetrics.connectionsActive.Add(context.Background(), -count)
	}
}

// DisconnectClient closes one client, for example when a revocation check
// after the upgrade finds the token revoked. It reports whether the client
// was registered. A nil hub or client closes nothing.
func (h *Hub) DisconnectClient(c *Client) bool {
	if h == nil || c == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.clients[c] {
		return false
	}
	h.removeClientLocked(c, closeReasonRevoked)
	return true
}
