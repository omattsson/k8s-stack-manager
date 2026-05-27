package handlers

import (
	"log/slog"
	"net/http"
	"strings"

	"backend/internal/api/middleware"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"
)

// WebSocketHandler handles WebSocket connection upgrades.
// It is a separate struct from Handler because it depends on *websocket.Hub
// rather than models.Repository.
type WebSocketHandler struct {
	hub            *websocket.Hub
	allowedOrigins string
	jwtSecret      string
}

// NewWebSocketHandler creates a new WebSocketHandler with the given hub, allowed origins, and JWT secret.
func NewWebSocketHandler(hub *websocket.Hub, allowedOrigins string, jwtSecret string) *WebSocketHandler {
	return &WebSocketHandler{
		hub:            hub,
		allowedOrigins: allowedOrigins,
		jwtSecret:      jwtSecret,
	}
}

// wsSubprotocolBearer is the subprotocol marker for the
// `Sec-WebSocket-Protocol: bearer, <jwt>` auth scheme. The client offers
// it as the first subprotocol; the server selects it on the upgrade
// response so browsers that require subprotocol negotiation complete
// the handshake cleanly.
const wsSubprotocolBearer = "bearer"

// HandleWebSocket godoc
// @Summary Open a WebSocket connection
// @Description Upgrades the HTTP connection to a WebSocket for real-time events. Authenticate via one of: `Sec-WebSocket-Protocol: bearer, <jwt>` subprotocol header, `Authorization: Bearer <jwt>` header, or `?token=<jwt>` query parameter (the query token is redacted from access logs and traces by middleware before the handler runs).
// @Tags websocket
// @Param token query string false "JWT authentication token (also accepted via Authorization header or Sec-WebSocket-Protocol subprotocol)"
// @Success 101 "Switching Protocols"
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /ws [get]
func (h *WebSocketHandler) HandleWebSocket(c *gin.Context) {
	// Auth precedence (most-secure first):
	//   1. Sec-WebSocket-Protocol: bearer, <jwt>     (no URL exposure)
	//   2. Authorization: Bearer <jwt>               (standard header)
	//   3. ?token=<jwt> query param                  (browser fallback)
	//
	// For the query-param path we read first from gin.Context (set by
	// the RedactWSToken middleware after it scrubs the URL) and only
	// fall back to c.Query when the middleware was not wired. The
	// production route stack always wires the middleware so the raw
	// URL is scrubbed before any logging or tracing sees it; the
	// fallback exists so handler unit tests don't need to register
	// the middleware to exercise the query path.
	tokenStr, viaSubprotocol := extractSubprotocolToken(c.GetHeader("Sec-WebSocket-Protocol"))
	// If the client explicitly chose subprotocol auth but the JWT half
	// is missing, 401 immediately. Falling through to Authorization /
	// query would silently switch the credential source — the caller's
	// intent (and audit trail) is "auth via subprotocol", not whatever
	// other header happens to be set.
	if viaSubprotocol && tokenStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}
	if tokenStr == "" {
		if authHeader := c.GetHeader("Authorization"); authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				tokenStr = parts[1]
			}
		}
	}
	if tokenStr == "" {
		tokenStr = middleware.WSTokenFromContext(c)
	}
	if tokenStr == "" {
		// Unreachable in production: RedactWSToken has already moved
		// the value into the gin.Context AND scrubbed it from
		// c.Request.URL.RawQuery, so c.Query returns "". This branch
		// exists only for handler unit tests that mount the route
		// without the redact middleware.
		tokenStr = c.Query("token")
	}

	if tokenStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}

	// Validate the JWT token using shared middleware logic
	if _, err := middleware.ValidateJWT(tokenStr, h.jwtSecret); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
		return
	}

	upgrader := gorilla.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     h.checkOrigin,
	}
	// Advertise the "bearer" subprotocol so gorilla acks it in the
	// handshake response when the client offered it. Strict browser
	// clients reject the upgrade if Sec-WebSocket-Protocol was set on
	// the request but not echoed back. Setting this unconditionally is
	// safe — gorilla only echoes a subprotocol the client actually
	// requested.
	if viaSubprotocol {
		upgrader.Subprotocols = []string{wsSubprotocolBearer}
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Error("WebSocket upgrade failed", "error", err)
		return
	}

	if _, err := websocket.NewClient(h.hub, conn); err != nil {
		slog.Error("WebSocket client creation failed", "error", err)
		return
	}
}

// extractSubprotocolToken parses a Sec-WebSocket-Protocol header value
// and returns the JWT half of a `bearer, <jwt>` pair (along with a flag
// saying that the bearer marker was present, even if the token was
// blank — so the upgrade can still 401 cleanly rather than silently
// falling through to other auth methods).
//
// The header is a comma-separated list per RFC 6455 §11.3.4. The
// credential is the subprotocol IMMEDIATELY adjacent to the bearer
// marker — preferring the one AFTER (`bearer, <jwt>`) over the one
// BEFORE (`<jwt>, bearer`). Non-adjacent subprotocols (e.g. a
// `graphql-ws` declared earlier in the header) are tolerated but
// ignored — they MUST NOT be picked as the credential, otherwise an
// arbitrary non-JWT string ends up validated as a JWT (auth fails
// noisily, but the parser was wrong).
//
// Returns ("", false) when the header is empty or contains no bearer
// marker — the handler then falls back to the next auth method.
// Returns ("", true) when the bearer marker is present but no usable
// adjacent token exists — the handler MUST 401 rather than fall through
// to other auth methods (the client explicitly chose subprotocol auth,
// so silently switching credentials would be wrong).
func extractSubprotocolToken(header string) (token string, viaSubprotocol bool) {
	if header == "" {
		return "", false
	}
	// Pre-clean: trim and drop empty entries so adjacency works on the
	// caller-meaningful subprotocols only.
	rawParts := strings.Split(header, ",")
	parts := make([]string, 0, len(rawParts))
	for _, raw := range rawParts {
		if p := strings.TrimSpace(raw); p != "" {
			parts = append(parts, p)
		}
	}

	isBearer := func(s string) bool { return strings.EqualFold(s, wsSubprotocolBearer) }

	// Pass 1: prefer the canonical `bearer, <jwt>` form. The first
	// non-bearer subprotocol immediately after a bearer marker wins.
	seenBearer := false
	for i, p := range parts {
		if !isBearer(p) {
			continue
		}
		seenBearer = true
		if i+1 < len(parts) && !isBearer(parts[i+1]) {
			return parts[i+1], true
		}
	}
	// Pass 2: tolerate the reversed `<jwt>, bearer` form. The spec puts
	// the most-preferred subprotocol first; some clients put their
	// credential before the marker.
	for i, p := range parts {
		if !isBearer(p) {
			continue
		}
		if i > 0 && !isBearer(parts[i-1]) {
			return parts[i-1], true
		}
	}
	if !seenBearer {
		return "", false
	}
	return "", true
}

// checkOrigin validates the request origin against the configured allowed origins.
func (h *WebSocketHandler) checkOrigin(r *http.Request) bool {
	if h.allowedOrigins == "" || h.allowedOrigins == "*" {
		return true
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	for _, allowed := range strings.Split(h.allowedOrigins, ",") {
		if strings.TrimSpace(allowed) == origin {
			return true
		}
	}

	return false
}
