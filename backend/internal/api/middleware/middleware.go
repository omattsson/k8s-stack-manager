package middleware

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
)

// CORS middleware with configurable allowed origins.
// Pass "*" or "" to allow all origins (development only).
// For production, pass a comma-separated list of allowed origins.
//
// Note: wildcard ("*") disables Access-Control-Allow-Credentials, which
// prevents cookie-based refresh token rotation for cross-origin requests.
// For local development with a separate frontend origin, set
// CORS_ALLOWED_ORIGINS to the frontend URL (e.g. "http://localhost:5173").
func CORS(allowedOrigins string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if allowedOrigins == "" || allowedOrigins == "*" {
			c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
			// No Allow-Credentials for wildcard — require explicit origin list
		} else {
			requestOrigin := c.Request.Header.Get("Origin")
			if requestOrigin != "" {
				allowed := false
				for _, origin := range strings.Split(allowedOrigins, ",") {
					if strings.TrimSpace(origin) == requestOrigin {
						c.Writer.Header().Set("Access-Control-Allow-Origin", requestOrigin)
						c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
						c.Writer.Header().Set("Vary", "Origin")
						allowed = true
						break
					}
				}
				if !allowed {
					// Block requests from non-whitelisted origins as defense-in-depth;
					// browsers enforce CORS client-side, but we also enforce server-side.
					c.AbortWithStatus(http.StatusForbidden)
					return
				}
			}
			// If there is no Origin header, treat this as a non-CORS request:
			// allow it through without setting Access-Control-Allow-Origin.
		}
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, Authorization, X-Request-ID, X-API-Key")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// Logger is a middleware that logs incoming requests using structured logging.
// When OpenTelemetry is active, trace_id and span_id are included in the log output
// for correlation with distributed traces.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		attrs := []any{
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
		}

		// If a trace span exists (otelgin middleware ran before us), include IDs.
		if spanCtx := trace.SpanFromContext(c.Request.Context()).SpanContext(); spanCtx.IsValid() {
			attrs = append(attrs,
				"trace_id", spanCtx.TraceID().String(),
				"span_id", spanCtx.SpanID().String(),
			)
		}

		slog.Info("incoming request", attrs...)
		c.Next()
	}
}

// Recovery is a middleware that recovers from any panics and writes a 500 if there was one.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("recovered from panic", "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
				c.Abort()
			}
		}()
		c.Next()
	}
}

// RequestID adds a unique request ID to each request.
// If the client sends an X-Request-ID header, it is reused; otherwise a new one is generated.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = generateRequestID()
		}
		c.Set("request_id", requestID)
		c.Writer.Header().Set("X-Request-ID", requestID)
		c.Next()
	}
}

// maxBytesBodyCapture wraps an io.ReadCloser to detect *http.MaxBytesError.
type maxBytesBodyCapture struct {
	rc       io.ReadCloser
	exceeded *bool
}

func (m *maxBytesBodyCapture) Read(p []byte) (int, error) {
	n, err := m.rc.Read(p)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			*m.exceeded = true
		}
	}
	return n, err
}

func (m *maxBytesBodyCapture) Close() error {
	return m.rc.Close()
}

// MaxBodySize limits the size of the request body to prevent memory exhaustion.
// Oversized payloads are translated to a 413 Request Entity Too Large response.
func MaxBodySize(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		var exceeded bool
		limitedReader := http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Request.Body = &maxBytesBodyCapture{rc: limitedReader, exceeded: &exceeded}
		c.Next()

		// If the body size was exceeded and the handler has not yet written
		// a response, return a 413 so clients get a clear signal.
		if exceeded && !c.Writer.Written() {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge,
				gin.H{"error": "request body too large"})
			return
		}
	}
}

// SecurityHeaders adds standard security headers to all responses.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

		// Add HSTS when behind TLS (direct or via reverse proxy)
		forwardedProto := c.GetHeader("X-Forwarded-Proto")
		isTLS := c.Request.TLS != nil
		if !isTLS && forwardedProto != "" {
			// X-Forwarded-Proto can be comma-separated (multiple proxies); check the first value.
			// Scheme values are case-insensitive per RFC 3986.
			first := strings.TrimSpace(strings.SplitN(forwardedProto, ",", 2)[0])
			isTLS = strings.EqualFold(first, "https")
		}
		if isTLS {
			c.Header("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}

		c.Next()
	}
}

func generateRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use timestamp-based ID if crypto/rand fails
		slog.Warn("failed to generate random request ID, using fallback", "error", err)
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
