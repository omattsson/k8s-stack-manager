package handlers

import (
	"log/slog"
	"net/http"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
)

// Favorites handler message constants.
const (
	msgInvalidEntityType = "entity_type must be one of: definition, instance, template"
)

const logKeyFavUserID = "user_id"



// FavoriteHandler handles user favorites endpoints.
type FavoriteHandler struct {
	favoriteRepo models.UserFavoriteRepository
}

// NewFavoriteHandler creates a new FavoriteHandler.
func NewFavoriteHandler(favoriteRepo models.UserFavoriteRepository) *FavoriteHandler {
	return &FavoriteHandler{favoriteRepo: favoriteRepo}
}

// addFavoriteRequest is the request body for adding a favorite.
type addFavoriteRequest struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
}

// ListFavorites godoc
// @Summary      List favorites for the authenticated user
// @Description  Returns all favorited entities for the current user
// @Tags         favorites
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   models.UserFavorite
// @Failure      401  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/favorites [get]
func (h *FavoriteHandler) ListFavorites(c *gin.Context) {
	userID := middleware.GetUserIDFromContext(c)

	favorites, err := h.favoriteRepo.List(userID)
	if err != nil {
		slog.Error("Failed to list favorites", "error", err, logKeyFavUserID, userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	c.JSON(http.StatusOK, favorites)
}

// AddFavorite godoc
// @Summary      Add a favorite
// @Description  Add an entity to the user's favorites (idempotent)
// @Tags         favorites
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        favorite body     addFavoriteRequest true "Favorite entity reference"
// @Success      201      {object} models.UserFavorite
// @Failure      400      {object} map[string]string
// @Failure      401      {object} map[string]string
// @Failure      500      {object} map[string]string
// @Router       /api/v1/favorites [post]
func (h *FavoriteHandler) AddFavorite(c *gin.Context) {
	userID := middleware.GetUserIDFromContext(c)

	var req addFavoriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidRequestFormat})
		return
	}

	if req.EntityType == "" || req.EntityID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "entity_type and entity_id are required"})
		return
	}

	if !models.ValidateFavoriteEntityType(req.EntityType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidEntityType})
		return
	}

	fav := &models.UserFavorite{
		UserID:     userID,
		EntityType: req.EntityType,
		EntityID:   req.EntityID,
	}

	if err := h.favoriteRepo.Add(fav); err != nil {
		status, message := mapError(err, "Favorite")
		if status >= http.StatusInternalServerError {
			slog.Error("Failed to add favorite", "error", err, logKeyFavUserID, userID)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusCreated, fav)
}

// RemoveFavorite godoc
// @Summary      Remove a favorite
// @Description  Remove an entity from the user's favorites
// @Tags         favorites
// @Produce      json
// @Security     BearerAuth
// @Param        entityType path string true "Entity type (definition, instance, template)"
// @Param        entityId   path string true "Entity ID"
// @Success      204  "Favorite removed"
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/favorites/{entityType}/{entityId} [delete]
func (h *FavoriteHandler) RemoveFavorite(c *gin.Context) {
	userID := middleware.GetUserIDFromContext(c)
	entityType := c.Param("entityType")
	entityID := c.Param("entityId")

	if !models.ValidateFavoriteEntityType(entityType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidEntityType})
		return
	}

	if entityID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "entity_id is required"})
		return
	}

	if err := h.favoriteRepo.Remove(userID, entityType, entityID); err != nil {
		status, message := mapError(err, "Favorite")
		if status >= http.StatusInternalServerError {
			slog.Error("Failed to remove favorite", "error", err, logKeyFavUserID, userID)
		}
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Status(http.StatusNoContent)
}

// CheckFavorite godoc
// @Summary      Check if an entity is favorited
// @Description  Check whether the authenticated user has favorited a specific entity
// @Tags         favorites
// @Produce      json
// @Security     BearerAuth
// @Param        entity_type query string true "Entity type (definition, instance, template)"
// @Param        entity_id   query string true "Entity ID"
// @Success      200  {object}  map[string]bool
// @Failure      400  {object}  map[string]string
// @Failure      401  {object}  map[string]string
// @Failure      500  {object}  map[string]string
// @Router       /api/v1/favorites/check [get]
func (h *FavoriteHandler) CheckFavorite(c *gin.Context) {
	userID := middleware.GetUserIDFromContext(c)
	entityType := c.Query("entity_type")
	entityID := c.Query("entity_id")

	if entityType == "" || entityID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "entity_type and entity_id query parameters are required"})
		return
	}

	if !models.ValidateFavoriteEntityType(entityType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": msgInvalidEntityType})
		return
	}

	isFav, err := h.favoriteRepo.IsFavorite(userID, entityType, entityID)
	if err != nil {
		slog.Error("Failed to check favorite", "error", err, logKeyFavUserID, userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msgInternalServerError})
		return
	}

	c.JSON(http.StatusOK, gin.H{"is_favorite": isFav})
}
