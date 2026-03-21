package models

import (
	"errors"
	"time"
)

// UserFavorite represents a user's favorited entity (definition, instance, or template).
type UserFavorite struct {
	CreatedAt  time.Time `json:"created_at"`
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	EntityType string    `json:"entity_type"`
	EntityID   string    `json:"entity_id"`
}

// Valid entity types for favorites.
var validFavoriteEntityTypes = map[string]bool{
	"definition": true,
	"instance":   true,
	"template":   true,
}

// ValidateFavoriteEntityType checks whether the entity type is one of the allowed values.
func ValidateFavoriteEntityType(entityType string) bool {
	return validFavoriteEntityTypes[entityType]
}

// Validate implements the Validator interface for UserFavorite.
func (f *UserFavorite) Validate() error {
	if f.UserID == "" {
		return errors.New("user_id is required")
	}
	if f.EntityType == "" {
		return errors.New("entity_type is required")
	}
	if !ValidateFavoriteEntityType(f.EntityType) {
		return errors.New("entity_type must be one of: definition, instance, template")
	}
	if f.EntityID == "" {
		return errors.New("entity_id is required")
	}
	return nil
}

// UserFavoriteRepository defines data access operations for user favorites.
type UserFavoriteRepository interface {
	List(userID string) ([]*UserFavorite, error)
	Add(fav *UserFavorite) error
	Remove(userID, entityType, entityID string) error
	IsFavorite(userID, entityType, entityID string) (bool, error)
}
