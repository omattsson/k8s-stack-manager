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

	// register receives clients requesting registration.
	register chan *Client

	// unregister receives clients requesting removal.
	unregister chan *Client

	// mu protects the clients and instanceSubs maps for reads outside the Run loop.
	mu sync.RWMutex

	// done signals the Run loop to stop.
	done chan struct{}

	// shutdownOnce ensures Shutdown is idempotent and safe to call concurrently.
	shutdownOnce sync.Once
}

// NewHub creates a new Hub ready to accept clients.
func NewHub() *Hub {
	return &Hub{
		clients:      make(map[*Client]bool),
		instanceSubs: make(map[string]map[*Client]bool),
		broadcast:    make(chan []byte, broadcastBufferSize),
		register:     make(chan *Client),
		unregister:   make(chan *Client),
		done:         make(chan struct{}),
	}
}

// Run starts the hub's event loop. It should be launched as a goroutine.
// It processes register, unregister, and broadcast events until Shutdown is called.
func (h *Hub) Run() {
	for {
		select {
		case <-h.done:
			h.closeAllClients()
			return
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			hubMetrics.connectionsActive.Add(context.Background(), 1)
			slog.Info("WebSocket client registered", "clients", h.ClientCount())
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				h.removeClientSubs(client)
				hubMetrics.connectionsActive.Add(context.Background(), -1)
			}
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
					if _, ok := h.clients[client]; ok {
						delete(h.clients, client)
						close(client.send)
						hubMetrics.connectionsActive.Add(context.Background(), -1)
					}
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

// Register safely registers a client with the hub. It returns ErrHubClosed if
// the hub has been shut down, preventing the caller from blocking forever.
func (h *Hub) Register(c *Client) error {
	select {
	case h.register <- c:
		return nil
	case <-h.done:
		return ErrHubClosed
	}
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
// Must be called with h.mu held (or from within the Run loop).
func (h *Hub) Subscribe(c *Client, instanceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
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
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				h.removeClientSubs(client)
				hubMetrics.connectionsActive.Add(context.Background(), -1)
			}
		}
		h.mu.Unlock()
	}
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
