// Package websocket provides WebSocket hub and client infrastructure for
// real-time communication between the backend and connected clients.
package websocket

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/models"
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

// UserSender sends a message only to the clients of one user (for example
// an in-app notification).
type UserSender interface {
	BroadcastToUser(userID string, message []byte)
}

var (
	_ TargetedSender = (*Hub)(nil)
	_ UserSender     = (*Hub)(nil)
)

// ClientRevoker closes open WebSocket connections after a revocation. Auth
// and user handlers depend on it, not on *Hub. Each method returns the number
// of closed connections. *Hub implements it; a nil *Hub is safe and closes
// nothing.
//
// The hub only knows the connections of this process. With fan-out, the
// revocation also goes to the other replicas through ws_events, and they
// close their sockets within about one poll interval. Without fan-out, the
// other replicas close them at the next revalidation or at token expiry.
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

	// fanout shares the messages of this hub with the hubs of the other
	// replicas. Nil: no fan-out (single replica).
	fanout atomic.Pointer[Fanout]
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
// It is safe for concurrent use and implements BroadcastSender. With fan-out,
// the clients of the other replicas get the message too.
func (h *Hub) Broadcast(message []byte) {
	h.deliverAll(message)
	h.publish(models.WSEventTargetAll, message)
}

// deliverAll queues a message for all local clients.
func (h *Hub) deliverAll(message []byte) {
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
// instance. If no local clients are subscribed, no local client gets the
// message (no point broadcasting deployment logs nobody is watching). With
// fan-out, the subscribed clients of the other replicas get the message too.
func (h *Hub) BroadcastToInstance(instanceID string, message []byte) {
	h.deliverInstance(instanceID, message)
	h.publish(models.WSEventTargetInstancePrefix+instanceID, message)
}

// deliverInstance sends a message to the local clients subscribed to the
// instance.
func (h *Hub) deliverInstance(instanceID string, message []byte) {
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
	h.finishSend(sent, slow)
}

// BroadcastToUser sends a message only to the clients of the user. An empty
// user ID sends nothing. With fan-out, the clients of the user on the other
// replicas get the message too. It implements UserSender.
func (h *Hub) BroadcastToUser(userID string, message []byte) {
	if userID == "" {
		return
	}
	h.deliverUser(userID, message)
	h.publish(models.WSEventTargetUserPrefix+userID, message)
}

// deliverUser sends a message to the local clients of the user.
func (h *Hub) deliverUser(userID string, message []byte) {
	h.mu.RLock()
	var slow []*Client
	var sent int64
	for client := range h.clients {
		if client.identity.UserID != userID {
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
	h.finishSend(sent, slow)
}

// finishSend counts the sent messages and removes the slow clients (full
// send buffer).
func (h *Hub) finishSend(sent int64, slow []*Client) {
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
// ClientRevoker. With fan-out, the other replicas close the sockets of the
// user too. The return value counts the local connections only. An empty
// user ID or a nil hub closes nothing.
func (h *Hub) DisconnectUser(userID string) int {
	if h == nil || userID == "" {
		return 0
	}
	closed := h.disconnectUserLocal(userID)
	h.publish(models.WSEventTargetRevokeUserPrefix+userID, nil)
	return closed
}

// DisconnectToken closes all connections opened with the access token that
// has this jti. It implements ClientRevoker. With fan-out, the other
// replicas close the sockets of the token too. The return value counts the
// local connections only. An empty token ID or a nil hub closes nothing.
func (h *Hub) DisconnectToken(tokenID string) int {
	if h == nil || tokenID == "" {
		return 0
	}
	closed := h.disconnectTokenLocal(tokenID)
	h.publish(models.WSEventTargetRevokeTokenPrefix+tokenID, nil)
	return closed
}

// disconnectUserLocal closes the local connections of the user without a
// fan-out row.
func (h *Hub) disconnectUserLocal(userID string) int {
	return h.disconnectMatching(func(c *Client) bool { return c.identity.UserID == userID })
}

// disconnectUserIssuedBefore closes the local connections of the user whose
// token was issued at or before t (millisecond precision, the same rule as
// the session store user block; an unknown issue time counts as before). A
// token without the iat_ms claim has a whole-second issue time, so for it the
// rule is "issued in the second of t or before". The
// fan-out poller calls it for a user revocation from another replica, with
// the row time: a session that the user opened after the revocation stays
// open.
func (h *Hub) disconnectUserIssuedBefore(userID string, t time.Time) int {
	return h.disconnectMatching(func(c *Client) bool {
		if c.identity.UserID != userID {
			return false
		}
		return c.identity.IssuedAt.IsZero() || t.IsZero() || c.identity.IssuedAt.UnixMilli() <= t.UnixMilli()
	})
}

// disconnectTokenLocal closes the local connections of the token. See
// disconnectUserLocal.
func (h *Hub) disconnectTokenLocal(tokenID string) int {
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
