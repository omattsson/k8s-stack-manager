package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// AuditLogger allows the audit middleware to persist audit entries.
// models.AuditLogRepository satisfies this interface.
type AuditLogger interface {
	Create(log *models.AuditLog) error
}

// bodyLogWriter wraps gin.ResponseWriter to capture the response body.
type bodyLogWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *bodyLogWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

// NewAuditMiddleware returns middleware that creates an AuditLog entry after
// successful mutating requests (POST, PUT, DELETE with status < 400).
func NewAuditMiddleware(repo AuditLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		if method != http.MethodPost && method != http.MethodPut && method != http.MethodDelete {
			c.Next()
			return
		}

		// Capture response body so we can extract entity ID for creates.
		blw := &bodyLogWriter{body: bytes.NewBuffer(nil), ResponseWriter: c.Writer}
		c.Writer = blw

		c.Next()

		// Only audit successful operations.
		if c.Writer.Status() >= 400 {
			return
		}

		userID := GetUserIDFromContext(c)
		username := GetUsernameFromContext(c)

		var action string
		switch method {
		case http.MethodPost:
			action = "create"
		case http.MethodPut:
			action = "update"
		case http.MethodDelete:
			action = "delete"
		}

		entityType := extractEntityType(c.FullPath())
		entityID := c.Param("id")
		if entityID == "" {
			entityID = c.Param("chartId")
		}

		// For creates, try to extract the ID from the response body.
		if entityID == "" && method == http.MethodPost {
			var resp map[string]interface{}
			if json.Unmarshal(blw.body.Bytes(), &resp) == nil {
				if id, ok := resp["id"]; ok {
					entityID = fmt.Sprintf("%v", id)
				}
			}
		}

		entry := &models.AuditLog{
			ID:         uuid.New().String(),
			UserID:     userID,
			Username:   username,
			Action:     action,
			EntityType: entityType,
			EntityID:   entityID,
			Timestamp:  time.Now().UTC(),
		}

		// Fire and forget — audit failures must not affect the response.
		go func() {
			if err := repo.Create(entry); err != nil {
				slog.Error("failed to create audit log", "error", err)
			}
		}()
	}
}

// extractEntityType derives the entity type from the route path.
// e.g. "/api/v1/stack-definitions/:id/charts/:chartId" → "chart_config"
func extractEntityType(fullPath string) string {
	parts := strings.Split(strings.Trim(fullPath, "/"), "/")

	// Walk backwards to find the last meaningful resource segment.
	for i := len(parts) - 1; i >= 0; i-- {
		seg := parts[i]
		if strings.HasPrefix(seg, ":") {
			continue
		}
		return normalizeEntityType(seg)
	}
	return "unknown"
}

func normalizeEntityType(segment string) string {
	mapping := map[string]string{
		"templates":         "stack_template",
		"stack-definitions": "stack_definition",
		"stack-instances":   "stack_instance",
		"charts":            "chart_config",
		"overrides":         "value_override",
		"auth":              "user",
		"audit-logs":        "audit_log",
	}
	if v, ok := mapping[segment]; ok {
		return v
	}
	return strings.ReplaceAll(segment, "-", "_")
}
