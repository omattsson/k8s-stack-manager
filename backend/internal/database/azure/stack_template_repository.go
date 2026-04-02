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

const tableStackTemplates = "StackTemplates"
const filterTplPKGlobal = odataPartitionKeyEq + pkGlobal + "'"


// StackTemplateRepository implements models.StackTemplateRepository for Azure Table Storage.
// Partition key: "global", Row key: template ID.
type StackTemplateRepository struct {
	client    AzureTableClient
	tableName string
}

// NewStackTemplateRepository creates a new Azure Table Storage stack template repository.
func NewStackTemplateRepository(accountName, accountKey, endpoint string, useAzurite bool) (*StackTemplateRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, tableStackTemplates, useAzurite)
	if err != nil {
		return nil, err
	}
	return &StackTemplateRepository{client: client, tableName: tableStackTemplates}, nil
}

// NewTestStackTemplateRepository creates a repository for unit testing.
func NewTestStackTemplateRepository() *StackTemplateRepository {
	return &StackTemplateRepository{tableName: tableStackTemplates}
}

// SetTestClient injects a mock client for testing.
func (r *StackTemplateRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// stackTemplateEntity is the typed Azure Table entity for stack templates.
type stackTemplateEntity struct {
	PartitionKey  string `json:"PartitionKey"`
	RowKey        string `json:"RowKey"`
	ID            string `json:"ID"`
	Name          string `json:"Name"`
	Description   string `json:"Description"`
	Category      string `json:"Category"`
	Version       string `json:"Version"`
	OwnerID       string `json:"OwnerID"`
	DefaultBranch string `json:"DefaultBranch"`
	IsPublished   bool   `json:"IsPublished"`
	CreatedAt     string `json:"CreatedAt"`
	UpdatedAt     string `json:"UpdatedAt"`
}

func (e *stackTemplateEntity) toModel() *models.StackTemplate {
	t := &models.StackTemplate{
		ID:            e.ID,
		Name:          e.Name,
		Description:   e.Description,
		Category:      e.Category,
		Version:       e.Version,
		OwnerID:       e.OwnerID,
		DefaultBranch: e.DefaultBranch,
		IsPublished:   e.IsPublished,
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339, e.CreatedAt)
	t.UpdatedAt, _ = time.Parse(time.RFC3339, e.UpdatedAt)
	return t
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
		return dberrors.NewDatabaseError(opMarshal, err)
	}

	_, err = r.client.AddEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError(opCreate, err)
	}
	return nil
}

func (r *StackTemplateRepository) FindByID(id string) (*models.StackTemplate, error) {
	ctx := context.Background()

	resp, err := r.client.GetEntity(ctx, pkGlobal, id, nil)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}

	var entity stackTemplateEntity
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError(opUnmarshal, err)
	}
	return entity.toModel(), nil
}

func (r *StackTemplateRepository) Update(template *models.StackTemplate) error {
	ctx := context.Background()
	now := time.Now().UTC()
	template.UpdatedAt = now

	entity := stackTemplateToEntity(template)
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

func (r *StackTemplateRepository) Delete(id string) error {
	ctx := context.Background()

	_, err := r.client.DeleteEntity(ctx, pkGlobal, id, nil)
	if err != nil {
		return mapAzureError(opDelete, err)
	}
	return nil
}

func (r *StackTemplateRepository) List() ([]models.StackTemplate, error) {
	ctx := context.Background()

	filter := filterTplPKGlobal
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[stackTemplateEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError(opList, err)
	}

	results := make([]models.StackTemplate, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}
	return results, nil
}

func (r *StackTemplateRepository) Count() (int64, error) {
	items, err := r.List()
	if err != nil {
		return 0, err
	}
	return int64(len(items)), nil
}

// ListPaged returns a page of stack templates ordered by created_at DESC,
// along with the total count. Azure Table has no LIMIT/OFFSET so this fetches
// all rows and slices in memory.
func (r *StackTemplateRepository) ListPaged(limit, offset int) ([]models.StackTemplate, int64, error) {
	all, err := r.List()
	if err != nil {
		return nil, 0, err
	}
	total := int64(len(all))
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	if offset >= len(all) {
		return []models.StackTemplate{}, total, nil
	}
	all = all[offset:]
	if limit < len(all) {
		all = all[:limit]
	}
	return all, total, nil
}

// ListPublishedPaged returns a page of published stack templates ordered by
// created_at DESC. Azure Table has no LIMIT/OFFSET so this fetches all
// published rows and slices in memory.
func (r *StackTemplateRepository) ListPublishedPaged(limit, offset int) ([]models.StackTemplate, int64, error) {
	all, err := r.ListPublished()
	if err != nil {
		return nil, 0, err
	}
	total := int64(len(all))
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	if offset >= len(all) {
		return []models.StackTemplate{}, total, nil
	}
	all = all[offset:]
	if limit < len(all) {
		all = all[:limit]
	}
	return all, total, nil
}

func (r *StackTemplateRepository) ListPublished() ([]models.StackTemplate, error) {
	ctx := context.Background()

	filter := filterTplPKGlobal + " and IsPublished eq true"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[stackTemplateEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError("list_published", err)
	}

	results := make([]models.StackTemplate, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}
	return results, nil
}

func (r *StackTemplateRepository) ListByOwner(ownerID string) ([]models.StackTemplate, error) {
	ctx := context.Background()

	filter := filterTplPKGlobal + " and OwnerID eq '" + ownerID + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[stackTemplateEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError("list_by_owner", err)
	}

	results := make([]models.StackTemplate, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}
	return results, nil
}

func stackTemplateToEntity(t *models.StackTemplate) map[string]interface{} {
	return map[string]interface{}{
		"PartitionKey":  pkGlobal,
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
