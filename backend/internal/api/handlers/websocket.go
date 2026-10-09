package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"backend/internal/api/middleware"
	"backend/internal/models"
	"backend/internal/sessionstore"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	gorilla "github.com/gorilla/websocket"
)

// WebSocketHandler handles WebSocket connection upgrades.
// It is a separate struct from Handler because it depends on *websocket.Hub
// rather than models.Repository.
type WebSocketHandler struct {
	sessionStore   sessionstore.SessionStore // nil: no blocklist checks
	userRepo       models.UserRepository     // nil: no user checks
	hub            *websocket.Hub
	allowedOrigins string
	jwtSecret      string
}

// WithRevocationChecks makes the upgrade run the same checks as the HTTP JWT
// auth: the token blocklist and the user blocklist in store, and (with
// userRepo) that the user exists and is not disabled. Either argument may be
// nil to skip its checks. Returns h for chaining.
func (h *WebSocketHandler) WithRevocationChecks(store sessionstore.SessionStore, userRepo models.UserRepository) *WebSocketHandler {
	h.sessionStore = store
	h.userRepo = userRepo
	return h
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
// @Description Upgrades the HTTP connection to a WebSocket for real-time events. Authenticate via one of: `Sec-WebSocket-Protocol: bearer, <jwt>` subprotocol header, `Authorization: Bearer <jwt>` header, or `?token=<jwt>` query parameter (the query token is redacted from access logs and traces by middleware before the handler runs). The upgrade runs the same checks as the HTTP JWT auth: a revoked token (logout), a token issued at or before a user block (user deleted, disabled or password reset), a deleted user and a disabled user get 401. The server closes an open socket with close code 1008 when the session is revoked and when the access token expires; the client must reconnect with a fresh token.
// @Tags websocket
// @Param token query string false "JWT authentication token (also accepted via Authorization header or Sec-WebSocket-Protocol subprotocol)"
// @Success 101 "Switching Protocols"
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 500 {object} map[string]string
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
	claims, err := middleware.ValidateJWT(tokenStr, h.jwtSecret)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
		return
	}
	if !h.checkSession(c, claims) {
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

	identity := websocket.ClientIdentity{UserID: claims.UserID, TokenID: claims.ID}
	if claims.ExpiresAt != nil {
		identity.ExpiresAt = claims.ExpiresAt.Time
	}
	if claims.IssuedAt != nil {
		identity.IssuedAt = claims.IssuedAt.Time
	}
	client, err := websocket.NewClientWithIdentity(h.hub, conn, identity)
	if err != nil {
		slog.Error("WebSocket client creation failed", "error", err)
		return
	}

	// A logout or a user revoke can run between the check above and the
	// registration; its disconnect then missed this client. Check once more
	// now that the client is registered.
	if middleware.CheckRevocation(c.Request.Context(), h.sessionStore, claims) != nil {
		h.hub.DisconnectClient(client)
	}
}

// checkSession runs the revocation checks of the HTTP JWT auth before the
// upgrade. It writes the error response and returns false when the upgrade
// must not happen:
//   - Token blocklist or user blocklist match: 401. A store error is logged
//     and the check passes (fail open, same policy as the HTTP path).
//   - User not found (deleted) or disabled: 401.
//   - Other user lookup error: 500. The upgrade fails closed here: the user
//     lookup is the only check that sees a disabled user directly, and a
//     refused socket only delays live updates until the client retries.
func (h *WebSocketHandler) checkSession(c *gin.Context, claims *middleware.Claims) bool {
	if err := middleware.CheckRevocation(c.Request.Context(), h.sessionStore, claims); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": middleware.RevocationMessage(err)})
		return false
	}
	if h.userRepo == nil {
		return true
	}
	if claims.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
		return false
	}
	inactive, err := userInactive(h.userRepo, claims.UserID)
	if err != nil {
		slog.Error("WebSocket user lookup failed", "user_id", claims.UserID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return false
	}
	if inactive {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Session revoked"})
		return false
	}
	return true
}

// userInactive reports whether the user is deleted (not found) or disabled.
// Other lookup errors are returned.
func userInactive(repo models.UserRepository, userID string) (bool, error) {
	user, err := repo.FindByID(userID)
	if err != nil {
		if isNotFoundError(err) {
			return true, nil
		}
		return false, err
	}
	return user.Disabled, nil
}

// NewWebSocketRevocationChecker returns the check that the hub runs every
// websocket.DefaultRevalidateInterval on the open sockets (see
// Hub.StartRevalidation). It applies the upgrade checks to each identity:
// the token and user blocklists in store (middleware.RevocationStatus) and,
// with userRepo, a deleted or disabled user. It looks up each distinct user
// once per run.
//
// On the first session store or user lookup error the run stops: the error
// is logged once, the rest of the identities pass (fail open), and the next
// run retries. So a database outage gives one log line and one failed query
// per run, not one per socket, and does not close any socket.
// Either argument may be nil to skip its checks; with both nil it returns nil.
func NewWebSocketRevocationChecker(store sessionstore.SessionStore, userRepo models.UserRepository) websocket.IdentityChecker {
	if store == nil && userRepo == nil {
		return nil
	}
	return func(ctx context.Context, identities []websocket.ClientIdentity) []bool {
		revoked := make([]bool, len(identities))
		inactiveUsers := make(map[string]bool)
		for i, id := range identities {
			if ctx.Err() != nil {
				return revoked
			}
			claims := &middleware.Claims{UserID: id.UserID}
			claims.ID = id.TokenID
			if !id.IssuedAt.IsZero() {
				claims.IssuedAt = jwt.NewNumericDate(id.IssuedAt)
			}
			revokedErr, lookupErr := middleware.RevocationStatus(ctx, store, claims)
			if revokedErr != nil {
				revoked[i] = true
				continue
			}
			if lookupErr != nil {
				slog.Error("WebSocket revalidation stopped: session store check failed; the next run retries",
					"checked", i, "identities", len(identities), "error", lookupErr)
				return revoked
			}
			if userRepo == nil || id.UserID == "" {
				continue
			}
			inactive, seen := inactiveUsers[id.UserID]
			if !seen {
				var err error
				inactive, err = userInactive(userRepo, id.UserID)
				if err != nil {
					slog.Error("WebSocket revalidation stopped: user lookup failed; the next run retries",
						"user_id", id.UserID, "checked", i, "identities", len(identities), "error", err)
					return revoked
				}
				inactiveUsers[id.UserID] = inactive
			}
			revoked[i] = inactive
		}
		return revoked
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
