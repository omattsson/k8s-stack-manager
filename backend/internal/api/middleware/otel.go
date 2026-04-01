package middleware

import (
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// SpanEnrichUser extracts authenticated user information from the Gin context
// (set by the auth middleware) and records it as span attributes so traces
// can be correlated with specific users. This middleware should be registered
// AFTER the auth middleware.
func SpanEnrichUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		span := trace.SpanFromContext(c.Request.Context())
		if !span.SpanContext().IsValid() {
			c.Next()
			return
		}

		if userID, ok := c.Get("userID"); ok {
			if id, isStr := userID.(string); isStr && id != "" {
				span.SetAttributes(attribute.String("user.id", id))
			}
		}

		if username, ok := c.Get("username"); ok {
			if name, isStr := username.(string); isStr && name != "" {
				span.SetAttributes(attribute.String("user.name", name))
			}
		}

		if role, ok := c.Get("role"); ok {
			if r, isStr := role.(string); isStr && r != "" {
				span.SetAttributes(attribute.String("user.role", r))
			}
		}

		c.Next()
	}
}
