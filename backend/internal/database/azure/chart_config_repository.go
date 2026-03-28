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

// chartConfigEntity is the typed Azure Table entity for chart configs.
type chartConfigEntity struct {
	PartitionKey      string  `json:"PartitionKey"`
	RowKey            string  `json:"RowKey"`
	ID                string  `json:"ID"`
	StackDefinitionID string  `json:"StackDefinitionID"`
	ChartName         string  `json:"ChartName"`
	RepositoryURL     string  `json:"RepositoryURL"`
	SourceRepoURL     string  `json:"SourceRepoURL"`
	ChartPath         string  `json:"ChartPath"`
	ChartVersion      string  `json:"ChartVersion"`
	DefaultValues     string  `json:"DefaultValues"`
	DeployOrder       float64 `json:"DeployOrder"`
	CreatedAt         string  `json:"CreatedAt"`
}

func (e *chartConfigEntity) toModel() *models.ChartConfig {
	c := &models.ChartConfig{
		ID:                e.ID,
		StackDefinitionID: e.StackDefinitionID,
		ChartName:         e.ChartName,
		RepositoryURL:     e.RepositoryURL,
		SourceRepoURL:     e.SourceRepoURL,
		ChartPath:         e.ChartPath,
		ChartVersion:      e.ChartVersion,
		DefaultValues:     e.DefaultValues,
		DeployOrder:       int(e.DeployOrder),
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339, e.CreatedAt)
	return c
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
	top := int32(1)
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
		Top:    &top,
	})

	entities, err := collectEntitiesTyped[chartConfigEntity](ctx, pager, nil, 1)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}
	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
	}

	return entities[0].toModel(), nil
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

	entities, err := collectEntitiesTyped[chartConfigEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError("list_by_definition", err)
	}

	results := make([]models.ChartConfig, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
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
