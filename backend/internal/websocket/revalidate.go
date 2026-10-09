package websocket

import (
	"context"
	"log/slog"
	"time"
)

// DefaultRevalidateInterval is the time between two revocation checks of the
// open sockets. It bounds the time a revoked socket stays open on a replica
// that did not handle the revoke request.
const DefaultRevalidateInterval = time.Minute

// revalidateTimeout bounds one revocation check, so a slow database cannot
// pile up work.
const revalidateTimeout = 30 * time.Second

// IdentityChecker reports which socket identities are revoked. The result
// has one entry per identity: true closes the sockets of that identity. On a
// lookup error the checker must return false for the affected identities
// (fail open) and log the error.
type IdentityChecker func(ctx context.Context, identities []ClientIdentity) []bool

// identityKey is the comparable form of a ClientIdentity.
type identityKey struct {
	userID   string
	tokenID  string
	issuedAt int64 // Unix nanoseconds
}

func keyOf(id ClientIdentity) identityKey {
	var iat int64
	if !id.IssuedAt.IsZero() {
		iat = id.IssuedAt.UnixNano()
	}
	return identityKey{userID: id.UserID, tokenID: id.TokenID, issuedAt: iat}
}

// StartRevalidation starts a goroutine that runs check every interval on the
// distinct identities of the open sockets and closes the revoked ones. This
// closes sockets that a revoke on another replica, a direct database change or
// a token blocklist entry made invalid. The goroutine stops when the hub shuts
// down. Only the first call starts a loop. A nil hub, a nil check or an
// interval <= 0 starts nothing.
func (h *Hub) StartRevalidation(interval time.Duration, check IdentityChecker) {
	if h == nil || check == nil || interval <= 0 {
		return
	}
	h.revalidateOnce.Do(func() {
		go h.revalidateLoop(interval, check)
	})
}

func (h *Hub) revalidateLoop(interval time.Duration, check IdentityChecker) {
	defer close(h.revalidateDone)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
			h.revalidate(check)
		}
	}
}

// revalidate runs one check. A panic in check is logged and does not stop the
// loop. It returns the number of closed sockets.
func (h *Hub) revalidate(check IdentityChecker) (closed int) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in WebSocket revalidation", "recover", r)
		}
	}()

	h.mu.RLock()
	seen := make(map[identityKey]bool, len(h.clients))
	identities := make([]ClientIdentity, 0, len(h.clients))
	for c := range h.clients {
		id := c.identity
		if id.UserID == "" && id.TokenID == "" {
			continue // socket without an access token identity (tests)
		}
		k := keyOf(id)
		if seen[k] {
			continue
		}
		seen[k] = true
		identities = append(identities, id)
	}
	h.mu.RUnlock()
	if len(identities) == 0 {
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), revalidateTimeout)
	defer cancel()
	// Stop the check early on shutdown.
	go func() {
		select {
		case <-h.done:
			cancel()
		case <-ctx.Done():
		}
	}()

	revoked := check(ctx, identities)
	if len(revoked) != len(identities) {
		slog.Error("WebSocket revalidation: checker returned a wrong result size",
			"identities", len(identities), "results", len(revoked))
		return 0
	}
	revokedKeys := make(map[identityKey]bool)
	for i, r := range revoked {
		if r {
			revokedKeys[keyOf(identities[i])] = true
		}
	}
	if len(revokedKeys) == 0 {
		return 0
	}
	closed = h.disconnectMatching(func(c *Client) bool { return revokedKeys[keyOf(c.identity)] })
	if closed > 0 {
		slog.Info("Closed revoked WebSocket connections", "connections", closed)
	}
	return closed
}
