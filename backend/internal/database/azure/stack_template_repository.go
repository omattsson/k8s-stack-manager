package azure

import (
	"context"
	"encoding/json"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// StackTemplateRepository implements models.StackTemplateRepository for Azure Table Storage.
// Partition key: "global", Row key: template ID.
type StackTemplateRepository struct {
	client    AzureTableClient
	tableName string
}

// NewStackTemplateRepository creates a new Azure Table Storage stack template repository.
func NewStackTemplateRepository(accountName, accountKey, endpoint string, useAzurite bool) (*StackTemplateRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "StackTemplates", useAzurite)
	if err != nil {
		return nil, err
	}
	return &StackTemplateRepository{client: client, tableName: "StackTemplates"}, nil
}

// NewTestStackTemplateRepository creates a repository for unit testing.
func NewTestStackTemplateRepository() *StackTemplateRepository {
	return &StackTemplateRepository{tableName: "StackTemplates"}
}

// SetTestClient injects a mock client for testing.
func (r *StackTemplateRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

func (r *StackTemplateRepository) Create(template *models.StackTemplate) error {
	ctx := context.Background()
	now := time.Now().UTC()

	if template.ID == "" {
		template.ID = newID()
	}
	template.CreatedAt = now
	template.UpdatedAt = now

	entity := stackTemplateToEntity(template)
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

func (r *StackTemplateRepository) FindByID(id string) (*models.StackTemplate, error) {
	ctx := context.Background()

	resp, err := r.client.GetEntity(ctx, "global", id, nil)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}

	var entity map[string]interface{}
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError("unmarshal", err)
	}
	return stackTemplateFromEntity(entity), nil
}

func (r *StackTemplateRepository) Update(template *models.StackTemplate) error {
	ctx := context.Background()
	now := time.Now().UTC()
	template.UpdatedAt = now

	entity := stackTemplateToEntity(template)
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

func (r *StackTemplateRepository) Delete(id string) error {
	ctx := context.Background()

	_, err := r.client.DeleteEntity(ctx, "global", id, nil)
	if err != nil {
		return mapAzureError("delete", err)
	}
	return nil
}

func (r *StackTemplateRepository) List() ([]models.StackTemplate, error) {
	ctx := context.Background()

	filter := "PartitionKey eq 'global'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("list", err)
	}

	results := make([]models.StackTemplate, 0, len(entities))
	for _, e := range entities {
		results = append(results, *stackTemplateFromEntity(e))
	}
	return results, nil
}

func (r *StackTemplateRepository) ListPublished() ([]models.StackTemplate, error) {
	ctx := context.Background()

	filter := "PartitionKey eq 'global' and IsPublished eq true"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("list_published", err)
	}

	results := make([]models.StackTemplate, 0, len(entities))
	for _, e := range entities {
		results = append(results, *stackTemplateFromEntity(e))
	}
	return results, nil
}

func (r *StackTemplateRepository) ListByOwner(ownerID string) ([]models.StackTemplate, error) {
	ctx := context.Background()

	filter := "PartitionKey eq 'global' and OwnerID eq '" + ownerID + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("list_by_owner", err)
	}

	results := make([]models.StackTemplate, 0, len(entities))
	for _, e := range entities {
		results = append(results, *stackTemplateFromEntity(e))
	}
	return results, nil
}

func stackTemplateToEntity(t *models.StackTemplate) map[string]interface{} {
	return map[string]interface{}{
		"PartitionKey":  "global",
		"RowKey":        t.ID,
		"ID":            t.ID,
		"Name":          t.Name,
		"Description":   t.Description,
		"Category":      t.Category,
		"Version":       t.Version,
		"OwnerID":       t.OwnerID,
		"DefaultBranch": t.DefaultBranch,
		"IsPublished":   t.IsPublished,
		"CreatedAt":     t.CreatedAt.Format(time.RFC3339),
		"UpdatedAt":     t.UpdatedAt.Format(time.RFC3339),
	}
}

func stackTemplateFromEntity(e map[string]interface{}) *models.StackTemplate {
	return &models.StackTemplate{
		ID:            getString(e, "ID"),
		Name:          getString(e, "Name"),
		Description:   getString(e, "Description"),
		Category:      getString(e, "Category"),
		Version:       getString(e, "Version"),
		OwnerID:       getString(e, "OwnerID"),
		DefaultBranch: getString(e, "DefaultBranch"),
		IsPublished:   getBool(e, "IsPublished"),
		CreatedAt:     parseTime(e, "CreatedAt"),
		UpdatedAt:     parseTime(e, "UpdatedAt"),
	}
}
