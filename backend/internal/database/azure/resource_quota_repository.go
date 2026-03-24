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

// GetByClusterID returns the resource quota config for the given cluster.
func (r *ResourceQuotaRepository) GetByClusterID(ctx context.Context, clusterID string) (*models.ResourceQuotaConfig, error) {
	resp, err := r.client.GetEntity(ctx, clusterID, quotaRowKey, nil)
	if err != nil {
		return nil, mapAzureError("get_by_cluster_id", err)
	}

	var entity map[string]interface{}
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError("unmarshal", err)
	}

	return resourceQuotaFromEntity(entity), nil
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

func resourceQuotaFromEntity(e map[string]interface{}) *models.ResourceQuotaConfig {
	return &models.ResourceQuotaConfig{
		ID:            getString(e, "ID"),
		ClusterID:     getString(e, "ClusterID"),
		CPURequest:    getString(e, "CPURequest"),
		CPULimit:      getString(e, "CPULimit"),
		MemoryRequest: getString(e, "MemoryRequest"),
		MemoryLimit:   getString(e, "MemoryLimit"),
		StorageLimit:  getString(e, "StorageLimit"),
		PodLimit:      getInt(e, "PodLimit"),
		CreatedAt:     parseTime(e, "CreatedAt"),
		UpdatedAt:     parseTime(e, "UpdatedAt"),
	}
}
