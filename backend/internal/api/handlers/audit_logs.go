package handlers

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

const maxExportLimit = 10000

// AuditLogHandler handles audit log query endpoints.
type AuditLogHandler struct {
	auditRepo models.AuditLogRepository
}

// NewAuditLogHandler creates a new AuditLogHandler.
func NewAuditLogHandler(auditRepo models.AuditLogRepository) *AuditLogHandler {
	return &AuditLogHandler{auditRepo: auditRepo}
}

// parseAuditFilters extracts common audit log filter parameters from the request.
func parseAuditFilters(c *gin.Context) (models.AuditLogFilters, error) {
	filters := models.AuditLogFilters{
		UserID:     c.Query("user_id"),
		EntityType: c.Query("entity_type"),
		EntityID:   c.Query("entity_id"),
		Action:     c.Query("action"),
		Cursor:     c.Query("cursor"),
		Limit:      25,
		Offset:     0,
	}

	if limitStr := c.Query("limit"); limitStr != "" {
		l, err := strconv.Atoi(limitStr)
		if err != nil || l < 0 {
			return filters, fmt.Errorf("Invalid limit parameter")
		}
		filters.Limit = l
	}

	if offsetStr := c.Query("offset"); offsetStr != "" {
		o, err := strconv.Atoi(offsetStr)
		if err != nil || o < 0 {
			return filters, fmt.Errorf("Invalid offset parameter")
		}
		filters.Offset = o
	}

	if sd := c.Query("start_date"); sd != "" {
		t, err := time.Parse(time.RFC3339, sd)
		if err != nil {
			return filters, fmt.Errorf("Invalid start_date format (use RFC3339)")
		}
		filters.StartDate = &t
	}

	if ed := c.Query("end_date"); ed != "" {
		t, err := time.Parse(time.RFC3339, ed)
		if err != nil {
			return filters, fmt.Errorf("Invalid end_date format (use RFC3339)")
		}
		filters.EndDate = &t
	}

	return filters, nil
}

// ListAuditLogs godoc
// @Summary     List audit logs
// @Description List audit logs with optional filters and pagination. Supports cursor-based pagination for efficient large dataset traversal.
// @Tags        audit-logs
// @Produce     json
// @Param       user_id     query    string false "Filter by user ID"
// @Param       entity_type query    string false "Filter by entity type"
// @Param       entity_id   query    string false "Filter by entity ID"
// @Param       action      query    string false "Filter by action"
// @Param       start_date  query    string false "Start date (RFC3339)"
// @Param       end_date    query    string false "End date (RFC3339)"
// @Param       limit       query    int    false "Page size (default 25)"
// @Param       offset      query    int    false "Offset (default 0)"
// @Param       cursor      query    string false "Cursor from previous page for cursor-based pagination (overrides offset)"
// @Success     200         {object} models.PaginatedAuditLogs
// @Failure     400         {object} map[string]string
// @Failure     500         {object} map[string]string
// @Router      /api/v1/audit-logs [get]
func (h *AuditLogHandler) ListAuditLogs(c *gin.Context) {
	filters, err := parseAuditFilters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, err := h.auditRepo.List(filters)
	if err != nil {
		status, message := mapError(err, "Audit log")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, models.PaginatedAuditLogs{
		Data:       result.Data,
		Total:      result.Total,
		Limit:      filters.Limit,
		Offset:     filters.Offset,
		NextCursor: result.NextCursor,
	})
}

// ExportAuditLogs godoc
// @Summary     Export audit logs
// @Description Export audit logs as CSV or JSON file download
// @Tags        audit-logs
// @Produce     octet-stream
// @Security    BearerAuth
// @Param       format      query    string false "Export format: csv or json (default: json)"
// @Param       user_id     query    string false "Filter by user ID"
// @Param       entity_type query    string false "Filter by entity type"
// @Param       entity_id   query    string false "Filter by entity ID"
// @Param       action      query    string false "Filter by action"
// @Param       start_date  query    string false "Start date (RFC3339)"
// @Param       end_date    query    string false "End date (RFC3339)"
// @Success     200         {file}   file
// @Failure     400         {object} map[string]string
// @Failure     500         {object} map[string]string
// @Router      /api/v1/audit-logs/export [get]
func (h *AuditLogHandler) ExportAuditLogs(c *gin.Context) {
	format := c.DefaultQuery("format", "json")
	if format != "json" && format != "csv" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid format parameter (use 'json' or 'csv')"})
		return
	}

	filters, err := parseAuditFilters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Override pagination for export: fetch from beginning, up to hard max.
	filters.Limit = maxExportLimit
	filters.Offset = 0

	result, err := h.auditRepo.List(filters)
	if err != nil {
		slog.Error("failed to export audit logs", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}
	logs := result.Data

	timestamp := time.Now().UTC().Format("20060102-150405")

	switch format {
	case "csv":
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		if err := w.Write([]string{"ID", "Timestamp", "UserID", "Username", "Action", "EntityType", "EntityID", "Details"}); err != nil {
			slog.Error("failed to write CSV header", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}
		for _, l := range logs {
			if err := w.Write([]string{
				l.ID,
				l.Timestamp.Format(time.RFC3339),
				l.UserID,
				l.Username,
				l.Action,
				l.EntityType,
				l.EntityID,
				l.Details,
			}); err != nil {
				slog.Error("failed to write CSV row", "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
				return
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			slog.Error("CSV flush error", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
			return
		}

		filename := fmt.Sprintf("audit-logs-%s.csv", timestamp)
		c.Header("Content-Type", "text/csv")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
		c.Data(http.StatusOK, "text/csv", buf.Bytes())
	default: // json
		filename := fmt.Sprintf("audit-logs-%s.json", timestamp)
		c.Header("Content-Type", "application/json")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))

		if err := json.NewEncoder(c.Writer).Encode(logs); err != nil {
			slog.Error("failed to encode audit logs JSON", "error", err)
		}
	}
}
