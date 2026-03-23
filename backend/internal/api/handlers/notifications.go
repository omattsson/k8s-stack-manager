package handlers

import (
	"log/slog"
	"net/http"
	"strconv"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// NotificationHandler handles notification endpoints.
type NotificationHandler struct {
	repo models.NotificationRepository
}

// NewNotificationHandler creates a new NotificationHandler.
func NewNotificationHandler(repo models.NotificationRepository) *NotificationHandler {
	return &NotificationHandler{repo: repo}
}

// updatePreferenceRequest represents a single preference update entry.
type updatePreferenceRequest struct {
	EventType string `json:"event_type"`
	Enabled   bool   `json:"enabled"`
}

// List godoc
// @Summary      List notifications
// @Description  List the authenticated user's notifications with optional filters and pagination
// @Tags         notifications
// @Produce      json
// @Security     BearerAuth
// @Param        unread_only query    bool   false "Only return unread notifications"
// @Param        limit       query    int    false "Page size (default 20, max 100)"
// @Param        offset      query    int    false "Offset (default 0)"
// @Success      200         {object} models.PaginatedNotifications
// @Failure      400         {object} map[string]string
// @Failure      401         {object} map[string]string
// @Failure      500         {object} map[string]string
// @Router       /api/v1/notifications [get]
func (h *NotificationHandler) List(c *gin.Context) {
	userID := middleware.GetUserIDFromContext(c)

	unreadOnly := c.Query("unread_only") == "true"

	limit := 20
	if limitStr := c.Query("limit"); limitStr != "" {
		l, err := strconv.Atoi(limitStr)
		if err != nil || l < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid limit parameter"})
			return
		}
		if l > 100 {
			l = 100
		}
		limit = l
	}

	offset := 0
	if offsetStr := c.Query("offset"); offsetStr != "" {
		o, err := strconv.Atoi(offsetStr)
		if err != nil || o < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid offset parameter"})
			return
		}
		offset = o
	}

	notifications, total, err := h.repo.ListByUser(c.Request.Context(), userID, unreadOnly, limit, offset)
	if err != nil {
		slog.Error("Failed to list notifications", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	unreadCount, err := h.repo.CountUnread(c.Request.Context(), userID)
	if err != nil {
		slog.Error("Failed to count unread notifications", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.JSON(http.StatusOK, models.PaginatedNotifications{
		Notifications: notifications,
		Total:         total,
		UnreadCount:   unreadCount,
	})
}

// CountUnread godoc
// @Summary      Count unread notifications
// @Description  Returns the unread notification count for badge display
// @Tags         notifications
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object} map[string]int64
// @Failure      401  {object} map[string]string
// @Failure      500  {object} map[string]string
// @Router       /api/v1/notifications/count [get]
func (h *NotificationHandler) CountUnread(c *gin.Context) {
	userID := middleware.GetUserIDFromContext(c)

	count, err := h.repo.CountUnread(c.Request.Context(), userID)
	if err != nil {
		slog.Error("Failed to count unread notifications", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"unread_count": count})
}

// MarkAsRead godoc
// @Summary      Mark notification as read
// @Description  Mark a single notification as read (verifies ownership)
// @Tags         notifications
// @Produce      json
// @Security     BearerAuth
// @Param        id   path string true "Notification ID"
// @Success      200  {object} map[string]string
// @Failure      400  {object} map[string]string
// @Failure      401  {object} map[string]string
// @Failure      404  {object} map[string]string
// @Failure      500  {object} map[string]string
// @Router       /api/v1/notifications/{id}/read [post]
func (h *NotificationHandler) MarkAsRead(c *gin.Context) {
	userID := middleware.GetUserIDFromContext(c)
	id := c.Param("id")

	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Notification ID is required"})
		return
	}

	if err := h.repo.MarkAsRead(c.Request.Context(), id, userID); err != nil {
		status, message := mapError(err, "Notification")
		if status >= http.StatusInternalServerError {
			slog.Error("Failed to mark notification as read", "error", err, "id", id, "user_id", userID)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// MarkAllAsRead godoc
// @Summary      Mark all notifications as read
// @Description  Mark all of the authenticated user's notifications as read
// @Tags         notifications
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object} map[string]string
// @Failure      401  {object} map[string]string
// @Failure      500  {object} map[string]string
// @Router       /api/v1/notifications/read-all [post]
func (h *NotificationHandler) MarkAllAsRead(c *gin.Context) {
	userID := middleware.GetUserIDFromContext(c)

	if err := h.repo.MarkAllAsRead(c.Request.Context(), userID); err != nil {
		slog.Error("Failed to mark all notifications as read", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// GetPreferences godoc
// @Summary      Get notification preferences
// @Description  Get the authenticated user's notification preferences
// @Tags         notifications
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}  models.NotificationPreference
// @Failure      401  {object} map[string]string
// @Failure      500  {object} map[string]string
// @Router       /api/v1/notifications/preferences [get]
func (h *NotificationHandler) GetPreferences(c *gin.Context) {
	userID := middleware.GetUserIDFromContext(c)

	prefs, err := h.repo.GetPreferences(c.Request.Context(), userID)
	if err != nil {
		status, message := mapError(err, "Notification preferences")
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, prefs)
}

// UpdatePreferences godoc
// @Summary      Update notification preferences
// @Description  Update the authenticated user's notification preferences (array of event_type + enabled)
// @Tags         notifications
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        preferences body     []updatePreferenceRequest true "Preferences to update"
// @Success      200         {array}  models.NotificationPreference
// @Failure      400         {object} map[string]string
// @Failure      401         {object} map[string]string
// @Failure      500         {object} map[string]string
// @Router       /api/v1/notifications/preferences [put]
func (h *NotificationHandler) UpdatePreferences(c *gin.Context) {
	userID := middleware.GetUserIDFromContext(c)

	var reqs []updatePreferenceRequest
	if err := c.ShouldBindJSON(&reqs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	if len(reqs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "At least one preference is required"})
		return
	}

	for _, r := range reqs {
		if r.EventType == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "event_type is required for each preference"})
			return
		}
	}

	// Fetch existing preferences to find IDs for update or generate new ones.
	existing, err := h.repo.GetPreferences(c.Request.Context(), userID)
	if err != nil {
		status, message := mapError(err, "Notification preferences")
		c.JSON(status, gin.H{"error": message})
		return
	}

	existingByEvent := make(map[string]string, len(existing))
	for _, p := range existing {
		existingByEvent[p.EventType] = p.ID
	}

	for _, r := range reqs {
		pref := &models.NotificationPreference{
			UserID:    userID,
			EventType: r.EventType,
			Enabled:   r.Enabled,
		}
		if id, ok := existingByEvent[r.EventType]; ok {
			pref.ID = id
		} else {
			pref.ID = generateID()
		}

		if err := h.repo.UpdatePreference(c.Request.Context(), pref); err != nil {
			status, message := mapError(err, "Notification preference")
			c.JSON(status, gin.H{"error": message})
			return
		}
	}

	// Return updated preferences.
	prefs, err := h.repo.GetPreferences(c.Request.Context(), userID)
	if err != nil {
		slog.Error("Failed to get updated preferences", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.JSON(http.StatusOK, prefs)
}

// generateID creates a new UUID string for notifications.
func generateID() string {
	return uuid.New().String()
}
