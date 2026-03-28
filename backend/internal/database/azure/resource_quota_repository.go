package azure

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"
)

const quotaRowKey = "quota"

// ResourceQuotaRepository implements models.ResourceQuotaRepository for Azure Table Storage.
// Partition key: ClusterID, Row key: "quota" (fixed, one config per cluster).
type ResourceQuotaRepository struct {
	client    AzureTableClient
	tableName string
}

// NewResourceQuotaRepository creates a new Azure Table Storage resource quota repository.
func NewResourceQuotaRepository(accountName, accountKey, endpoint string, useAzurite bool) (*ResourceQuotaRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "ResourceQuotaConfigs", useAzurite)
	if err != nil {
		return nil, err
	}
	return &ResourceQuotaRepository{client: client, tableName: "ResourceQuotaConfigs"}, nil
}

// NewTestResourceQuotaRepository creates a repository for unit testing.
func NewTestResourceQuotaRepository() *ResourceQuotaRepository {
	return &ResourceQuotaRepository{tableName: "ResourceQuotaConfigs"}
}

// SetTestClient injects a mock client for testing.
func (r *ResourceQuotaRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// resourceQuotaEntity maps to the Azure Table JSON representation.
type resourceQuotaEntity struct {
	PartitionKey  string `json:"PartitionKey"`
	RowKey        string `json:"RowKey"`
	ID            string `json:"ID"`
	ClusterID     string `json:"ClusterID"`
	CPURequest    string `json:"CPURequest"`
	CPULimit      string `json:"CPULimit"`
	MemoryRequest string `json:"MemoryRequest"`
	MemoryLimit   string `json:"MemoryLimit"`
	StorageLimit  string `json:"StorageLimit"`
	PodLimit      int    `json:"PodLimit"`
	CreatedAt     string `json:"CreatedAt"`
	UpdatedAt     string `json:"UpdatedAt"`
}

func (e *resourceQuotaEntity) toModel() *models.ResourceQuotaConfig {
	q := &models.ResourceQuotaConfig{
		ID:            e.ID,
		ClusterID:     e.ClusterID,
		CPURequest:    e.CPURequest,
		CPULimit:      e.CPULimit,
		MemoryRequest: e.MemoryRequest,
		MemoryLimit:   e.MemoryLimit,
		StorageLimit:  e.StorageLimit,
		PodLimit:      e.PodLimit,
	}
	q.CreatedAt, _ = time.Parse(time.RFC3339, e.CreatedAt)
	q.UpdatedAt, _ = time.Parse(time.RFC3339, e.UpdatedAt)
	return q
}

// GetByClusterID returns the resource quota config for the given cluster.
func (r *ResourceQuotaRepository) GetByClusterID(ctx context.Context, clusterID string) (*models.ResourceQuotaConfig, error) {
	resp, err := r.client.GetEntity(ctx, clusterID, quotaRowKey, nil)
	if err != nil {
		return nil, mapAzureError("get_by_cluster_id", err)
	}

	var entity resourceQuotaEntity
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError("unmarshal", err)
	}

	return entity.toModel(), nil
}

// Upsert creates or updates the resource quota config for a cluster.
func (r *ResourceQuotaRepository) Upsert(ctx context.Context, config *models.ResourceQuotaConfig) error {
	now := time.Now().UTC()
	config.UpdatedAt = now

	// Try to get existing entity.
	_, err := r.client.GetEntity(ctx, config.ClusterID, quotaRowKey, nil)
	if err != nil {
		azErr := mapAzureError("upsert_check", err)
		if !errors.Is(azErr, dberrors.ErrNotFound) {
			return azErr
		}

		// Not found — create new entity.
		if config.ID == "" {
			config.ID = newID()
		}
		config.CreatedAt = now

		entity := resourceQuotaToEntity(config)
		entityBytes, marshalErr := json.Marshal(entity)
		if marshalErr != nil {
			return dberrors.NewDatabaseError("marshal", marshalErr)
		}

		_, addErr := r.client.AddEntity(ctx, entityBytes, nil)
		if addErr != nil {
			return mapAzureError("create", addErr)
		}
		return nil
	}

	// Exists — update.
	entity := resourceQuotaToEntity(config)
	entityBytes, err := json.Marshal(entity)
	if err != nil {
		return dberrors.NewDatabaseError("marshal", err)
	}

	_, err = r.client.UpdateEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError("update", err)
	}
	return nil
}

// Delete removes the resource quota config for the given cluster.
func (r *ResourceQuotaRepository) Delete(ctx context.Context, clusterID string) error {
	_, err := r.client.DeleteEntity(ctx, clusterID, quotaRowKey, nil)
	if err != nil {
		return mapAzureError("delete", err)
	}
	return nil
}

func resourceQuotaToEntity(config *models.ResourceQuotaConfig) map[string]interface{} {
	return map[string]interface{}{
		"PartitionKey":  config.ClusterID,
		"RowKey":        quotaRowKey,
		"ID":            config.ID,
		"ClusterID":     config.ClusterID,
		"CPURequest":    config.CPURequest,
		"CPULimit":      config.CPULimit,
		"MemoryRequest": config.MemoryRequest,
		"MemoryLimit":   config.MemoryLimit,
		"StorageLimit":  config.StorageLimit,
		"PodLimit":      config.PodLimit,
		"CreatedAt":     config.CreatedAt.Format(time.RFC3339),
		"UpdatedAt":     config.UpdatedAt.Format(time.RFC3339),
	}
}

