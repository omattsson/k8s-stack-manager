package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"backend/internal/models"
	"backend/internal/notifier"
	notifChannel "backend/internal/notifier/channel"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Notification channel handler constants.
const (
	entityNotificationChannel = "Notification channel"
	msgChannelIDRequired      = "Channel ID is required"
)

// NotificationChannelHandler handles CRUD operations for notification channels.
type NotificationChannelHandler struct {
	repo models.NotificationChannelRepository
}

// NewNotificationChannelHandler creates a new NotificationChannelHandler.
func NewNotificationChannelHandler(repo models.NotificationChannelRepository) *NotificationChannelHandler {
	return &NotificationChannelHandler{repo: repo}
}

type createChannelRequest struct {
	Name       string `json:"name" binding:"required"`
	WebhookURL string `json:"webhook_url" binding:"required,url,startswith=https://"`
	Secret     string `json:"secret,omitempty"`
	Enabled    *bool  `json:"enabled"`
}

type updateChannelRequest struct {
	Name       string `json:"name,omitempty"`
	WebhookURL string  `json:"webhook_url,omitempty" binding:"omitempty,url,startswith=https://"`
	Secret     *string `json:"secret,omitempty"`
	Enabled    *bool   `json:"enabled,omitempty"`
}

type updateSubscriptionsRequest struct {
	EventTypes []string `json:"event_types"`
}

// notificationChannelWithCount is the list response DTO.
type notificationChannelWithCount struct {
	models.NotificationChannel
	SubscriptionCount int `json:"subscription_count"`
}

// ListChannels godoc
// @Summary     List all notification channels
// @Description Returns all notification channels with subscription counts
// @Tags        notification-channels
// @Produce     json
// @Success     200 {array}  notificationChannelWithCount
// @Failure     500 {object} map[string]string
// @Router      /api/v1/admin/notification-channels [get]
// @Security    BearerAuth
func (h *NotificationChannelHandler) ListChannels(c *gin.Context) {
	channels, err := h.repo.ListChannels(c.Request.Context())
	if err != nil {
		slog.Error("Failed to list notification channels", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	counts, err := h.repo.CountSubscriptionsByChannel(c.Request.Context())
	if err != nil {
		slog.Error("Failed to count subscriptions", "error", err)
		counts = make(map[string]int)
	}

	result := make([]notificationChannelWithCount, len(channels))
	for i, ch := range channels {
		result[i].NotificationChannel = ch
		result[i].SubscriptionCount = counts[ch.ID]
	}
	c.JSON(http.StatusOK, result)
}

// CreateChannel godoc
// @Summary     Create a notification channel
// @Description Creates a new notification channel
// @Tags        notification-channels
// @Accept      json
// @Produce     json
// @Param       channel body     createChannelRequest true "Channel"
// @Success     201     {object} models.NotificationChannel
// @Failure     400     {object} map[string]string
// @Failure     409     {object} map[string]string
// @Failure     500     {object} map[string]string
// @Router      /api/v1/admin/notification-channels [post]
// @Security    BearerAuth
func (h *NotificationChannelHandler) CreateChannel(c *gin.Context) {
	var req createChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.WebhookURL = strings.TrimSpace(req.WebhookURL)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Channel name is required"})
		return
	}

	now := time.Now().UTC()
	channel := models.NotificationChannel{
		ID:         uuid.New().String(),
		Name:       req.Name,
		WebhookURL: req.WebhookURL,
		Secret:     req.Secret,
		Enabled:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if req.Enabled != nil {
		channel.Enabled = *req.Enabled
	}

	if err := h.repo.CreateChannel(c.Request.Context(), &channel); err != nil {
		status, msg := mapError(err, entityNotificationChannel)
		c.JSON(status, gin.H{"error": msg})
		return
	}

	c.JSON(http.StatusCreated, channel)
}

// GetChannel godoc
// @Summary     Get a notification channel
// @Description Returns a notification channel by ID
// @Tags        notification-channels
// @Produce     json
// @Param       id path string true "Channel ID"
// @Success     200 {object} models.NotificationChannel
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/admin/notification-channels/{id} [get]
// @Security    BearerAuth
func (h *NotificationChannelHandler) GetChannel(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgChannelIDRequired})
		return
	}

	channel, err := h.repo.GetChannel(c.Request.Context(), id)
	if err != nil {
		status, msg := mapError(err, entityNotificationChannel)
		c.JSON(status, gin.H{"error": msg})
		return
	}

	c.JSON(http.StatusOK, channel)
}

