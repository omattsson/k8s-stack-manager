package azure

import (
	"context"
	"encoding/json"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

const tableStackInstances = "StackInstances"
const filterPKGlobal = odataPartitionKeyEq + pkGlobal + "'"


// StackInstanceRepository implements models.StackInstanceRepository for Azure Table Storage.
// Partition key: "global", Row key: instance ID.
type StackInstanceRepository struct {
	client    AzureTableClient
	tableName string
}

// NewStackInstanceRepository creates a new Azure Table Storage stack instance repository.
func NewStackInstanceRepository(accountName, accountKey, endpoint string, useAzurite bool) (*StackInstanceRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, tableStackInstances, useAzurite)
	if err != nil {
		return nil, err
	}
	return &StackInstanceRepository{client: client, tableName: tableStackInstances}, nil
}

// NewTestStackInstanceRepository creates a repository for unit testing.
func NewTestStackInstanceRepository() *StackInstanceRepository {
	return &StackInstanceRepository{tableName: tableStackInstances}
}

// SetTestClient injects a mock client for testing.
func (r *StackInstanceRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// stackInstanceEntity is the typed Azure Table entity for stack instances.
type stackInstanceEntity struct {
	PartitionKey      string  `json:"PartitionKey"`
	RowKey            string  `json:"RowKey"`
	ID                string  `json:"ID"`
	StackDefinitionID string  `json:"StackDefinitionID"`
	Name              string  `json:"Name"`
	Namespace         string  `json:"Namespace"`
	OwnerID           string  `json:"OwnerID"`
	Branch            string  `json:"Branch"`
	ClusterID         string  `json:"ClusterID"`
	Status            string  `json:"Status"`
	ErrorMessage      string  `json:"ErrorMessage"`
	TTLMinutes        float64 `json:"TTLMinutes"`
	LastDeployedAt    string  `json:"LastDeployedAt,omitempty"`
	ExpiresAt         string  `json:"ExpiresAt,omitempty"`
	CreatedAt         string  `json:"CreatedAt"`
	UpdatedAt         string  `json:"UpdatedAt"`
}

func (e *stackInstanceEntity) toModel() *models.StackInstance {
	instance := &models.StackInstance{
		ID:                e.ID,
		StackDefinitionID: e.StackDefinitionID,
		Name:              e.Name,
		Namespace:         e.Namespace,
		OwnerID:           e.OwnerID,
		Branch:            e.Branch,
		ClusterID:         e.ClusterID,
		Status:            e.Status,
		ErrorMessage:      e.ErrorMessage,
		TTLMinutes:        int(e.TTLMinutes),
	}
	instance.CreatedAt, _ = time.Parse(time.RFC3339, e.CreatedAt)
	instance.UpdatedAt, _ = time.Parse(time.RFC3339, e.UpdatedAt)
	if e.LastDeployedAt != "" {
		t, err := time.Parse(time.RFC3339, e.LastDeployedAt)
		if err == nil {
			instance.LastDeployedAt = &t
		}
	}
	if e.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, e.ExpiresAt)
		if err == nil {
			instance.ExpiresAt = &t
		}
	}
	return instance
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
		return dberrors.NewDatabaseError(opMarshal, err)
	}

	_, err = r.client.AddEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError(opCreate, err)
	}
	return nil
}

func (r *StackInstanceRepository) FindByID(id string) (*models.StackInstance, error) {
	ctx := context.Background()

	resp, err := r.client.GetEntity(ctx, pkGlobal, id, nil)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}

	var entity stackInstanceEntity
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError(opUnmarshal, err)
	}
	return entity.toModel(), nil
}

func (r *StackInstanceRepository) Update(instance *models.StackInstance) error {
	ctx := context.Background()
	now := time.Now().UTC()
	instance.UpdatedAt = now

	entity := stackInstanceToEntity(instance)
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

func (r *StackInstanceRepository) Delete(id string) error {
	ctx := context.Background()

	_, err := r.client.DeleteEntity(ctx, pkGlobal, id, nil)
	if err != nil {
		return mapAzureError(opDelete, err)
	}
	return nil
}

func (r *StackInstanceRepository) FindByNamespace(namespace string) (*models.StackInstance, error) {
	ctx := context.Background()

	filter := filterPKGlobal + " and Namespace eq '" + escapeODataString(namespace) + "'"
	top := int32(1)
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
		Top:    &top,
	})

	entities, err := collectEntitiesTyped[stackInstanceEntity](ctx, pager, nil, 1)
	if err != nil {
		return nil, mapAzureError("find_by_namespace", err)
	}

	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("find_by_namespace", dberrors.ErrNotFound)
	}
	return entities[0].toModel(), nil
}

func (r *StackInstanceRepository) List() ([]models.StackInstance, error) {
	ctx := context.Background()

	filter := filterPKGlobal
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[stackInstanceEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError(opList, err)
	}

	results := make([]models.StackInstance, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}
	return results, nil
}

