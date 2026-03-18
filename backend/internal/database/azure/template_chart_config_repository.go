package azure

import (
	"context"
	"encoding/json"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// TemplateChartConfigRepository implements models.TemplateChartConfigRepository for Azure Table Storage.
// Partition key: stack_template_id, Row key: chart config ID.
type TemplateChartConfigRepository struct {
	client    AzureTableClient
	tableName string
}

// NewTemplateChartConfigRepository creates a new Azure Table Storage template chart config repository.
func NewTemplateChartConfigRepository(accountName, accountKey, endpoint string, useAzurite bool) (*TemplateChartConfigRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "TemplateChartConfigs", useAzurite)
	if err != nil {
		return nil, err
	}
	return &TemplateChartConfigRepository{client: client, tableName: "TemplateChartConfigs"}, nil
}

// NewTestTemplateChartConfigRepository creates a repository for unit testing.
func NewTestTemplateChartConfigRepository() *TemplateChartConfigRepository {
	return &TemplateChartConfigRepository{tableName: "TemplateChartConfigs"}
}

// SetTestClient injects a mock client for testing.
func (r *TemplateChartConfigRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

func (r *TemplateChartConfigRepository) Create(config *models.TemplateChartConfig) error {
	ctx := context.Background()
	now := time.Now().UTC()

	if config.ID == "" {
		config.ID = newID()
	}
	if config.StackTemplateID == "" {
		return dberrors.NewDatabaseError("create", dberrors.ErrValidation)
	}
	config.CreatedAt = now

	entity := templateChartConfigToEntity(config)
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

func (r *TemplateChartConfigRepository) FindByID(id string) (*models.TemplateChartConfig, error) {
	ctx := context.Background()

	// ID is the row key, but we don't know the partition key (stack_template_id).
	// We must scan across partitions.
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

	return templateChartConfigFromEntity(entities[0]), nil
}

func (r *TemplateChartConfigRepository) Update(config *models.TemplateChartConfig) error {
	ctx := context.Background()

	entity := templateChartConfigToEntity(config)
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

func (r *TemplateChartConfigRepository) Delete(id string) error {
	ctx := context.Background()

	// Find the entity first to get the partition key.
	config, err := r.FindByID(id)
	if err != nil {
		return err
	}

	_, err = r.client.DeleteEntity(ctx, config.StackTemplateID, id, nil)
	if err != nil {
		return mapAzureError("delete", err)
	}
	return nil
}

func (r *TemplateChartConfigRepository) ListByTemplate(templateID string) ([]models.TemplateChartConfig, error) {
	ctx := context.Background()

	// Query by partition key — very efficient.
	filter := "PartitionKey eq '" + templateID + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("list_by_template", err)
	}

	results := make([]models.TemplateChartConfig, 0, len(entities))
	for _, e := range entities {
		results = append(results, *templateChartConfigFromEntity(e))
	}
	return results, nil
}

func templateChartConfigToEntity(c *models.TemplateChartConfig) map[string]interface{} {
	return map[string]interface{}{
		"PartitionKey":    c.StackTemplateID,
		"RowKey":          c.ID,
		"ID":              c.ID,
		"StackTemplateID": c.StackTemplateID,
		"ChartName":       c.ChartName,
		"RepositoryURL":   c.RepositoryURL,
		"SourceRepoURL":   c.SourceRepoURL,
		"ChartPath":       c.ChartPath,
		"ChartVersion":    c.ChartVersion,
		"DefaultValues":   c.DefaultValues,
		"LockedValues":    c.LockedValues,
		"DeployOrder":     c.DeployOrder,
		"Required":        c.Required,
		"CreatedAt":       c.CreatedAt.Format(time.RFC3339),
	}
}

func templateChartConfigFromEntity(e map[string]interface{}) *models.TemplateChartConfig {
	return &models.TemplateChartConfig{
		ID:              getString(e, "ID"),
		StackTemplateID: getString(e, "StackTemplateID"),
		ChartName:       getString(e, "ChartName"),
		RepositoryURL:   getString(e, "RepositoryURL"),
		SourceRepoURL:   getString(e, "SourceRepoURL"),
		ChartPath:       getString(e, "ChartPath"),
		ChartVersion:    getString(e, "ChartVersion"),
		DefaultValues:   getString(e, "DefaultValues"),
		LockedValues:    getString(e, "LockedValues"),
		DeployOrder:     getInt(e, "DeployOrder"),
		Required:        getBool(e, "Required"),
		CreatedAt:       parseTime(e, "CreatedAt"),
	}
}
