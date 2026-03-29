package azure

import (
	"context"
	"encoding/json"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

const cleanupPolicyPartitionKey = "policy"

// CleanupPolicyRepository implements models.CleanupPolicyRepository for Azure Table Storage.
// Partition key: "policy", Row key: policy ID (UUID).
type CleanupPolicyRepository struct {
	client    AzureTableClient
	tableName string
}

// NewCleanupPolicyRepository creates a new Azure Table Storage cleanup policy repository.
func NewCleanupPolicyRepository(accountName, accountKey, endpoint string, useAzurite bool) (*CleanupPolicyRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "CleanupPolicies", useAzurite)
	if err != nil {
		return nil, err
	}
	return &CleanupPolicyRepository{client: client, tableName: "CleanupPolicies"}, nil
}

// NewTestCleanupPolicyRepository creates a repository for unit testing.
func NewTestCleanupPolicyRepository() *CleanupPolicyRepository {
	return &CleanupPolicyRepository{tableName: "CleanupPolicies"}
}

// SetTestClient injects a mock client for testing.
func (r *CleanupPolicyRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// cleanupPolicyEntity is the typed Azure Table entity for cleanup policies.
type cleanupPolicyEntity struct {
	PartitionKey string `json:"PartitionKey"`
	RowKey       string `json:"RowKey"`
	ID           string `json:"ID"`
	Name         string `json:"Name"`
	ClusterID    string `json:"ClusterID"`
	Action       string `json:"Action"`
	Condition    string `json:"Condition"`
	Schedule     string `json:"Schedule"`
	Enabled      bool   `json:"Enabled"`
	DryRun       bool   `json:"DryRun"`
	LastRunAt    string `json:"LastRunAt,omitempty"`
	CreatedAt    string `json:"CreatedAt"`
	UpdatedAt    string `json:"UpdatedAt"`
}

func (e *cleanupPolicyEntity) toModel() *models.CleanupPolicy {
	p := &models.CleanupPolicy{
		ID:        e.ID,
		Name:      e.Name,
		ClusterID: e.ClusterID,
		Action:    e.Action,
		Condition: e.Condition,
		Schedule:  e.Schedule,
		Enabled:   e.Enabled,
		DryRun:    e.DryRun,
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, e.CreatedAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, e.UpdatedAt)
	if e.LastRunAt != "" {
		t, err := time.Parse(time.RFC3339, e.LastRunAt)
		if err == nil {
			p.LastRunAt = &t
		}
	}
	return p
}

func (r *CleanupPolicyRepository) Create(policy *models.CleanupPolicy) error {
	ctx := context.Background()
	now := time.Now().UTC()

	if policy.ID == "" {
		policy.ID = newID()
	}
	policy.CreatedAt = now
	policy.UpdatedAt = now

	if err := policy.Validate(); err != nil {
		return dberrors.NewDatabaseError(err.Error(), dberrors.ErrValidation)
	}

	entity := r.toEntity(policy)
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

func (r *CleanupPolicyRepository) FindByID(id string) (*models.CleanupPolicy, error) {
	ctx := context.Background()

	resp, err := r.client.GetEntity(ctx, cleanupPolicyPartitionKey, id, nil)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}

	var entity cleanupPolicyEntity
	if err := json.Unmarshal(resp.Value, &entity); err != nil {
		return nil, dberrors.NewDatabaseError("unmarshal", err)
	}
	return entity.toModel(), nil
}

func (r *CleanupPolicyRepository) Update(policy *models.CleanupPolicy) error {
	ctx := context.Background()
	policy.UpdatedAt = time.Now().UTC()

	if err := policy.Validate(); err != nil {
		return dberrors.NewDatabaseError(err.Error(), dberrors.ErrValidation)
	}

	entity := r.toEntity(policy)
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

func (r *CleanupPolicyRepository) Delete(id string) error {
	ctx := context.Background()

	_, err := r.client.DeleteEntity(ctx, cleanupPolicyPartitionKey, id, nil)
	if err != nil {
		return mapAzureError("delete", err)
	}
	return nil
}

func (r *CleanupPolicyRepository) List() ([]models.CleanupPolicy, error) {
	ctx := context.Background()

	filter := "PartitionKey eq '" + cleanupPolicyPartitionKey + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[cleanupPolicyEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError("list", err)
	}

	results := make([]models.CleanupPolicy, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}
	return results, nil
}

func (r *CleanupPolicyRepository) ListEnabled() ([]models.CleanupPolicy, error) {
	ctx := context.Background()

	filter := "PartitionKey eq '" + cleanupPolicyPartitionKey + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[cleanupPolicyEntity](ctx, pager, func(e *cleanupPolicyEntity) bool {
		return e.Enabled
	}, 0)
	if err != nil {
		return nil, mapAzureError("list_enabled", err)
	}

	results := make([]models.CleanupPolicy, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}
	return results, nil
}

func (r *CleanupPolicyRepository) toEntity(p *models.CleanupPolicy) map[string]interface{} {
	e := map[string]interface{}{
		"PartitionKey": cleanupPolicyPartitionKey,
		"RowKey":       p.ID,
		"ID":           p.ID,
		"Name":         p.Name,
		"ClusterID":    p.ClusterID,
		"Action":       p.Action,
		"Condition":    p.Condition,
		"Schedule":     p.Schedule,
		"Enabled":      p.Enabled,
		"DryRun":       p.DryRun,
		"CreatedAt":    p.CreatedAt.Format(time.RFC3339),
		"UpdatedAt":    p.UpdatedAt.Format(time.RFC3339),
	}
	if p.LastRunAt != nil {
		e["LastRunAt"] = p.LastRunAt.Format(time.RFC3339)
	}
	return e
}
