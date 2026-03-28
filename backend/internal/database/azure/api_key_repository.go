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

// apiKeyEntity is the typed Azure Table entity for API keys.
type apiKeyEntity struct {
	PartitionKey string `json:"PartitionKey"`
	RowKey       string `json:"RowKey"`
	ID           string `json:"ID"`
	UserID       string `json:"UserID"`
	Name         string `json:"Name"`
	KeyHash      string `json:"KeyHash"`
	Prefix       string `json:"Prefix"`
	LastUsedAt   string `json:"LastUsedAt,omitempty"`
	ExpiresAt    string `json:"ExpiresAt,omitempty"`
	CreatedAt    string `json:"CreatedAt"`
}

func (e *apiKeyEntity) toModel() *models.APIKey {
	key := &models.APIKey{
		ID:      e.ID,
		UserID:  e.UserID,
		Name:    e.Name,
		KeyHash: e.KeyHash,
		Prefix:  e.Prefix,
	}
	key.CreatedAt, _ = time.Parse(time.RFC3339, e.CreatedAt)
	if e.LastUsedAt != "" {
		t, err := time.Parse(time.RFC3339, e.LastUsedAt)
		if err == nil {
			key.LastUsedAt = &t
		}
	}
	if e.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, e.ExpiresAt)
		if err == nil {
			key.ExpiresAt = &t
		}
	}
	return key
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

	var entity apiKeyEntity
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError("unmarshal", err)
	}

	return entity.toModel(), nil
}

// FindByPrefix scans the entire table and returns all keys matching prefix.
// Azure Table Storage has no secondary index, so client-side filtering is used.
// This is acceptable given the expected low total volume of API keys.
func (r *APIKeyRepository) FindByPrefix(prefix string) ([]*models.APIKey, error) {
	ctx := context.Background()

	pager := r.client.NewListEntitiesPager(nil)
	entities, err := collectEntitiesTyped[apiKeyEntity](ctx, pager, func(e *apiKeyEntity) bool {
		return e.Prefix == prefix
	}, 0)
	if err != nil {
		return nil, mapAzureError("find_by_prefix", err)
	}
	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("find_by_prefix", dberrors.ErrNotFound)
	}

	keys := make([]*models.APIKey, 0, len(entities))
	for _, e := range entities {
		keys = append(keys, e.toModel())
	}
	return keys, nil
}

func (r *APIKeyRepository) ListByUser(userID string) ([]*models.APIKey, error) {
	ctx := context.Background()

	filter := "PartitionKey eq '" + userID + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[apiKeyEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError("list_by_user", err)
	}

	keys := make([]*models.APIKey, 0, len(entities))
	for _, e := range entities {
		keys = append(keys, e.toModel())
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

	// For the read-modify-write of UpdateLastUsed, we deserialize into the typed
	// entity struct, update the field, and re-marshal for the write.
	var entity apiKeyEntity
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return dberrors.NewDatabaseError("unmarshal", err)
	}

	entity.LastUsedAt = t.UTC().Format(time.RFC3339)

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