// UpdateChannel godoc
// @Summary     Update a notification channel
// @Description Updates an existing notification channel
// @Tags        notification-channels
// @Accept      json
// @Produce     json
// @Param       id      path     string               true "Channel ID"
// @Param       channel body     updateChannelRequest  true "Channel updates"
// @Success     200     {object} models.NotificationChannel
// @Failure     400     {object} map[string]string
// @Failure     404     {object} map[string]string
// @Failure     409     {object} map[string]string
// @Failure     500     {object} map[string]string
// @Router      /api/v1/admin/notification-channels/{id} [put]
// @Security    BearerAuth
func (h *NotificationChannelHandler) UpdateChannel(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgChannelIDRequired})
		return
	}

	existing, err := h.repo.GetChannel(c.Request.Context(), id)
	if err != nil {
		status, msg := mapError(err, entityNotificationChannel)
		c.JSON(status, gin.H{"error": msg})
		return
	}

	var req updateChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.WebhookURL = strings.TrimSpace(req.WebhookURL)
	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.WebhookURL != "" {
		existing.WebhookURL = req.WebhookURL
	}
	if req.Secret != nil {
		existing.Secret = *req.Secret
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	existing.UpdatedAt = time.Now().UTC()

	if err := h.repo.UpdateChannel(c.Request.Context(), existing); err != nil {
		status, msg := mapError(err, entityNotificationChannel)
		c.JSON(status, gin.H{"error": msg})
		return
	}

	c.JSON(http.StatusOK, existing)
}

// DeleteChannel godoc
// @Summary     Delete a notification channel
// @Description Deletes a notification channel and its subscriptions
// @Tags        notification-channels
// @Param       id path string true "Channel ID"
// @Success     204
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/admin/notification-channels/{id} [delete]
// @Security    BearerAuth
func (h *NotificationChannelHandler) DeleteChannel(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgChannelIDRequired})
		return
	}

	if err := h.repo.DeleteChannel(c.Request.Context(), id); err != nil {
		status, msg := mapError(err, entityNotificationChannel)
		c.JSON(status, gin.H{"error": msg})
		return
	}

	c.Status(http.StatusNoContent)
}

// GetSubscriptions godoc
// @Summary     Get channel subscriptions
// @Description Returns event type subscriptions for a channel
// @Tags        notification-channels
// @Produce     json
// @Param       id path string true "Channel ID"
// @Success     200 {object} map[string]interface{}
// @Failure     400 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/admin/notification-channels/{id}/subscriptions [get]
// @Security    BearerAuth
func (h *NotificationChannelHandler) GetSubscriptions(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgChannelIDRequired})
		return
	}

	if _, err := h.repo.GetChannel(c.Request.Context(), id); err != nil {
		status, msg := mapError(err, entityNotificationChannel)
		c.JSON(status, gin.H{"error": msg})
		return
	}

	subs, err := h.repo.GetSubscriptions(c.Request.Context(), id)
	if err != nil {
		slog.Error("Failed to get subscriptions", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	eventTypes := make([]string, len(subs))
	for i, s := range subs {
		eventTypes[i] = s.EventType
	}

	c.JSON(http.StatusOK, gin.H{"event_types": eventTypes})
}

// UpdateSubscriptions godoc
// @Summary     Update channel subscriptions
// @Description Replaces all event type subscriptions for a channel
// @Tags        notification-channels
// @Accept      json
// @Produce     json
// @Param       id           path     string                    true "Channel ID"
// @Param       subscriptions body    updateSubscriptionsRequest true "Event types"
// @Success     200 {object} map[string]interface{}
// @Failure     400 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/admin/notification-channels/{id}/subscriptions [put]
// @Security    BearerAuth
func (h *NotificationChannelHandler) UpdateSubscriptions(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgChannelIDRequired})
		return
	}

	// Verify channel exists.
	if _, err := h.repo.GetChannel(c.Request.Context(), id); err != nil {
		status, msg := mapError(err, entityNotificationChannel)
		c.JSON(status, gin.H{"error": msg})
		return
	}

	var req updateSubscriptionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	// Deduplicate and validate event types.
	known := make(map[string]bool)
	for _, et := range notifier.AllEventTypes() {
		known[et] = true
	}
	seen := make(map[string]bool)
	unique := make([]string, 0, len(req.EventTypes))
	for _, et := range req.EventTypes {
		if !known[et] {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown event type: %s", et)})
			return
		}
		if !seen[et] {
			seen[et] = true
			unique = append(unique, et)
		}
	}

	if err := h.repo.SetSubscriptions(c.Request.Context(), id, unique); err != nil {
		status, msg := mapError(err, entityNotificationChannel)
		c.JSON(status, gin.H{"error": msg})
		return
	}

	c.JSON(http.StatusOK, gin.H{"event_types": unique})
}

