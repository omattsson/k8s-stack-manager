package azure

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// SharedValuesRepository implements models.SharedValuesRepository for Azure Table Storage.
// Partition key: ClusterID, Row key: SharedValues ID (UUID).
type SharedValuesRepository struct {
	client    AzureTableClient
	tableName string
}

// NewSharedValuesRepository creates a new Azure Table Storage shared values repository.
func NewSharedValuesRepository(accountName, accountKey, endpoint string, useAzurite bool) (*SharedValuesRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "SharedValues", useAzurite)
	if err != nil {
		return nil, err
	}
	return &SharedValuesRepository{client: client, tableName: "SharedValues"}, nil
}

// NewTestSharedValuesRepository creates a repository for unit testing.
func NewTestSharedValuesRepository() *SharedValuesRepository {
	return &SharedValuesRepository{tableName: "SharedValues"}
}

// SetTestClient injects a mock client for testing.
func (r *SharedValuesRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// sharedValuesEntity is the typed Azure Table entity for shared values.
type sharedValuesEntity struct {
	PartitionKey string  `json:"PartitionKey"`
	RowKey       string  `json:"RowKey"`
	ID           string  `json:"ID"`
	ClusterID    string  `json:"ClusterID"`
	Name         string  `json:"Name"`
	Description  string  `json:"Description"`
	Values       string  `json:"Values"`
	Priority     float64 `json:"Priority"`
	CreatedAt    string  `json:"CreatedAt"`
	UpdatedAt    string  `json:"UpdatedAt"`
}

func (e *sharedValuesEntity) toModel() *models.SharedValues {
	sv := &models.SharedValues{
		ID:          e.ID,
		ClusterID:   e.ClusterID,
		Name:        e.Name,
		Description: e.Description,
		Values:      e.Values,
		Priority:    int(e.Priority),
	}
	sv.CreatedAt, _ = time.Parse(time.RFC3339, e.CreatedAt)
	sv.UpdatedAt, _ = time.Parse(time.RFC3339, e.UpdatedAt)
	return sv
}

func (r *SharedValuesRepository) Create(sv *models.SharedValues) error {
	ctx := context.Background()
	now := time.Now().UTC()

	if sv.ID == "" {
		sv.ID = newID()
	}
	sv.CreatedAt = now
	sv.UpdatedAt = now

	if err := sv.Validate(); err != nil {
		return dberrors.NewDatabaseError(err.Error(), dberrors.ErrValidation)
	}

	entity := sharedValuesToEntity(sv)
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

func (r *SharedValuesRepository) FindByID(id string) (*models.SharedValues, error) {
	ctx := context.Background()

	// ID is the RowKey but we don't know the PartitionKey, so scan by RowKey.
	filter := "RowKey eq '" + escapeODataString(id) + "'"
	top := int32(1)
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
		Top:    &top,
	})

	entities, err := collectEntitiesTyped[sharedValuesEntity](ctx, pager, nil, 1)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}
	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
	}

	return entities[0].toModel(), nil
}

func (r *SharedValuesRepository) FindByClusterAndID(clusterID, id string) (*models.SharedValues, error) {
	ctx := context.Background()

	resp, err := r.client.GetEntity(ctx, clusterID, id, nil)
	if err != nil {
		return nil, mapAzureError("find_by_cluster_and_id", err)
	}

	var entity sharedValuesEntity
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError("unmarshal", err)
	}

	return entity.toModel(), nil
}

func (r *SharedValuesRepository) Update(sv *models.SharedValues) error {
	ctx := context.Background()
	now := time.Now().UTC()
	sv.UpdatedAt = now

	if err := sv.Validate(); err != nil {
		return dberrors.NewDatabaseError(err.Error(), dberrors.ErrValidation)
	}

	entity := sharedValuesToEntity(sv)
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

func (r *SharedValuesRepository) Delete(id string) error {
	ctx := context.Background()

	// Find entity to get partition key.
	sv, err := r.FindByID(id)
	if err != nil {
		return err
	}

	_, err = r.client.DeleteEntity(ctx, sv.ClusterID, sv.ID, nil)
	if err != nil {
		return mapAzureError("delete", err)
	}
	return nil
}

func (r *SharedValuesRepository) ListByCluster(clusterID string) ([]models.SharedValues, error) {
	ctx := context.Background()

	filter := "PartitionKey eq '" + escapeODataString(clusterID) + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[sharedValuesEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError("list_by_cluster", err)
	}

	results := make([]models.SharedValues, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}

	// Sort by Priority ascending (lower = applied first).
	sort.Slice(results, func(i, j int) bool {
		return results[i].Priority < results[j].Priority
	})

	return results, nil
}

func sharedValuesToEntity(sv *models.SharedValues) map[string]interface{} {
	return map[string]interface{}{
		"PartitionKey": sv.ClusterID,
		"RowKey":       sv.ID,
		"ID":           sv.ID,
		"ClusterID":    sv.ClusterID,
		"Name":         sv.Name,
		"Description":  sv.Description,
		"Values":       sv.Values,
		"Priority":     sv.Priority,
		"CreatedAt":    sv.CreatedAt.Format(time.RFC3339),
		"UpdatedAt":    sv.UpdatedAt.Format(time.RFC3339),
	}
}
