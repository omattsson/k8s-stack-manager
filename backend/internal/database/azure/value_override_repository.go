package azure

import (
	"context"
	"encoding/json"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// ValueOverrideRepository implements models.ValueOverrideRepository for Azure Table Storage.
// Partition key: stack_instance_id, Row key: chart_config_id.
type ValueOverrideRepository struct {
	client    AzureTableClient
	tableName string
}

// NewValueOverrideRepository creates a new Azure Table Storage value override repository.
func NewValueOverrideRepository(accountName, accountKey, endpoint string, useAzurite bool) (*ValueOverrideRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "ValueOverrides", useAzurite)
	if err != nil {
		return nil, err
	}
	return &ValueOverrideRepository{client: client, tableName: "ValueOverrides"}, nil
}

// NewTestValueOverrideRepository creates a repository for unit testing.
func NewTestValueOverrideRepository() *ValueOverrideRepository {
	return &ValueOverrideRepository{tableName: "ValueOverrides"}
}

// SetTestClient injects a mock client for testing.
func (r *ValueOverrideRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// valueOverrideEntity is the typed Azure Table entity for value overrides.
type valueOverrideEntity struct {
	PartitionKey    string `json:"PartitionKey"`
	RowKey          string `json:"RowKey"`
	ID              string `json:"ID"`
	StackInstanceID string `json:"StackInstanceID"`
	ChartConfigID   string `json:"ChartConfigID"`
	Values          string `json:"Values"`
	UpdatedAt       string `json:"UpdatedAt"`
}

func (e *valueOverrideEntity) toModel() *models.ValueOverride {
	o := &models.ValueOverride{
		ID:              e.ID,
		StackInstanceID: e.StackInstanceID,
		ChartConfigID:   e.ChartConfigID,
		Values:          e.Values,
	}
	o.UpdatedAt, _ = time.Parse(time.RFC3339, e.UpdatedAt)
	return o
}

func (r *ValueOverrideRepository) Create(override *models.ValueOverride) error {
	ctx := context.Background()
	now := time.Now().UTC()

	if override.ID == "" {
		override.ID = newID()
	}
	if override.StackInstanceID == "" || override.ChartConfigID == "" {
		return dberrors.NewDatabaseError("create", dberrors.ErrValidation)
	}
	override.UpdatedAt = now

	entity := valueOverrideToEntity(override)
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

func (r *ValueOverrideRepository) FindByID(id string) (*models.ValueOverride, error) {
	ctx := context.Background()

	// ID is stored as a property; row key is chart_config_id.
	filter := "ID eq '" + id + "'"
	top := int32(1)
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
		Top:    &top,
	})

	entities, err := collectEntitiesTyped[valueOverrideEntity](ctx, pager, nil, 1)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}
	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
	}

	return entities[0].toModel(), nil
}

func (r *ValueOverrideRepository) FindByInstanceAndChart(instanceID, chartConfigID string) (*models.ValueOverride, error) {
	ctx := context.Background()

	// Direct point query — PK=instanceID, RK=chartConfigID.
	resp, err := r.client.GetEntity(ctx, instanceID, chartConfigID, nil)
	if err != nil {
		return nil, mapAzureError("find_by_instance_and_chart", err)
	}

	var entity valueOverrideEntity
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError("unmarshal", err)
	}
	return entity.toModel(), nil
}

func (r *ValueOverrideRepository) Update(override *models.ValueOverride) error {
	ctx := context.Background()
	now := time.Now().UTC()
	override.UpdatedAt = now

	entity := valueOverrideToEntity(override)
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

func (r *ValueOverrideRepository) Delete(id string) error {
	ctx := context.Background()

	// Find entity to get partition key and row key.
	override, err := r.FindByID(id)
	if err != nil {
		return err
	}

	_, err = r.client.DeleteEntity(ctx, override.StackInstanceID, override.ChartConfigID, nil)
	if err != nil {
		return mapAzureError("delete", err)
	}
	return nil
}

func (r *ValueOverrideRepository) ListByInstance(instanceID string) ([]models.ValueOverride, error) {
	ctx := context.Background()

	// Query by partition key — very efficient.
	filter := "PartitionKey eq '" + instanceID + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[valueOverrideEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError("list_by_instance", err)
	}

	results := make([]models.ValueOverride, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}
	return results, nil
}

func valueOverrideToEntity(o *models.ValueOverride) map[string]interface{} {
	return map[string]interface{}{
		"PartitionKey":    o.StackInstanceID,
		"RowKey":          o.ChartConfigID,
		"ID":              o.ID,
		"StackInstanceID": o.StackInstanceID,
		"ChartConfigID":   o.ChartConfigID,
		"Values":          o.Values,
		"UpdatedAt":       o.UpdatedAt.Format(time.RFC3339),
	}
}
