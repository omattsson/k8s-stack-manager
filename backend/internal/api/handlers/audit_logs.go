package handlers

import (
	"net/http"
	"strconv"
	"time"

	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// AuditLogHandler handles audit log query endpoints.
type AuditLogHandler struct {
	auditRepo models.AuditLogRepository
}

// NewAuditLogHandler creates a new AuditLogHandler.
func NewAuditLogHandler(auditRepo models.AuditLogRepository) *AuditLogHandler {
	return &AuditLogHandler{auditRepo: auditRepo}
}

// ListAuditLogs godoc
// @Summary     List audit logs
// @Description List audit logs with optional filters and pagination
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
// @Success     200         {object} models.PaginatedAuditLogs
// @Failure     400         {object} map[string]string
// @Failure     500         {object} map[string]string
// @Router      /api/v1/audit-logs [get]
func (h *AuditLogHandler) ListAuditLogs(c *gin.Context) {
	filters := models.AuditLogFilters{
		UserID:     c.Query("user_id"),
		EntityType: c.Query("entity_type"),
		EntityID:   c.Query("entity_id"),
		Action:     c.Query("action"),
		Limit:      25,
		Offset:     0,
	}

	if limitStr := c.Query("limit"); limitStr != "" {
		l, err := strconv.Atoi(limitStr)
		if err != nil || l < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid limit parameter"})
			return
		}
		filters.Limit = l
	}

	if offsetStr := c.Query("offset"); offsetStr != "" {
		o, err := strconv.Atoi(offsetStr)
		if err != nil || o < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid offset parameter"})
			return
		}
		filters.Offset = o
	}

	if sd := c.Query("start_date"); sd != "" {
		t, err := time.Parse(time.RFC3339, sd)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid start_date format (use RFC3339)"})
			return
		}
		filters.StartDate = &t
	}

	if ed := c.Query("end_date"); ed != "" {
		t, err := time.Parse(time.RFC3339, ed)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid end_date format (use RFC3339)"})
			return
		}
		filters.EndDate = &t
	}

	logs, total, err := h.auditRepo.List(filters)
	if err != nil {
		status, message := mapError(err, "Audit log")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, models.PaginatedAuditLogs{
		Data:   logs,
		Total:  total,
		Limit:  filters.Limit,
		Offset: filters.Offset,
	})
}
