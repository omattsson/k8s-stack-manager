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

const tableTemplateVersions = "TemplateVersions"


// TemplateVersionRepository implements models.TemplateVersionRepository for Azure Table Storage.
// Partition key: template_id, Row key: version_id.
type TemplateVersionRepository struct {
	client    AzureTableClient
	tableName string
}

// NewTestTemplateVersionRepository creates a repository for unit testing.
func NewTestTemplateVersionRepository() *TemplateVersionRepository {
	return &TemplateVersionRepository{tableName: tableTemplateVersions}
}

// SetTestClient injects a mock client for testing.
func (r *TemplateVersionRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// templateVersionEntity represents the Azure Table Storage entity for a template version.
type templateVersionEntity struct {
	PartitionKey  string `json:"PartitionKey"`
	RowKey        string `json:"RowKey"`
	TemplateID    string `json:"TemplateID"`
	Version       string `json:"Version"`
	Snapshot      string `json:"Snapshot"`
	ChangeSummary string `json:"ChangeSummary"`
	CreatedBy     string `json:"CreatedBy"`
	CreatedAt     string `json:"CreatedAt"`
}

// NewTemplateVersionRepository creates a new Azure Table Storage template version repository.
func NewTemplateVersionRepository(accountName, accountKey, endpoint string, useAzurite bool) (*TemplateVersionRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, tableTemplateVersions, useAzurite)
	if err != nil {
		return nil, err
	}
	return &TemplateVersionRepository{client: client, tableName: tableTemplateVersions}, nil
}

func templateVersionToEntity(v *models.TemplateVersion) *templateVersionEntity {
	return &templateVersionEntity{
		PartitionKey:  v.TemplateID,
		RowKey:        v.ID,
		TemplateID:    v.TemplateID,
		Version:       v.Version,
		Snapshot:      v.Snapshot,
		ChangeSummary: v.ChangeSummary,
		CreatedBy:     v.CreatedBy,
		CreatedAt:     v.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func entityToTemplateVersion(entity *templateVersionEntity) *models.TemplateVersion {
	createdAt, _ := time.Parse(time.RFC3339, entity.CreatedAt)
	return &models.TemplateVersion{
		ID:            entity.RowKey,
		TemplateID:    entity.TemplateID,
		Version:       entity.Version,
		Snapshot:      entity.Snapshot,
		ChangeSummary: entity.ChangeSummary,
		CreatedBy:     entity.CreatedBy,
		CreatedAt:     createdAt,
	}
}

// Create inserts a new template version record.
func (r *TemplateVersionRepository) Create(ctx context.Context, version *models.TemplateVersion) error {
	entity := templateVersionToEntity(version)
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

// ListByTemplate returns all versions for a template, ordered newest first.
func (r *TemplateVersionRepository) ListByTemplate(ctx context.Context, templateID string) ([]models.TemplateVersion, error) {
	filter := "PartitionKey eq '" + escapeODataString(templateID) + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	var versions []models.TemplateVersion
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, dberrors.NewDatabaseError(opList, err)
		}
		for _, raw := range page.Entities {
			var entity templateVersionEntity
			if err := json.Unmarshal(raw, &entity); err != nil {
				return nil, dberrors.NewDatabaseError(opUnmarshal, err)
			}
			versions = append(versions, *entityToTemplateVersion(&entity))
		}
	}

	// Sort by CreatedAt descending.
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].CreatedAt.After(versions[j].CreatedAt)
	})

	return versions, nil
}

// GetByID returns a single template version by its ID.
// Uses templateID as the partition key for an efficient point lookup.
func (r *TemplateVersionRepository) GetByID(ctx context.Context, templateID, id string) (*models.TemplateVersion, error) {
	filter := "PartitionKey eq '" + escapeODataString(templateID) + "' and RowKey eq '" + escapeODataString(id) + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, dberrors.NewDatabaseError(opFind, err)
		}
		for _, raw := range page.Entities {
			var entity templateVersionEntity
			if err := json.Unmarshal(raw, &entity); err != nil {
				return nil, dberrors.NewDatabaseError(opUnmarshal, err)
			}
			return entityToTemplateVersion(&entity), nil
		}
	}

	return nil, dberrors.NewDatabaseError(opFind, dberrors.ErrNotFound)
}

// GetLatestByTemplate returns the most recent version for a template.
func (r *TemplateVersionRepository) GetLatestByTemplate(ctx context.Context, templateID string) (*models.TemplateVersion, error) {
	versions, err := r.ListByTemplate(ctx, templateID)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, dberrors.NewDatabaseError(opFind, dberrors.ErrNotFound)
	}
	// ListByTemplate already returns newest first.
	return &versions[0], nil
}
