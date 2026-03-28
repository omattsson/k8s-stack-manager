package azure

import (
	"context"
	"encoding/json"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// StackDefinitionRepository implements models.StackDefinitionRepository for Azure Table Storage.
// Partition key: "global", Row key: definition ID.
type StackDefinitionRepository struct {
	client    AzureTableClient
	tableName string
}

// NewStackDefinitionRepository creates a new Azure Table Storage stack definition repository.
func NewStackDefinitionRepository(accountName, accountKey, endpoint string, useAzurite bool) (*StackDefinitionRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "StackDefinitions", useAzurite)
	if err != nil {
		return nil, err
	}
	return &StackDefinitionRepository{client: client, tableName: "StackDefinitions"}, nil
}

// NewTestStackDefinitionRepository creates a repository for unit testing.
func NewTestStackDefinitionRepository() *StackDefinitionRepository {
	return &StackDefinitionRepository{tableName: "StackDefinitions"}
}

// SetTestClient injects a mock client for testing.
func (r *StackDefinitionRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// stackDefinitionEntity is the typed Azure Table entity for stack definitions.
type stackDefinitionEntity struct {
	PartitionKey          string `json:"PartitionKey"`
	RowKey                string `json:"RowKey"`
	ID                    string `json:"ID"`
	Name                  string `json:"Name"`
	Description           string `json:"Description"`
	OwnerID               string `json:"OwnerID"`
	SourceTemplateID      string `json:"SourceTemplateID"`
	SourceTemplateVersion string `json:"SourceTemplateVersion"`
	DefaultBranch         string `json:"DefaultBranch"`
	CreatedAt             string `json:"CreatedAt"`
	UpdatedAt             string `json:"UpdatedAt"`
}

func (e *stackDefinitionEntity) toModel() *models.StackDefinition {
	d := &models.StackDefinition{
		ID:                    e.ID,
		Name:                  e.Name,
		Description:           e.Description,
		OwnerID:               e.OwnerID,
		SourceTemplateID:      e.SourceTemplateID,
		SourceTemplateVersion: e.SourceTemplateVersion,
		DefaultBranch:         e.DefaultBranch,
	}
	d.CreatedAt, _ = time.Parse(time.RFC3339, e.CreatedAt)
	d.UpdatedAt, _ = time.Parse(time.RFC3339, e.UpdatedAt)
	return d
}

func (r *StackDefinitionRepository) Create(definition *models.StackDefinition) error {
	ctx := context.Background()
	now := time.Now().UTC()

	if definition.ID == "" {
		definition.ID = newID()
	}
	definition.CreatedAt = now
	definition.UpdatedAt = now

	entity := stackDefinitionToEntity(definition)
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

func (r *StackDefinitionRepository) FindByID(id string) (*models.StackDefinition, error) {
	ctx := context.Background()

	resp, err := r.client.GetEntity(ctx, "global", id, nil)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}

	var entity stackDefinitionEntity
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError("unmarshal", err)
	}
	return entity.toModel(), nil
}

func (r *StackDefinitionRepository) Update(definition *models.StackDefinition) error {
	ctx := context.Background()
	now := time.Now().UTC()
	definition.UpdatedAt = now

	entity := stackDefinitionToEntity(definition)
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

func (r *StackDefinitionRepository) Delete(id string) error {
	ctx := context.Background()

	_, err := r.client.DeleteEntity(ctx, "global", id, nil)
	if err != nil {
		return mapAzureError("delete", err)
	}
	return nil
}

func (r *StackDefinitionRepository) List() ([]models.StackDefinition, error) {
	ctx := context.Background()

	filter := "PartitionKey eq 'global'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[stackDefinitionEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError("list", err)
	}

	results := make([]models.StackDefinition, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}
	return results, nil
}

func (r *StackDefinitionRepository) ListByOwner(ownerID string) ([]models.StackDefinition, error) {
	ctx := context.Background()

	filter := "PartitionKey eq 'global' and OwnerID eq '" + ownerID + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[stackDefinitionEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError("list_by_owner", err)
	}

	results := make([]models.StackDefinition, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}
	return results, nil
}

func (r *StackDefinitionRepository) ListByTemplate(templateID string) ([]models.StackDefinition, error) {
	ctx := context.Background()

	filter := "PartitionKey eq 'global' and SourceTemplateID eq '" + templateID + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[stackDefinitionEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError("list_by_template", err)
	}

	results := make([]models.StackDefinition, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}
	return results, nil
}

func stackDefinitionToEntity(d *models.StackDefinition) map[string]interface{} {
	return map[string]interface{}{
		"PartitionKey":          "global",
		"RowKey":                d.ID,
		"ID":                    d.ID,
		"Name":                  d.Name,
		"Description":           d.Description,
		"OwnerID":               d.OwnerID,
		"SourceTemplateID":      d.SourceTemplateID,
		"SourceTemplateVersion": d.SourceTemplateVersion,
		"DefaultBranch":         d.DefaultBranch,
		"CreatedAt":             d.CreatedAt.Format(time.RFC3339),
		"UpdatedAt":             d.UpdatedAt.Format(time.RFC3339),
	}
}
