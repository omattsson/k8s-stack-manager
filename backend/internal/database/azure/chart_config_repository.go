package azure

import (
	"context"
	"encoding/json"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// ChartConfigRepository implements models.ChartConfigRepository for Azure Table Storage.
// Partition key: stack_definition_id, Row key: chart config ID.
type ChartConfigRepository struct {
	client    AzureTableClient
	tableName string
}

// NewChartConfigRepository creates a new Azure Table Storage chart config repository.
func NewChartConfigRepository(accountName, accountKey, endpoint string, useAzurite bool) (*ChartConfigRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "ChartConfigs", useAzurite)
	if err != nil {
		return nil, err
	}
	return &ChartConfigRepository{client: client, tableName: "ChartConfigs"}, nil
}

// NewTestChartConfigRepository creates a repository for unit testing.
func NewTestChartConfigRepository() *ChartConfigRepository {
	return &ChartConfigRepository{tableName: "ChartConfigs"}
}

// SetTestClient injects a mock client for testing.
func (r *ChartConfigRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

func (r *ChartConfigRepository) Create(config *models.ChartConfig) error {
	ctx := context.Background()
	now := time.Now().UTC()

	if config.ID == "" {
		config.ID = newID()
	}
	if config.StackDefinitionID == "" {
		return dberrors.NewDatabaseError("create", dberrors.ErrValidation)
	}
	config.CreatedAt = now

	entity := chartConfigToEntity(config)
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

func (r *ChartConfigRepository) FindByID(id string) (*models.ChartConfig, error) {
	ctx := context.Background()

	// ID is the row key, but partition key (stack_definition_id) is unknown.
	filter := "RowKey eq '" + id + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}
	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
	}

	return chartConfigFromEntity(entities[0]), nil
}

func (r *ChartConfigRepository) Update(config *models.ChartConfig) error {
	ctx := context.Background()

	entity := chartConfigToEntity(config)
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

func (r *ChartConfigRepository) Delete(id string) error {
	ctx := context.Background()

	// Find entity to get partition key.
	config, err := r.FindByID(id)
	if err != nil {
		return err
	}

	_, err = r.client.DeleteEntity(ctx, config.StackDefinitionID, id, nil)
	if err != nil {
		return mapAzureError("delete", err)
	}
	return nil
}

func (r *ChartConfigRepository) ListByDefinition(definitionID string) ([]models.ChartConfig, error) {
	ctx := context.Background()

	// Query by partition key — very efficient.
	filter := "PartitionKey eq '" + definitionID + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("list_by_definition", err)
	}

	results := make([]models.ChartConfig, 0, len(entities))
	for _, e := range entities {
		results = append(results, *chartConfigFromEntity(e))
	}
	return results, nil
}

func chartConfigToEntity(c *models.ChartConfig) map[string]interface{} {
	return map[string]interface{}{
		"PartitionKey":      c.StackDefinitionID,
		"RowKey":            c.ID,
		"ID":                c.ID,
		"StackDefinitionID": c.StackDefinitionID,
		"ChartName":         c.ChartName,
		"RepositoryURL":     c.RepositoryURL,
		"SourceRepoURL":     c.SourceRepoURL,
		"ChartPath":         c.ChartPath,
		"ChartVersion":      c.ChartVersion,
		"DefaultValues":     c.DefaultValues,
		"DeployOrder":       c.DeployOrder,
		"CreatedAt":         c.CreatedAt.Format(time.RFC3339),
	}
}

func chartConfigFromEntity(e map[string]interface{}) *models.ChartConfig {
	return &models.ChartConfig{
		ID:                getString(e, "ID"),
		StackDefinitionID: getString(e, "StackDefinitionID"),
		ChartName:         getString(e, "ChartName"),
		RepositoryURL:     getString(e, "RepositoryURL"),
		SourceRepoURL:     getString(e, "SourceRepoURL"),
		ChartPath:         getString(e, "ChartPath"),
		ChartVersion:      getString(e, "ChartVersion"),
		DefaultValues:     getString(e, "DefaultValues"),
		DeployOrder:       getInt(e, "DeployOrder"),
		CreatedAt:         parseTime(e, "CreatedAt"),
	}
}
