package websocket

import (
	"context"
	"log/slog"
	"time"
)

// Close codes that the hub passes to Revocable.Revoke.
const (
	// CloseCodeGoingAway closes a revocable when the hub shuts down (1001).
	CloseCodeGoingAway = 1001
	// CloseCodePolicyViolation closes a revocable whose credential or access
	// was revoked (1008).
	CloseCodePolicyViolation = 1008
	// CloseReasonAccessLost is the reason for a revocable whose instance was
	// deleted, stopped or cleaned, or whose user lost access to it.
	CloseReasonAccessLost = "access revoked"
	// CloseReasonShutdown is the reason on hub shutdown.
	CloseReasonShutdown = "server shutdown"
)

// Revocable is a long-lived connection that is not a /ws client but must be
// closed by the same revocations: a port-forward or exec session. The hub
// closes it on DisconnectUser, DisconnectToken, DisconnectAPIKey (also the
// ones that came through the fan-out from another replica), on a failed
// revalidation, on DisconnectInstance and on shutdown. The implementation must
// be comparable (use a pointer type): the hub keeps revocables in a map.
// Identity and InstanceID are called under the hub lock: keep them cheap. A revocable does not
// count in ClientCount and does not receive broadcasts.
type Revocable interface {
	// Identity is the credential of the connection. The revalidation runs the
	// IdentityChecker on it like on the identity of a /ws client.
	Identity() ClientIdentity
	// InstanceID is the stack instance of the connection (access re-check and
	// DisconnectInstance).
	InstanceID() string
	// Kind names the feature of the connection (for example "port_forward").
	// The access re-check uses it to apply the switch of that feature only.
	Kind() string
	// Revoke closes the connection with a WebSocket close code and reason. It
	// must not block (start the close in a goroutine or signal a channel) and
	// may be called from any goroutine. The hub calls it at most once, outside
	// its lock.
	Revoke(code int, reason string)
}

// RevocableAccessChecker reports which revocables lost their access (true:
// close with 1008). It has one result per revocable. On a lookup error it
// returns false for the affected ones (fail open) and logs the error, for
// example with a LogLimiter.
type RevocableAccessChecker func(ctx context.Context, revocables []Revocable) []bool

// RegisterRevocable adds r to the hub. The returned function removes it
// again; call it when the connection ends. It is safe to call more than
// once. It returns ErrHubClosed when the hub has shut down.
func (h *Hub) RegisterRevocable(r Revocable) (unregister func(), err error) {
	h.mu.Lock()
	select {
	case <-h.done:
		h.mu.Unlock()
		return nil, ErrHubClosed
	default:
	}
	h.revocables[r] = struct{}{}
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		delete(h.revocables, r)
		h.mu.Unlock()
	}, nil
}

// RevocableCount returns the number of registered revocables.
func (h *Hub) RevocableCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.revocables)
}

// SetRevocableAccessChecker sets the access re-check that every revalidation
// run applies to the revocables that passed the identity check. Call it
// before the traffic starts. A nil checker removes the re-check.
func (h *Hub) SetRevocableAccessChecker(check RevocableAccessChecker) {
	if check == nil {
		h.accessChecker.Store(nil)
		return
	}
	h.accessChecker.Store(&check)
}

// DisconnectInstance closes the local revocables of the stack instance with
// 1008 (instance deleted, stopped or cleaned). Other replicas close theirs at
// the next revalidation. It returns the number of closed revocables.
func (h *Hub) DisconnectInstance(instanceID string) int {
	if h == nil || instanceID == "" {
		return 0
	}
	return h.revokeRevocables(func(r Revocable) bool { return r.InstanceID() == instanceID },
		CloseCodePolicyViolation, CloseReasonAccessLost)
}

// takeRevocablesLocked removes the matching revocables from the set and
// returns them. Caller holds h.mu (write lock).
func (h *Hub) takeRevocablesLocked(match func(Revocable) bool) []Revocable {
	var taken []Revocable
	for r := range h.revocables {
		if match(r) {
			taken = append(taken, r)
			delete(h.revocables, r)
		}
	}
	return taken
}

// revokeRevocables removes the matching revocables and revokes them outside
// the lock. It returns their number.
func (h *Hub) revokeRevocables(match func(Revocable) bool, code int, reason string) int {
	h.mu.Lock()
	taken := h.takeRevocablesLocked(match)
	h.mu.Unlock()
	revokeAll(taken, code, reason)
	return len(taken)
}

// revokeAll calls Revoke on each revocable. A panic in Revoke is logged and
// does not stop the others.
func revokeAll(rs []Revocable, code int, reason string) {
	for _, r := range rs {
		func() {
			defer func() {
				if p := recover(); p != nil {
					slog.Error("Panic in Revocable.Revoke", "recover", p)
				}
			}()
			r.Revoke(code, reason)
		}()
	}
}

// LogLimiter allows a log line at most once per interval. It is safe for
// concurrent use. The revalidation checkers use it so a database outage gives
// one line per interval and not one per run.
type LogLimiter struct {
	l logLimiter
}

// NewLogLimiter returns a LogLimiter. An interval <= 0 allows every line.
func NewLogLimiter(interval time.Duration) *LogLimiter {
	return &LogLimiter{l: logLimiter{every: interval}}
}

// Allow reports whether a line may be logged now, and counts it when it may.
func (l *LogLimiter) Allow() bool {
	if l == nil {
		return true
	}
	ok, _ := l.l.allow(time.Now())
	return ok
}
