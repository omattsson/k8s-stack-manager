package azure

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"
)

const tableInstanceQuotaOverrides = "InstanceQuotaOverrides"


const instanceQuotaRowKey = "quota"

// InstanceQuotaOverrideRepository implements models.InstanceQuotaOverrideRepository for Azure Table Storage.
// Partition key: StackInstanceID, Row key: "quota" (fixed, one override per instance).
type InstanceQuotaOverrideRepository struct {
	client    AzureTableClient
	tableName string
}

// NewInstanceQuotaOverrideRepository creates a new Azure Table Storage instance quota override repository.
func NewInstanceQuotaOverrideRepository(accountName, accountKey, endpoint string, useAzurite bool) (*InstanceQuotaOverrideRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, tableInstanceQuotaOverrides, useAzurite)
	if err != nil {
		return nil, err
	}
	return &InstanceQuotaOverrideRepository{client: client, tableName: tableInstanceQuotaOverrides}, nil
}

// NewTestInstanceQuotaOverrideRepository creates a repository for unit testing.
func NewTestInstanceQuotaOverrideRepository() *InstanceQuotaOverrideRepository {
	return &InstanceQuotaOverrideRepository{tableName: tableInstanceQuotaOverrides}
}

// SetTestClient injects a mock client for testing.
func (r *InstanceQuotaOverrideRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// instanceQuotaOverrideEntity maps to the Azure Table JSON representation.
type instanceQuotaOverrideEntity struct {
	PartitionKey    string `json:"PartitionKey"`
	RowKey          string `json:"RowKey"`
	ID              string `json:"ID"`
	StackInstanceID string `json:"StackInstanceID"`
	CPURequest      string `json:"CPURequest"`
	CPULimit        string `json:"CPULimit"`
	MemoryRequest   string `json:"MemoryRequest"`
	MemoryLimit     string `json:"MemoryLimit"`
	StorageLimit    string `json:"StorageLimit"`
	PodLimit        *int   `json:"PodLimit,omitempty"`
	CreatedAt       string `json:"CreatedAt"`
	UpdatedAt       string `json:"UpdatedAt"`
}

func (e *instanceQuotaOverrideEntity) toModel() *models.InstanceQuotaOverride {
	o := &models.InstanceQuotaOverride{
		ID:              e.ID,
		StackInstanceID: e.StackInstanceID,
		CPURequest:      e.CPURequest,
		CPULimit:        e.CPULimit,
		MemoryRequest:   e.MemoryRequest,
		MemoryLimit:     e.MemoryLimit,
		StorageLimit:    e.StorageLimit,
		PodLimit:        e.PodLimit,
	}
	o.CreatedAt, _ = time.Parse(time.RFC3339, e.CreatedAt)
	o.UpdatedAt, _ = time.Parse(time.RFC3339, e.UpdatedAt)
	return o
}

// GetByInstanceID returns the quota override for the given stack instance.
func (r *InstanceQuotaOverrideRepository) GetByInstanceID(ctx context.Context, instanceID string) (*models.InstanceQuotaOverride, error) {
	resp, err := r.client.GetEntity(ctx, instanceID, instanceQuotaRowKey, nil)
	if err != nil {
		return nil, mapAzureError("get_by_instance_id", err)
	}

	var entity instanceQuotaOverrideEntity
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError(opUnmarshal, err)
	}

	return entity.toModel(), nil
}

// Upsert creates or updates the quota override for a stack instance.
func (r *InstanceQuotaOverrideRepository) Upsert(ctx context.Context, override *models.InstanceQuotaOverride) error {
	now := time.Now().UTC()
	override.UpdatedAt = now

	// Try to get existing entity.
	_, err := r.client.GetEntity(ctx, override.StackInstanceID, instanceQuotaRowKey, nil)
	if err != nil {
		azErr := mapAzureError("upsert_check", err)
		if !errors.Is(azErr, dberrors.ErrNotFound) {
			return azErr
		}

		// Not found — create new entity.
		if override.ID == "" {
			override.ID = newID()
		}
		override.CreatedAt = now

		entity := instanceQuotaOverrideToEntity(override)
		entityBytes, marshalErr := json.Marshal(entity)
		if marshalErr != nil {
			return dberrors.NewDatabaseError(opMarshal, marshalErr)
		}

		_, addErr := r.client.AddEntity(ctx, entityBytes, nil)
		if addErr != nil {
			return mapAzureError(opCreate, addErr)
		}
		return nil
	}

	// Exists — update.
	entity := instanceQuotaOverrideToEntity(override)
	entityBytes, err := json.Marshal(entity)
	if err != nil {
		return dberrors.NewDatabaseError(opMarshal, err)
	}

	_, err = r.client.UpdateEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError(opUpdate, err)
	}
	return nil
}

// Delete removes the quota override for the given stack instance.
func (r *InstanceQuotaOverrideRepository) Delete(ctx context.Context, instanceID string) error {
	_, err := r.client.DeleteEntity(ctx, instanceID, instanceQuotaRowKey, nil)
	if err != nil {
		return mapAzureError(opDelete, err)
	}
	return nil
}

func instanceQuotaOverrideToEntity(o *models.InstanceQuotaOverride) map[string]interface{} {
	entity := map[string]interface{}{
		"PartitionKey":    o.StackInstanceID,
		"RowKey":          instanceQuotaRowKey,
		"ID":              o.ID,
		"StackInstanceID": o.StackInstanceID,
		"CPURequest":      o.CPURequest,
		"CPULimit":        o.CPULimit,
		"MemoryRequest":   o.MemoryRequest,
		"MemoryLimit":     o.MemoryLimit,
		"StorageLimit":    o.StorageLimit,
		"CreatedAt":       o.CreatedAt.Format(time.RFC3339),
		"UpdatedAt":       o.UpdatedAt.Format(time.RFC3339),
	}
	if o.PodLimit != nil {
		entity["PodLimit"] = *o.PodLimit
	}
	return entity
}

