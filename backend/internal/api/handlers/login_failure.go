package handlers

import (
	"log/slog"
	"unicode/utf8"

	"backend/internal/api/middleware"
)

// Reasons of a failed local login (#619). They go into the log line and into
// the "reason" attribute of auth.login.total. Keep the set fixed: the metric
// attribute must have a low cardinality, and dashboards and alerts may match
// these values. The HTTP response never shows the reason.
const (
	// Credential failures (401 "Invalid username or password").
	loginReasonUserNotFound     = "user_not_found"    // no user with this username
	loginReasonPasswordMismatch = "password_mismatch" // bcrypt: wrong password
	loginReasonNoPassword       = "no_password"       // the user has no local password (SSO user)
	loginReasonHashInvalid      = "hash_invalid"      // the stored hash is malformed (counted as a failure)
	loginReasonRereadNotFound   = "reread_not_found"  // the user was deleted during the password check
	loginReasonHashChanged      = "hash_changed"      // the password changed during the password check
	loginReasonRecheckFailed    = "recheck_failed"    // the user was deleted after the token was signed
	loginReasonSessionRevoked   = "session_revoked"   // the new session was revoked before the response (401 "Session revoked")

	// Account state (403), checked only after a correct password (#632).
	loginReasonDisabled             = "disabled"
	loginReasonLocalLoginNotAllowed = "local_login_not_allowed" // OIDC is on and the user is not a service account

	// Server-side failures (500).
	loginReasonLookupError  = "lookup_error"  // the user lookup failed
	loginReasonRereadError  = "reread_error"  // the re-read after the password check failed
	loginReasonRecheckError = "recheck_error" // the re-check after signing failed
	loginReasonTokenError   = "token_error"   // the access token could not be signed
	loginReasonSessionError = "session_error" // the session re-check failed
)

// maxLoggedUsernameLen caps the username in a log line: the value is client
// input and is not checked before the lookup.
const maxLoggedUsernameLen = 64

// loginFailure describes one failed local login for logLoginFailure.
type loginFailure struct {
	err       error  // server-side cause; logged only for server-side failures
	username  string // submitted username
	clientIP  string // c.ClientIP(): the TCP peer or the client behind TRUSTED_PROXIES
	requestID string // set by middleware.RequestID; empty without it
	userID    string // empty when no user was found
	reason    string // one of the loginReason* constants
	status    string // auth.login.total status: invalid, disabled, restricted, revoked or failure
	httpCode  int
	cacheHit  bool
}

// logLoginFailure writes one structured log line for a failed local login and
// counts it on auth.login.total with its reason. It never logs the password,
// the password hash or a token.
//
// Level: Warn for credential and account failures, the same level as the
// other rejected client authentication events (refresh token reuse, invalid
// OIDC state); a failed login is a security event, and LOGIN_RATE_LIMIT bounds
// the volume for each client IP. Error for server-side failures. hash_invalid
// is counted as a failure but logged at Warn: any client can trigger it with
// the username of a user with a malformed hash, so Error lines would let a
// client set off error-rate alerts.
func (h *AuthHandler) logLoginFailure(f loginFailure) {
	logger := h.logger
	if logger == nil {
		logger = slog.Default()
	}
	attrs := []any{
		"reason", f.reason,
		// The username is logged also for user_not_found, the case where it
		// is most often a password typed into the username field. Tradeoff:
		// forensics (credential stuffing, which account a client tried) need
		// it, and the value is capped; a log reader with access to these
		// lines may therefore see such a mistyped password.
		"username", truncateForLog(f.username, maxLoggedUsernameLen),
		"cache_hit", f.cacheHit,
		"status", f.httpCode,
		"client_ip", f.clientIP,
		"request_id", f.requestID,
	}
	if f.userID != "" {
		attrs = append(attrs, "user_id", f.userID)
	}
	if f.status == "failure" && f.reason != loginReasonHashInvalid {
		if f.err != nil {
			attrs = append(attrs, "error", f.err)
		}
		logger.Error("Login failed", attrs...)
	} else {
		logger.Warn("Login failed", attrs...)
	}
	middleware.RecordLoginFailure("local", f.status, f.reason)
}

// truncateForLog cuts s to at most n bytes at a rune boundary and marks the cut.
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