// TestChannel godoc
// @Summary     Test a notification channel
// @Description Sends a test payload to the channel's webhook URL
// @Tags        notification-channels
// @Produce     json
// @Param       id path string true "Channel ID"
// @Success     200 {object} map[string]interface{}
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/admin/notification-channels/{id}/test [post]
// @Security    BearerAuth
func (h *NotificationChannelHandler) TestChannel(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgChannelIDRequired})
		return
	}

	channel, err := h.repo.GetChannel(c.Request.Context(), id)
	if err != nil {
		status, msg := mapError(err, entityNotificationChannel)
		c.JSON(status, gin.H{"error": msg})
		return
	}

	testPayload := notifChannel.EventPayload{
		EventType:       "test",
		Timestamp:       time.Now().UTC(),
		Title:           "Test notification",
		Message:         "This is a test notification from k8s-stack-manager.",
		UserDisplayName: "System",
		EntityType:      "notification_channel",
		EntityID:        channel.ID,
	}

	body, err := json.Marshal(testPayload)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, channel.WebhookURL, bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create test request"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-StackManager-Event", "test")
	if channel.Secret != "" {
		mac := hmac.New(sha256.New, []byte(channel.Secret))
		mac.Write(body)
		req.Header.Set("X-StackManager-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "Connection failed",
		})
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()

	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if success {
		msg = "Test notification sent successfully"
	}
	c.JSON(http.StatusOK, gin.H{
		"success": success,
		"message": msg,
	})
}

// ListDeliveryLogs godoc
// @Summary     List delivery logs for a channel
// @Description Returns paginated delivery logs for a notification channel
// @Tags        notification-channels
// @Produce     json
// @Param       id     path  string true  "Channel ID"
// @Param       limit  query int    false "Page size (default 20)"
// @Param       offset query int    false "Offset (default 0)"
// @Success     200 {object} map[string]interface{}
// @Failure     400 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Router      /api/v1/admin/notification-channels/{id}/delivery-logs [get]
// @Security    BearerAuth
func (h *NotificationChannelHandler) ListDeliveryLogs(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgChannelIDRequired})
		return
	}

	if _, err := h.repo.GetChannel(c.Request.Context(), id); err != nil {
		status, msg := mapError(err, entityNotificationChannel)
		c.JSON(status, gin.H{"error": msg})
		return
	}

	limit := 20
	offset := 0
	if l := c.Query("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 100 {
		limit = 100
	}
	if o := c.Query("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	logs, total, err := h.repo.ListDeliveryLogs(c.Request.Context(), id, limit, offset)
	if err != nil {
		slog.Error("Failed to list delivery logs", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"logs":   logs,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// ListEventTypes godoc
// @Summary     List all event types
// @Description Returns all known event types that channels can subscribe to
// @Tags        notification-channels
// @Produce     json
// @Success     200 {object} map[string]interface{}
// @Router      /api/v1/admin/notification-channels/event-types [get]
// @Security    BearerAuth
func (h *NotificationChannelHandler) ListEventTypes(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"event_types": notifier.AllEventTypes()})
}
