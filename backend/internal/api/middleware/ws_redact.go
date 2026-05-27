package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// wsContextKey is the gin.Context key used to pass the WebSocket
// authentication token from the redaction middleware to the handler.
// Exported via Get* helpers below rather than as a string constant so
// the contract stays in one place.
const wsContextKey = "ws.token"

// RedactWSToken strips the `token` query parameter from `/ws` requests
// before any logging or tracing middleware can capture it.
//
// The WebSocket /ws endpoint accepts a JWT via a `?token=<jwt>` query
// parameter (for browsers that can't send Authorization headers on
// upgrade). Several downstream middlewares — most notably otelgin,
// which sets the `http.target` span attribute to the raw RequestURI —
// would otherwise persist that token in traces, access logs, and
// observability backends. This middleware moves the value into the
// gin.Context under `ws.token` and rewrites the URL so the rest of
// the stack only ever sees `/ws` with no query string.
//
// The middleware MUST be registered before any middleware that reads
// `c.Request.URL.RawQuery` or `c.Request.RequestURI` (otelgin,
// HTTPMetrics, custom access-log middlewares). It is a no-op for
// every path other than `/ws`.
//
// The handler retrieves the token via WSTokenFromContext below.
func RedactWSToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Match `/ws` exactly OR any descendant (`/ws/v2`, `/ws/foo`).
		// `/wsfoo` and `/wso` are NOT matched — the second segment must
		// be either absent or path-separated. Keeps redaction in scope
		// even if a future route adds versioned WS endpoints.
		path := c.Request.URL.Path
		if path != "/ws" && !strings.HasPrefix(path, "/ws/") {
			c.Next()
			return
		}
		q := c.Request.URL.Query()
		// Check key presence (not value!) so `/ws?token=` is scrubbed
		// just like `/ws?token=<jwt>` is. Leaving `token=` in the URL
		// would still leak the param name in access logs and confuse
		// log-scanning tools looking for "this request had token=…".
		if _, hasToken := q["token"]; !hasToken {
			c.Next()
			return
		}
		// Stash for the handler only when a non-empty value was sent —
		// `?token=` (empty) is treated as "no credential" by the handler.
		if token := q.Get("token"); token != "" {
			c.Set(wsContextKey, token)
		}
		// Scrub from the request URL so downstream middleware (otelgin
		// `http.target`, any access logger that reads RawQuery /
		// RequestURI) does not capture the token. RequestURI is what
		// `RequestURI` field of *http.Request reports — we update it
		// too, even though otelgin reads URL.Path + URL.RawQuery, to
		// keep the request struct internally consistent.
		q.Del("token")
		c.Request.URL.RawQuery = q.Encode()
		if c.Request.RequestURI != "" {
			c.Request.RequestURI = c.Request.URL.RequestURI()
		}
		c.Next()
	}
}

// WSTokenFromContext returns the JWT extracted by RedactWSToken, if
// any. The empty string means no token was found in the request — the
// handler should fall back to Authorization header / Sec-WebSocket-
// Protocol checking before failing the upgrade.
func WSTokenFromContext(c *gin.Context) string {
	if v, ok := c.Get(wsContextKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
