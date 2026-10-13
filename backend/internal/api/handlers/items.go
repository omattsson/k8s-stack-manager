package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"backend/internal/database"
	"backend/internal/models"
	"backend/internal/websocket"

	"github.com/gin-gonic/gin"
)

// Items handler message constants.
const (
	msgInvalidIDFormat = "Invalid ID format"
)

func parseUintParam(s string) (uint, error) {
	id, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if id < 0 {
		return 0, strconv.ErrRange
	}
	return uint(id), nil
}

// Handler is the reference implementation of the CRUD handler pattern
// (Items): repository injection, handleDBError, optional WebSocket
// broadcast. No route registers these handlers: unauthenticated item routes
// let any client change data (GitHub issue #534). The handler tests call them
// on their own router.
type Handler struct {
	repository models.Repository
	hub        websocket.BroadcastSender
}

func NewHandler(repository models.Repository) *Handler {
	return &Handler{repository: repository}
}

func NewHandlerWithHub(repository models.Repository, hub websocket.BroadcastSender) *Handler {
	return &Handler{repository: repository, hub: hub}
}

func (h *Handler) broadcast(msgType string, payload interface{}) {
	if h.hub == nil {
		return
	}
	msg, err := websocket.NewMessage(msgType, payload)
	if err != nil {
		slog.Error("Failed to create WebSocket message", "type", msgType, "error", err)
		return
	}
	b, err := msg.Bytes()
	if err != nil {
		slog.Error("Failed to serialise WebSocket message", "type", msgType, "error", err)
		return
	}
	h.hub.Broadcast(b)
}

func handleDBError(err error) (int, string) {
	if err == nil {
		return http.StatusOK, ""
	}

	var dbErr *database.DatabaseError
	if errors.As(err, &dbErr) {
		if errors.Is(dbErr.Err, database.ErrValidation) {
			return http.StatusBadRequest, dbErr.Error()
		}
		if errors.Is(dbErr.Err, database.ErrNotFound) {
			return http.StatusNotFound, "Item not found"
		}
		if errors.Is(dbErr.Err, database.ErrDuplicateKey) {
			return http.StatusConflict, "Item already exists"
		}
		return http.StatusInternalServerError, msgInternalServerError
	}

	if strings.Contains(err.Error(), "not found") {
		return http.StatusNotFound, "Item not found"
	}
	// Never leak raw error messages to clients
	return http.StatusInternalServerError, msgInternalServerError
}

// CreateItem creates an item.
func (h *Handler) CreateItem(c *gin.Context) {
	var item models.Item
	if err := c.ShouldBindJSON(&item); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	if item.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Name is required"})
		return
	}

	// Version is server-managed; force initial value regardless of client input.
	item.Version = 1

	if err := h.repository.Create(c.Request.Context(), &item); err != nil {
		status, message := handleDBError(err)
		c.JSON(status, gin.H{"error": message})
		return
	}

	h.broadcast("item.created", item)
	c.JSON(http.StatusCreated, item)
}

// GetItems lists the items (filter, sort and pagination query parameters).
func (h *Handler) GetItems(c *gin.Context) {
	// Parse query parameters
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	minPrice, _ := strconv.ParseFloat(c.Query("min_price"), 64)
	maxPrice, _ := strconv.ParseFloat(c.Query("max_price"), 64)

	// Validate parameters
	if c.Query("limit") != "" && limit <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid limit parameter"})
		return
	}
	if c.Query("offset") != "" && offset < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid offset parameter"})
		return
	}

	var items []models.Item
	conditions := make([]interface{}, 0)

	// Handle name filtering
	if c.Query("name_exact") != "" {
		conditions = append(conditions, models.Filter{
			Field: "name",
			Op:    "exact",
			Value: c.Query("name_exact"),
		})
	} else if name := c.Query("name"); name != "" {
		conditions = append(conditions, models.Filter{
			Field: "name",
			Value: name,
		})
	}
	if minPrice > 0 {
		conditions = append(conditions, models.Filter{Field: "price", Op: ">=", Value: minPrice})
	}
	if maxPrice > 0 {
		conditions = append(conditions, models.Filter{Field: "price", Op: "<=", Value: maxPrice})
	}
	if limit > 0 {
		conditions = append(conditions, models.Pagination{Limit: limit, Offset: offset})
	}

	if err := h.repository.List(c.Request.Context(), &items, conditions...); err != nil {
		status, message := handleDBError(err)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, items)
}

// GetItem returns one item by ID.
func (h *Handler) GetItem(c *gin.Context) {
	id, err := parseUintParam(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidIDFormat})
		return
	}

	var item models.Item
	if err := h.repository.FindByID(c.Request.Context(), id, &item); err != nil {
		status, message := handleDBError(err)
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, item)
}

// UpdateItem updates an item (optimistic locking with the version field).
func (h *Handler) UpdateItem(c *gin.Context) {
	id, err := parseUintParam(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidIDFormat})
		return
	}

	// Get the current version from the database
	var currentItem models.Item
	if err := h.repository.FindByID(c.Request.Context(), id, &currentItem); err != nil {
		status, message := handleDBError(err)
		c.JSON(status, gin.H{"error": message})
		return
	}

	var updateItem models.Item
	if err := c.ShouldBindJSON(&updateItem); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	// Update fields from request
	currentItem.Name = updateItem.Name
	currentItem.Price = updateItem.Price

	// Optimistic locking: if the client provided a version, use it so the
	// repository can detect conflicts. If version=0 (not provided), the
	// repository uses the version we just read — this still detects conflicts
	// that occur between our FindByID and the repository's WHERE-version check,
	// but the client must send the version to guarantee end-to-end safety.
	if updateItem.Version > 0 {
		currentItem.Version = updateItem.Version
	}

	if err := h.repository.Update(c.Request.Context(), &currentItem); err != nil {
		if strings.Contains(err.Error(), "version mismatch") {
			c.JSON(http.StatusConflict, gin.H{"error": "Item has been modified by another request"})
			return
		}
		status, message := handleDBError(err)
		c.JSON(status, gin.H{"error": message})
		return
	}

	h.broadcast("item.updated", currentItem)
	c.JSON(http.StatusOK, currentItem)
}

// DeleteItem deletes an item.
func (h *Handler) DeleteItem(c *gin.Context) {
	id, err := parseUintParam(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidIDFormat})
		return
	}

	// Delete directly — the repository returns ErrNotFound if the item doesn't exist.
	// This avoids a race condition between a FindByID check and the actual delete.
	item := &models.Item{Base: models.Base{ID: id}}
	if err := h.repository.Delete(c.Request.Context(), item); err != nil {
		status, message := handleDBError(err)
		c.JSON(status, gin.H{"error": message})
		return
	}

	h.broadcast("item.deleted", gin.H{"id": id})
	c.Status(http.StatusNoContent)
}
