package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

const tableChartBranchOverrides = "ChartBranchOverrides"


// ChartBranchOverrideRepository implements models.ChartBranchOverrideRepository for Azure Table Storage.
// Partition key: stack_instance_id, Row key: chart_config_id.
type ChartBranchOverrideRepository struct {
	client    AzureTableClient
	tableName string
}

// NewChartBranchOverrideRepository creates a new Azure Table Storage chart branch override repository.
func NewChartBranchOverrideRepository(accountName, accountKey, endpoint string, useAzurite bool) (*ChartBranchOverrideRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, tableChartBranchOverrides, useAzurite)
	if err != nil {
		return nil, err
	}
	return &ChartBranchOverrideRepository{client: client, tableName: tableChartBranchOverrides}, nil
}

// NewTestChartBranchOverrideRepository creates a repository for unit testing.
func NewTestChartBranchOverrideRepository() *ChartBranchOverrideRepository {
	return &ChartBranchOverrideRepository{tableName: tableChartBranchOverrides}
}

// SetTestClient injects a mock client for testing.
func (r *ChartBranchOverrideRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// chartBranchOverrideEntity is the typed Azure Table entity for chart branch overrides.
type chartBranchOverrideEntity struct {
	PartitionKey    string `json:"PartitionKey"`
	RowKey          string `json:"RowKey"`
	ID              string `json:"ID"`
	StackInstanceID string `json:"StackInstanceID"`
	ChartConfigID   string `json:"ChartConfigID"`
	Branch          string `json:"Branch"`
	UpdatedAt       string `json:"UpdatedAt"`
}

func (e *chartBranchOverrideEntity) toModel() *models.ChartBranchOverride {
	o := &models.ChartBranchOverride{
		ID:              e.ID,
		StackInstanceID: e.StackInstanceID,
		ChartConfigID:   e.ChartConfigID,
		Branch:          e.Branch,
	}
	o.UpdatedAt, _ = time.Parse(time.RFC3339, e.UpdatedAt)
	return o
}

func (r *ChartBranchOverrideRepository) List(instanceID string) ([]*models.ChartBranchOverride, error) {
	ctx := context.Background()

	filter := "PartitionKey eq '" + escapeODataString(instanceID) + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[chartBranchOverrideEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError(opList, err)
	}

	results := make([]*models.ChartBranchOverride, 0, len(entities))
	for _, e := range entities {
		results = append(results, e.toModel())
	}
	return results, nil
}

func (r *ChartBranchOverrideRepository) Get(instanceID, chartConfigID string) (*models.ChartBranchOverride, error) {
	ctx := context.Background()

	resp, err := r.client.GetEntity(ctx, instanceID, chartConfigID, nil)
	if err != nil {
		return nil, mapAzureError("get", err)
	}

	var entity chartBranchOverrideEntity
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError(opUnmarshal, err)
	}
	return entity.toModel(), nil
}

func (r *ChartBranchOverrideRepository) Set(override *models.ChartBranchOverride) error {
	ctx := context.Background()

	if err := override.Validate(); err != nil {
		return dberrors.NewDatabaseError("validate", fmt.Errorf("%s: %w", err.Error(), dberrors.ErrValidation))
	}

	if override.ID == "" {
		override.ID = newID()
	}
	override.UpdatedAt = time.Now().UTC()

	entity := chartBranchOverrideToEntity(override)
	entityBytes, err := json.Marshal(entity)
	if err != nil {
		return dberrors.NewDatabaseError(opMarshal, err)
	}

	// Upsert — replace if exists, create if not.
	_, err = r.client.UpsertEntity(ctx, entityBytes, &aztables.UpsertEntityOptions{
		UpdateMode: aztables.UpdateModeReplace,
	})
	if err != nil {
		return mapAzureError("set", err)
	}
	return nil
}

func (r *ChartBranchOverrideRepository) Delete(instanceID, chartConfigID string) error {
	ctx := context.Background()

	_, err := r.client.DeleteEntity(ctx, instanceID, chartConfigID, nil)
	if err != nil {
		return mapAzureError(opDelete, err)
	}
	return nil
}

func (r *ChartBranchOverrideRepository) DeleteByInstance(instanceID string) error {
	overrides, err := r.List(instanceID)
	if err != nil {
		return err
	}
	for _, o := range overrides {
		if err := r.Delete(o.StackInstanceID, o.ChartConfigID); err != nil {
			return err
		}
	}
	return nil
}

func chartBranchOverrideToEntity(o *models.ChartBranchOverride) map[string]interface{} {
	return map[string]interface{}{
		"PartitionKey":    o.StackInstanceID,
		"RowKey":          o.ChartConfigID,
		"ID":              o.ID,
		"StackInstanceID": o.StackInstanceID,
		"ChartConfigID":   o.ChartConfigID,
		"Branch":          o.Branch,
		"UpdatedAt":       o.UpdatedAt.Format(time.RFC3339),
	}
}
