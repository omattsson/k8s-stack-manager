package azure

import (
	"context"
	"encoding/json"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// ChartBranchOverrideRepository implements models.ChartBranchOverrideRepository for Azure Table Storage.
// Partition key: stack_instance_id, Row key: chart_config_id.
type ChartBranchOverrideRepository struct {
	client    AzureTableClient
	tableName string
}

// NewChartBranchOverrideRepository creates a new Azure Table Storage chart branch override repository.
func NewChartBranchOverrideRepository(accountName, accountKey, endpoint string, useAzurite bool) (*ChartBranchOverrideRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "ChartBranchOverrides", useAzurite)
	if err != nil {
		return nil, err
	}
	return &ChartBranchOverrideRepository{client: client, tableName: "ChartBranchOverrides"}, nil
}

// NewTestChartBranchOverrideRepository creates a repository for unit testing.
func NewTestChartBranchOverrideRepository() *ChartBranchOverrideRepository {
	return &ChartBranchOverrideRepository{tableName: "ChartBranchOverrides"}
}

// SetTestClient injects a mock client for testing.
func (r *ChartBranchOverrideRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

func (r *ChartBranchOverrideRepository) List(instanceID string) ([]*models.ChartBranchOverride, error) {
	ctx := context.Background()

	filter := "PartitionKey eq '" + escapeODataString(instanceID) + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("list", err)
	}

	results := make([]*models.ChartBranchOverride, 0, len(entities))
	for _, e := range entities {
		results = append(results, chartBranchOverrideFromEntity(e))
	}
	return results, nil
}

func (r *ChartBranchOverrideRepository) Get(instanceID, chartConfigID string) (*models.ChartBranchOverride, error) {
	ctx := context.Background()

	resp, err := r.client.GetEntity(ctx, instanceID, chartConfigID, nil)
	if err != nil {
		return nil, mapAzureError("get", err)
	}

	var entity map[string]interface{}
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError("unmarshal", err)
	}
	return chartBranchOverrideFromEntity(entity), nil
}

func (r *ChartBranchOverrideRepository) Set(override *models.ChartBranchOverride) error {
	ctx := context.Background()

	if err := override.Validate(); err != nil {
		return dberrors.NewDatabaseError("validate", dberrors.ErrValidation)
	}

	if override.ID == "" {
		override.ID = newID()
	}
	override.UpdatedAt = time.Now().UTC()

	entity := chartBranchOverrideToEntity(override)
	entityBytes, err := json.Marshal(entity)
	if err != nil {
		return dberrors.NewDatabaseError("marshal", err)
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
		return mapAzureError("delete", err)
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

func chartBranchOverrideFromEntity(e map[string]interface{}) *models.ChartBranchOverride {
	return &models.ChartBranchOverride{
		ID:              getString(e, "ID"),
		StackInstanceID: getString(e, "StackInstanceID"),
		ChartConfigID:   getString(e, "ChartConfigID"),
		Branch:          getString(e, "Branch"),
		UpdatedAt:       parseTime(e, "UpdatedAt"),
	}
}
