// Package ctxutil holds context helpers shared by the API packages.
package ctxutil

import (
	"context"
	"time"
)

// WriteTimeout limits a detached write of a request: the writes with side
// effects after the commit (audit entry, hook, revoke) and the reads they need
// (#674).
const WriteTimeout = 30 * time.Second

// Detach returns a context with the values of parent (request ID, trace,
// hook trigger) but without its cancel and deadline, which ends after timeout.
// A client that leaves cannot skip the work, and a stalled database cannot
// hold it forever: the work then ends with context.DeadlineExceeded. The
// caller must call the cancel function.
func Detach(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}
