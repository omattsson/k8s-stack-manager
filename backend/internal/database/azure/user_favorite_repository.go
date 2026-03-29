package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

const tableUserFavorites = "UserFavorites"


// UserFavoriteRepository implements models.UserFavoriteRepository for Azure Table Storage.
// Partition key: UserID, Row key: "entityType:entityID" (composite for uniqueness).
type UserFavoriteRepository struct {
	client    AzureTableClient
	tableName string
}

// NewUserFavoriteRepository creates a new Azure Table Storage user favorite repository.
func NewUserFavoriteRepository(accountName, accountKey, endpoint string, useAzurite bool) (*UserFavoriteRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, tableUserFavorites, useAzurite)
	if err != nil {
		return nil, err
	}
	return &UserFavoriteRepository{client: client, tableName: tableUserFavorites}, nil
}

// NewTestUserFavoriteRepository creates a repository for unit testing without connecting.
func NewTestUserFavoriteRepository() *UserFavoriteRepository {
	return &UserFavoriteRepository{tableName: tableUserFavorites}
}

// SetTestClient injects a mock client for testing.
func (r *UserFavoriteRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// favoriteRowKey builds the composite row key from entity type and entity ID.
func favoriteRowKey(entityType, entityID string) string {
	return entityType + ":" + entityID
}

func (r *UserFavoriteRepository) Add(fav *models.UserFavorite) error {
	ctx := context.Background()

	if err := fav.Validate(); err != nil {
		return dberrors.NewDatabaseError("add", fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
	}

	now := time.Now().UTC()
	if fav.ID == "" {
		fav.ID = newID()
	}
	fav.CreatedAt = now

	rk := favoriteRowKey(fav.EntityType, fav.EntityID)

	entity := map[string]interface{}{
		"PartitionKey": fav.UserID,
		"RowKey":       rk,
		"ID":           fav.ID,
		"UserID":       fav.UserID,
		"EntityType":   fav.EntityType,
		"EntityID":     fav.EntityID,
		"CreatedAt":    now.Format(time.RFC3339),
	}

	entityBytes, err := json.Marshal(entity)
	if err != nil {
		return dberrors.NewDatabaseError(opMarshal, err)
	}

	// Idempotent upsert — don't error on duplicate.
	_, err = r.client.UpsertEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError("add", err)
	}
	return nil
}

func (r *UserFavoriteRepository) Remove(userID, entityType, entityID string) error {
	ctx := context.Background()

	rk := favoriteRowKey(entityType, entityID)
	_, err := r.client.DeleteEntity(ctx, userID, rk, nil)
	if err != nil {
		return mapAzureError("remove", err)
	}
	return nil
}

func (r *UserFavoriteRepository) List(userID string) ([]*models.UserFavorite, error) {
	ctx := context.Background()

	filter := "PartitionKey eq '" + escapeODataString(userID) + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[userFavoriteEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError(opList, err)
	}

	favorites := make([]*models.UserFavorite, 0, len(entities))
	for _, e := range entities {
		favorites = append(favorites, e.toModel())
	}
	return favorites, nil
}

// userFavoriteEntity maps to the Azure Table JSON representation.
type userFavoriteEntity struct {
	PartitionKey string `json:"PartitionKey"`
	RowKey       string `json:"RowKey"`
	ID           string `json:"ID"`
	UserID       string `json:"UserID"`
	EntityType   string `json:"EntityType"`
	EntityID     string `json:"EntityID"`
	CreatedAt    string `json:"CreatedAt"`
}

func (e *userFavoriteEntity) toModel() *models.UserFavorite {
	fav := &models.UserFavorite{
		ID:         e.ID,
		UserID:     e.UserID,
		EntityType: e.EntityType,
		EntityID:   e.EntityID,
		CreatedAt:  func() time.Time { t, _ := time.Parse(time.RFC3339, e.CreatedAt); return t }(),
	}
	// Fall back to parsing the RowKey if EntityType is empty.
	if fav.EntityType == "" {
		if idx := strings.Index(e.RowKey, ":"); idx >= 0 {
			fav.EntityType = e.RowKey[:idx]
		}
	}
	return fav
}

func (r *UserFavoriteRepository) IsFavorite(userID, entityType, entityID string) (bool, error) {
	ctx := context.Background()

	rk := favoriteRowKey(entityType, entityID)
	_, err := r.client.GetEntity(ctx, userID, rk, nil)
	if err != nil {
		azErr := mapAzureError("is_favorite", err)
		if errors.Is(azErr, dberrors.ErrNotFound) {
			return false, nil
		}
		return false, azErr
	}
	return true, nil
}

