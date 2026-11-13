package middleware

import (
	"strings"
	"unicode/utf8"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
)

// AuditRequestContext is the request context of an audit entry (#785). It is
// a plain value: code that runs after the handler returned (stream end
// entries, goroutines) must use a captured value, because Gin reuses the
// *gin.Context.
type AuditRequestContext struct {
	ClientIP   string
	RequestID  string
	TraceID    string
	AuthMethod string
	APIKeyID   string
}

// CaptureRequestContext reads the audit request context while the handler
// runs. The client IP obeys TRUSTED_PROXIES (c.ClientIP).
func CaptureRequestContext(c *gin.Context) AuditRequestContext {
	rc := AuditRequestContext{
		ClientIP:   TruncateUTF8(c.ClientIP(), models.MaxAuditClientIPLen),
		RequestID:  TruncateUTF8(c.GetString(RequestIDKey), models.MaxAuditRequestIDLen),
		AuthMethod: GetAuthMethodFromContext(c),
		APIKeyID:   GetAPIKeyIDFromContext(c),
	}
	if sc := trace.SpanContextFromContext(c.Request.Context()); sc.IsValid() {
		rc.TraceID = sc.TraceID().String()
	}
	return rc
}

// Apply copies the context into an entry. A set AuthMethod (password, oidc,
// system) stays.
func (r AuditRequestContext) Apply(e *models.AuditLog) {
	e.ClientIP = r.ClientIP
	e.RequestID = r.RequestID
	e.TraceID = r.TraceID
	e.APIKeyID = r.APIKeyID
	if e.AuthMethod == "" {
		e.AuthMethod = r.AuthMethod
	}
}

// ApplyRequestContext copies the request context into an entry. Call it only
// while the handler runs.
func ApplyRequestContext(c *gin.Context, e *models.AuditLog) {
	CaptureRequestContext(c).Apply(e)
}

// auditEntityNameKey holds the entity name that a handler sets for its audit
// entry. A private key type, so no other c.Set can collide with it.
type auditEntityNameKey struct{}

// SetAuditEntityName sets the entity name of the audit entry of this request,
// for example the name of a deleted entity, which the response does not hold.
func SetAuditEntityName(c *gin.Context, name string) {
	c.Set(auditEntityNameKey{}, name)
}

// TruncateUTF8 returns s as valid UTF-8 (invalid bytes become U+FFFD) of at
// most max bytes, cut at a rune start.
func TruncateUTF8(s string, max int) string {
	if max <= 0 {
		return ""
	}
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// validRequestID reports whether a client X-Request-ID is safe to keep: 1 to
// 64 bytes of [A-Za-z0-9._-].
func validRequestID(id string) bool {
	if id == "" || len(id) > models.MaxAuditRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		b := id[i]
		ok := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '.' || b == '_' || b == '-'
		if !ok {
			return false
		}
	}
	return true
}
