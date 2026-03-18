package azure

import (
	"context"
	"encoding/json"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// APIKeyRepository implements models.APIKeyRepository for Azure Table Storage.
// Partition key: UserID, Row key: key ID.
type APIKeyRepository struct {
	client    AzureTableClient
	tableName string
}

// NewAPIKeyRepository creates a new Azure Table Storage API key repository.
func NewAPIKeyRepository(accountName, accountKey, endpoint string, useAzurite bool) (*APIKeyRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "APIKeys", useAzurite)
	if err != nil {
		return nil, err
	}
	return &APIKeyRepository{client: client, tableName: "APIKeys"}, nil
}

// NewTestAPIKeyRepository creates an APIKeyRepository for unit testing without connecting.
func NewTestAPIKeyRepository() *APIKeyRepository {
	return &APIKeyRepository{tableName: "APIKeys"}
}

// SetTestClient injects a mock client for testing.
func (r *APIKeyRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

func (r *APIKeyRepository) Create(key *models.APIKey) error {
	ctx := context.Background()
	now := time.Now().UTC()

	if key.ID == "" {
		key.ID = newID()
	}
	if key.UserID == "" || key.Name == "" {
		return dberrors.NewDatabaseError("create", dberrors.ErrValidation)
	}

	key.CreatedAt = now

	entity := map[string]interface{}{
		"PartitionKey": key.UserID,
		"RowKey":       key.ID,
		"ID":           key.ID,
		"UserID":       key.UserID,
		"Name":         key.Name,
		"KeyHash":      key.KeyHash,
		"Prefix":       key.Prefix,
		"CreatedAt":    now.Format(time.RFC3339),
	}
	if key.ExpiresAt != nil {
		entity["ExpiresAt"] = key.ExpiresAt.Format(time.RFC3339)
	}

	entityBytes, err := json.Marshal(entity)
	if err != nil {
		return dberrors.NewDatabaseError("marshal", err)
	}

	_, err = r.client.AddEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError("create", err)
	}
	return nil
}

func (r *APIKeyRepository) FindByID(userID, keyID string) (*models.APIKey, error) {
	ctx := context.Background()

	resp, err := r.client.GetEntity(ctx, userID, keyID, nil)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}

	var entity map[string]interface{}
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError("unmarshal", err)
	}

	return apiKeyFromEntity(entity), nil
}

// FindByPrefix scans the entire table and returns the first key matching prefix.
// Azure Table Storage has no secondary index, so client-side filtering is used.
// This is acceptable given the expected low total volume of API keys.
func (r *APIKeyRepository) FindByPrefix(prefix string) (*models.APIKey, error) {
	ctx := context.Background()

	pager := r.client.NewListEntitiesPager(nil)
	entities, err := collectEntities(ctx, pager, func(e map[string]interface{}) bool {
		return getString(e, "Prefix") == prefix
	})
	if err != nil {
		return nil, mapAzureError("find_by_prefix", err)
	}
	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("find_by_prefix", dberrors.ErrNotFound)
	}

	return apiKeyFromEntity(entities[0]), nil
}

func (r *APIKeyRepository) ListByUser(userID string) ([]*models.APIKey, error) {
	ctx := context.Background()

	filter := "PartitionKey eq '" + userID + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("list_by_user", err)
	}

	keys := make([]*models.APIKey, 0, len(entities))
	for _, e := range entities {
		keys = append(keys, apiKeyFromEntity(e))
	}
	return keys, nil
}

func (r *APIKeyRepository) UpdateLastUsed(userID, keyID string, t time.Time) error {
	ctx := context.Background()

	// Read current entity to preserve all existing fields.
	resp, err := r.client.GetEntity(ctx, userID, keyID, nil)
	if err != nil {
		return mapAzureError("update_last_used_read", err)
	}

	var entity map[string]interface{}
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return dberrors.NewDatabaseError("unmarshal", err)
	}

	entity["LastUsedAt"] = t.UTC().Format(time.RFC3339)

	entityBytes, err := json.Marshal(entity)
	if err != nil {
		return dberrors.NewDatabaseError("marshal", err)
	}

	_, err = r.client.UpdateEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError("update_last_used", err)
	}
	return nil
}

func (r *APIKeyRepository) Delete(userID, keyID string) error {
	ctx := context.Background()

	_, err := r.client.DeleteEntity(ctx, userID, keyID, nil)
	if err != nil {
		return mapAzureError("delete", err)
	}
	return nil
}

func apiKeyFromEntity(e map[string]interface{}) *models.APIKey {
	key := &models.APIKey{
		ID:        getString(e, "ID"),
		UserID:    getString(e, "UserID"),
		Name:      getString(e, "Name"),
		KeyHash:   getString(e, "KeyHash"),
		Prefix:    getString(e, "Prefix"),
		CreatedAt: parseTime(e, "CreatedAt"),
	}
	if getString(e, "LastUsedAt") != "" {
		t := parseTime(e, "LastUsedAt")
		key.LastUsedAt = &t
	}
	if getString(e, "ExpiresAt") != "" {
		t := parseTime(e, "ExpiresAt")
		key.ExpiresAt = &t
	}
	return key
}