func (r *StackInstanceRepository) ListPaged(limit, offset int) ([]models.StackInstance, int, error) {
	ctx := context.Background()

	filter := filterPKGlobal
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	// Collect all then paginate in-memory. Azure Table Storage does not support
	// $skip, so server-side offset is not possible. However, using $top on the
	// pager limits the number of entities fetched when offset is 0 and the total
	// count is not needed beyond the page window.
	entities, err := collectEntitiesTyped[stackInstanceEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, 0, mapAzureError(opList, err)
	}

	total := len(entities)

	// Apply offset
	if offset > 0 {
		if offset >= total {
			return []models.StackInstance{}, total, nil
		}
		entities = entities[offset:]
	}

	// Apply limit
	if limit > 0 && limit < len(entities) {
		entities = entities[:limit]
	}

	results := make([]models.StackInstance, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}
	return results, total, nil
}

func (r *StackInstanceRepository) CountAll() (int, error) {
	ctx := context.Background()
	filter := filterPKGlobal
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})
	entities, err := collectEntitiesTyped[stackInstanceEntity](ctx, pager, nil, 0)
	if err != nil {
		return 0, mapAzureError(opList, err)
	}
	return len(entities), nil
}

func (r *StackInstanceRepository) CountByStatus(status string) (int, error) {
	ctx := context.Background()
	filter := filterPKGlobal + " and Status eq '" + escapeODataString(status) + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})
	entities, err := collectEntitiesTyped[stackInstanceEntity](ctx, pager, nil, 0)
	if err != nil {
		return 0, mapAzureError(opList, err)
	}
	return len(entities), nil
}

func (r *StackInstanceRepository) ExistsByDefinitionAndStatus(definitionID, status string) (bool, error) {
	ctx := context.Background()
	filter := filterPKGlobal +
		" and StackDefinitionID eq '" + escapeODataString(definitionID) + "'" +
		" and Status eq '" + escapeODataString(status) + "'"
	top := int32(1)
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
		Top:    &top,
	})
	entities, err := collectEntitiesTyped[stackInstanceEntity](ctx, pager, nil, 1)
	if err != nil {
		return false, mapAzureError(opList, err)
	}
	return len(entities) > 0, nil
}

func (r *StackInstanceRepository) ListByOwner(ownerID string) ([]models.StackInstance, error) {
	ctx := context.Background()

	filter := filterPKGlobal + " and OwnerID eq '" + escapeODataString(ownerID) + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[stackInstanceEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError("list_by_owner", err)
	}

	results := make([]models.StackInstance, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}
	return results, nil
}

func (r *StackInstanceRepository) FindByCluster(clusterID string) ([]models.StackInstance, error) {
	ctx := context.Background()

	// For empty clusterID, Azure Table OData 'ClusterID eq ""' will not match
	// entities where the property is absent (pre-existing rows). Scan the full
	// partition and filter in-memory instead.
	if clusterID == "" {
		filter := filterPKGlobal
		pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
			Filter: &filter,
		})

		entities, err := collectEntitiesTyped[stackInstanceEntity](ctx, pager, func(e *stackInstanceEntity) bool {
			return e.ClusterID == ""
		}, 0)
		if err != nil {
			return nil, mapAzureError("find_by_cluster", err)
		}

		results := make([]models.StackInstance, 0, len(entities))
		for _, e := range entities {
			results = append(results, *e.toModel())
		}
		return results, nil
	}

	filter := filterPKGlobal + " and ClusterID eq '" + escapeODataString(clusterID) + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[stackInstanceEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError("find_by_cluster", err)
	}

	results := make([]models.StackInstance, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}
	return results, nil
}

func (r *StackInstanceRepository) CountByClusterAndOwner(clusterID, ownerID string) (int, error) {
	instances, err := r.FindByCluster(clusterID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, inst := range instances {
		if inst.OwnerID == ownerID {
			count++
		}
	}
	return count, nil
}

func (r *StackInstanceRepository) ListExpired() ([]*models.StackInstance, error) {
	ctx := context.Background()
	now := time.Now().UTC()

	// Pre-filter to running instances; only they can expire.
	filter := filterPKGlobal + " and Status eq 'running'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[stackInstanceEntity](ctx, pager, func(e *stackInstanceEntity) bool {
		if e.Status != models.StackStatusRunning {
			return false
		}
		if e.ExpiresAt == "" {
			return false
		}
		t, parseErr := time.Parse(time.RFC3339, e.ExpiresAt)
		if parseErr != nil {
			return false
		}
		return t.Before(now)
	}, 0)
	if err != nil {
		return nil, mapAzureError("list_expired", err)
	}

	results := make([]*models.StackInstance, 0, len(entities))
	for _, e := range entities {
		results = append(results, e.toModel())
	}
	return results, nil
}

func stackInstanceToEntity(i *models.StackInstance) map[string]interface{} {
	entity := map[string]interface{}{
		"PartitionKey":      pkGlobal,
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
		"TTLMinutes":        int64(i.TTLMinutes),
		"CreatedAt":         i.CreatedAt.Format(time.RFC3339),
		"UpdatedAt":         i.UpdatedAt.Format(time.RFC3339),
	}
	if i.LastDeployedAt != nil {
		entity["LastDeployedAt"] = i.LastDeployedAt.Format(time.RFC3339)
	}
	if i.ExpiresAt != nil {
		entity["ExpiresAt"] = i.ExpiresAt.Format(time.RFC3339)
	}
	return entity
}
