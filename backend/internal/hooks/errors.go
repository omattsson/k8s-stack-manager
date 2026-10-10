package hooks

import (
	"errors"
	"fmt"
	"strings"
)

// maxDenyMessageLen limits the subscriber deny message that the core shows
// to users (deployment log, notifications).
const maxDenyMessageLen = 500

// DeniedError is returned (wrapped in *FailedError) when a subscriber answers
// with allowed=false. Message is the subscriber's reason.
type DeniedError struct {
	Hook    string
	Message string
}

func (e *DeniedError) Error() string { return e.Message }

// FailedError is returned by Fire when a subscription with
// failure_policy=fail fails or denies. Err is the cause: a *DeniedError for a
// denial, else a transport or decode error. The transport error can contain
// the subscriber URL; show UserMessage to users, not Error().
type FailedError struct {
	Hook string
	Err  error
}

func (e *FailedError) Error() string {
	return fmt.Sprintf("hook %q failed (failure_policy=fail): %v", e.Hook, e.Err)
}

func (e *FailedError) Unwrap() error { return e.Err }

// RedirectError is returned when a subscriber answers with a 3xx status.
// The dispatcher does not follow redirects: a signed body must not go to
// another URL. The error does not contain the Location header or the body.
type RedirectError struct {
	StatusCode int
}

// Error starts with "hook returned status" so that the dispatch span gets
// the http_error outcome.
func (e *RedirectError) Error() string {
	return fmt.Sprintf("hook returned status %d (redirect; redirects are not followed)", e.StatusCode)
}

// UserMessage returns a message that is safe to show to users for a hook
// error of event (for example "pre-rollback") that blocked operation (for
// example "rollback"): the subscriber reason for a denial, a generic text for
// other failures. It never contains the subscriber URL.
func UserMessage(err error, event, operation string) string {
	var failed *FailedError
	hook := ""
	if asFailed(err, &failed) {
		hook = failed.Hook
	}
	var denied *DeniedError
	if asDenied(err, &denied) {
		if hook == "" {
			hook = denied.Hook
		}
		return fmt.Sprintf("%s hook %q denied the %s: %s", event, hook, operation, sanitizeDenyMessage(denied.Message))
	}
	var redirect *RedirectError
	if hook != "" && errors.As(err, &redirect) {
		return fmt.Sprintf("%s hook %q failed (the subscriber answered with a redirect)", event, hook)
	}
	if hook != "" {
		return fmt.Sprintf("%s hook %q failed (unreachable or timed out)", event, hook)
	}
	return fmt.Sprintf("%s hook failed", event)
}

// sanitizeDenyMessage flattens a subscriber message to one line and limits
// its length.
func sanitizeDenyMessage(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > maxDenyMessageLen {
		msg = msg[:maxDenyMessageLen] + "…"
	}
	if msg == "" {
		msg = "subscriber denied"
	}
	return msg
}

func asFailed(err error, target **FailedError) bool { return errors.As(err, target) }

func asDenied(err error, target **DeniedError) bool { return errors.As(err, target) }
