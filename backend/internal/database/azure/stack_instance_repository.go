package azure

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// StackInstanceRepository implements models.StackInstanceRepository for Azure Table Storage.
// Partition key: "global", Row key: instance ID.
type StackInstanceRepository struct {
	client    AzureTableClient
	tableName string
}

// NewStackInstanceRepository creates a new Azure Table Storage stack instance repository.
func NewStackInstanceRepository(accountName, accountKey, endpoint string, useAzurite bool) (*StackInstanceRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "StackInstances", useAzurite)
	if err != nil {
		return nil, err
	}
	return &StackInstanceRepository{client: client, tableName: "StackInstances"}, nil
}

// NewTestStackInstanceRepository creates a repository for unit testing.
func NewTestStackInstanceRepository() *StackInstanceRepository {
	return &StackInstanceRepository{tableName: "StackInstances"}
}

// SetTestClient injects a mock client for testing.
func (r *StackInstanceRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

func (r *StackInstanceRepository) Create(instance *models.StackInstance) error {
	ctx := context.Background()
	now := time.Now().UTC()

	if instance.ID == "" {
		instance.ID = newID()
	}
	instance.CreatedAt = now
	instance.UpdatedAt = now

	entity := stackInstanceToEntity(instance)
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

func (r *StackInstanceRepository) FindByID(id string) (*models.StackInstance, error) {
	ctx := context.Background()

	resp, err := r.client.GetEntity(ctx, "global", id, nil)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}

	var entity map[string]interface{}
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError("unmarshal", err)
	}
	return stackInstanceFromEntity(entity), nil
}

func (r *StackInstanceRepository) Update(instance *models.StackInstance) error {
	ctx := context.Background()
	now := time.Now().UTC()
	instance.UpdatedAt = now

	entity := stackInstanceToEntity(instance)
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

func (r *StackInstanceRepository) Delete(id string) error {
	ctx := context.Background()

	_, err := r.client.DeleteEntity(ctx, "global", id, nil)
	if err != nil {
		return mapAzureError("delete", err)
	}
	return nil
}

func (r *StackInstanceRepository) FindByNamespace(namespace string) (*models.StackInstance, error) {
	ctx := context.Background()

	// Escape single quotes in OData string literals to prevent filter injection.
	escaped := strings.ReplaceAll(namespace, "'", "''")
	filter := "PartitionKey eq 'global' and Namespace eq '" + escaped + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("find_by_namespace", err)
	}

	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("find_by_namespace", dberrors.ErrNotFound)
	}
	return stackInstanceFromEntity(entities[0]), nil
}

func (r *StackInstanceRepository) List() ([]models.StackInstance, error) {
	ctx := context.Background()

	filter := "PartitionKey eq 'global'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("list", err)
	}

	results := make([]models.StackInstance, 0, len(entities))
	for _, e := range entities {
		results = append(results, *stackInstanceFromEntity(e))
	}
	return results, nil
}

func (r *StackInstanceRepository) ListByOwner(ownerID string) ([]models.StackInstance, error) {
	ctx := context.Background()

	filter := "PartitionKey eq 'global' and OwnerID eq '" + ownerID + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("list_by_owner", err)
	}

	results := make([]models.StackInstance, 0, len(entities))
	for _, e := range entities {
		results = append(results, *stackInstanceFromEntity(e))
	}
	return results, nil
}

func (r *StackInstanceRepository) FindByCluster(clusterID string) ([]models.StackInstance, error) {
	ctx := context.Background()

	escaped := strings.ReplaceAll(clusterID, "'", "''")
	filter := "PartitionKey eq 'global' and ClusterID eq '" + escaped + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("find_by_cluster", err)
	}

	results := make([]models.StackInstance, 0, len(entities))
	for _, e := range entities {
		results = append(results, *stackInstanceFromEntity(e))
	}
	return results, nil
}

func stackInstanceToEntity(i *models.StackInstance) map[string]interface{} {
	entity := map[string]interface{}{
		"PartitionKey":      "global",
		"RowKey":            i.ID,
		"ID":                i.ID,
		"StackDefinitionID": i.StackDefinitionID,
		"Name":              i.Name,
		"Namespace":         i.Namespace,
		"OwnerID":           i.OwnerID,
		"Branch":            i.Branch,
		"ClusterID":         i.ClusterID,
		"Status":            i.Status,
		"ErrorMessage":      i.ErrorMessage,
		"CreatedAt":         i.CreatedAt.Format(time.RFC3339),
		"UpdatedAt":         i.UpdatedAt.Format(time.RFC3339),
	}
	if i.LastDeployedAt != nil {
		entity["LastDeployedAt"] = i.LastDeployedAt.Format(time.RFC3339)
	}
	return entity
}

func stackInstanceFromEntity(e map[string]interface{}) *models.StackInstance {
	instance := &models.StackInstance{
		ID:                getString(e, "ID"),
		StackDefinitionID: getString(e, "StackDefinitionID"),
		Name:              getString(e, "Name"),
		Namespace:         getString(e, "Namespace"),
		OwnerID:           getString(e, "OwnerID"),
		Branch:            getString(e, "Branch"),
		ClusterID:         getString(e, "ClusterID"),
		Status:            getString(e, "Status"),
		ErrorMessage:      getString(e, "ErrorMessage"),
		CreatedAt:         parseTime(e, "CreatedAt"),
		UpdatedAt:         parseTime(e, "UpdatedAt"),
	}
	if s := getString(e, "LastDeployedAt"); s != "" {
		t := parseTime(e, "LastDeployedAt")
		instance.LastDeployedAt = &t
	}
	return instance
}
