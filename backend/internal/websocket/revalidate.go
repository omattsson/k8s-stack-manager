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
	apiKeyID string
	issuedAt int64 // Unix nanoseconds
}

func keyOf(id ClientIdentity) identityKey {
	var iat int64
	if !id.IssuedAt.IsZero() {
		iat = id.IssuedAt.UnixNano()
	}
	return identityKey{userID: id.UserID, tokenID: id.TokenID, apiKeyID: id.APIKeyID, issuedAt: iat}
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
	seen := make(map[identityKey]bool, len(h.clients)+len(h.revocables))
	identities := make([]ClientIdentity, 0, len(h.clients)+len(h.revocables))
	add := func(id ClientIdentity) {
		if id.UserID == "" && id.TokenID == "" && id.APIKeyID == "" {
			return // connection without a credential identity (tests)
		}
		k := keyOf(id)
		if seen[k] {
			return
		}
		seen[k] = true
		identities = append(identities, id)
	}
	for c := range h.clients {
		add(c.identity)
	}
	for r := range h.revocables {
		add(r.Identity())
	}
	h.mu.RUnlock()

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

	if len(identities) > 0 {
		closed += h.revalidateIdentities(ctx, check, identities)
	}
	closed += h.revalidateAccess(ctx)
	if closed > 0 {
		slog.Info("Closed revoked WebSocket connections", "connections", closed)
	}
	return closed
}

// revalidateIdentities runs the identity check and closes the clients and
// revocables of the revoked identities.
func (h *Hub) revalidateIdentities(ctx context.Context, check IdentityChecker, identities []ClientIdentity) int {
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
	return h.disconnectIdentities(func(id ClientIdentity) bool { return revokedKeys[keyOf(id)] })
}

// revalidateAccess runs the access checker on the registered revocables and
// closes the ones that lost access (1008). It does nothing without a checker.
func (h *Hub) revalidateAccess(ctx context.Context) int {
	checkPtr := h.accessChecker.Load()
	if checkPtr == nil {
		return 0
	}
	h.mu.RLock()
	rs := make([]Revocable, 0, len(h.revocables))
	for r := range h.revocables {
		rs = append(rs, r)
	}
	h.mu.RUnlock()
	if len(rs) == 0 {
		return 0
	}
	lost := (*checkPtr)(ctx, rs)
	if len(lost) != len(rs) {
		slog.Error("WebSocket revalidation: access checker returned a wrong result size",
			"revocables", len(rs), "results", len(lost))
		return 0
	}
	losers := make(map[Revocable]bool)
	for i, l := range lost {
		if l {
			losers[rs[i]] = true
		}
	}
	if len(losers) == 0 {
		return 0
	}
	return h.revokeRevocables(func(r Revocable) bool { return losers[r] }, CloseCodePolicyViolation, CloseReasonAccessLost)
}
