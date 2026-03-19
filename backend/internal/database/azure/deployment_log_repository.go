package azure

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// escapeODataString escapes single quotes in a string value for use in OData
// filter expressions. OData uses doubled single quotes ('') as the escape
// sequence for a literal single quote within a string literal.
func escapeODataString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// DeploymentLogRepository implements models.DeploymentLogRepository for Azure Table Storage.
// Partition key: stack_instance_id, Row key: reverse_timestamp + uuid.
// This ensures recent-first ordering within each instance partition.
//
// Known limitation: All methods use context.Background() internally because the
// DeploymentLogRepository interface (models.DeploymentLogRepository) does not
// accept context parameters, unlike the main Repository interface. Adding context
// support requires a broader interface refactor tracked for a future release.
type DeploymentLogRepository struct {
	client    AzureTableClient
	tableName string
}

// NewDeploymentLogRepository creates a new Azure Table Storage deployment log repository.
func NewDeploymentLogRepository(accountName, accountKey, endpoint string, useAzurite bool) (*DeploymentLogRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "DeploymentLogs", useAzurite)
	if err != nil {
		return nil, err
	}
	return &DeploymentLogRepository{client: client, tableName: "DeploymentLogs"}, nil
}

// NewTestDeploymentLogRepository creates a repository for unit testing.
func NewTestDeploymentLogRepository() *DeploymentLogRepository {
	return &DeploymentLogRepository{tableName: "DeploymentLogs"}
}

// SetTestClient injects a mock client for testing.
func (r *DeploymentLogRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

func (r *DeploymentLogRepository) Create(log *models.DeploymentLog) error {
	ctx := context.Background()

	if log.StartedAt.IsZero() {
		log.StartedAt = time.Now().UTC()
	}
	if log.ID == "" {
		log.ID = newID()
	}

	pk := log.StackInstanceID
	rk := reverseTimestamp(log.StartedAt) + "_" + log.ID

	entity := deploymentLogToEntity(log, pk, rk)
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

func (r *DeploymentLogRepository) FindByID(id string) (*models.DeploymentLog, error) {
	ctx := context.Background()

	// No secondary index available; scan all partitions filtering by ID property.
	filter := "ID eq '" + escapeODataString(id) + "'"
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

	return deploymentLogFromEntity(entities[0]), nil
}

func (r *DeploymentLogRepository) Update(log *models.DeploymentLog) error {
	ctx := context.Background()

	// Reconstruct the row key by scanning for the entity first.
	filter := "PartitionKey eq '" + escapeODataString(log.StackInstanceID) + "' and ID eq '" + escapeODataString(log.ID) + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return mapAzureError("update_lookup", err)
	}
	if len(entities) == 0 {
		return dberrors.NewDatabaseError("update", dberrors.ErrNotFound)
	}

	// Use the original PK/RK from the found entity.
	pk := getString(entities[0], "PartitionKey")
	rk := getString(entities[0], "RowKey")

	entity := deploymentLogToEntity(log, pk, rk)
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

func (r *DeploymentLogRepository) ListByInstance(instanceID string) ([]models.DeploymentLog, error) {
	ctx := context.Background()

	filter := "PartitionKey eq '" + escapeODataString(instanceID) + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("list_by_instance", err)
	}

	results := make([]models.DeploymentLog, 0, len(entities))
	for _, e := range entities {
		results = append(results, *deploymentLogFromEntity(e))
	}
	return results, nil
}

func (r *DeploymentLogRepository) GetLatestByInstance(instanceID string) (*models.DeploymentLog, error) {
	ctx := context.Background()

	filter := "PartitionKey eq '" + escapeODataString(instanceID) + "'"
	top := int32(1)
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
		Top:    &top,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, mapAzureError("get_latest", err)
	}

	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("get_latest", dberrors.ErrNotFound)
	}

	return deploymentLogFromEntity(entities[0]), nil
}

func deploymentLogToEntity(log *models.DeploymentLog, pk, rk string) map[string]interface{} {
	entity := map[string]interface{}{
		"PartitionKey":    pk,
		"RowKey":          rk,
		"ID":              log.ID,
		"StackInstanceID": log.StackInstanceID,
		"Action":          log.Action,
		"Status":          log.Status,
		"Output":          log.Output,
		"ErrorMessage":    log.ErrorMessage,
		"StartedAt":       log.StartedAt.Format(time.RFC3339),
	}
	if log.CompletedAt != nil {
		entity["CompletedAt"] = log.CompletedAt.Format(time.RFC3339)
	}
	return entity
}

func deploymentLogFromEntity(e map[string]interface{}) *models.DeploymentLog {
	log := &models.DeploymentLog{
		ID:              getString(e, "ID"),
		StackInstanceID: getString(e, "StackInstanceID"),
		Action:          getString(e, "Action"),
		Status:          getString(e, "Status"),
		Output:          getString(e, "Output"),
		ErrorMessage:    getString(e, "ErrorMessage"),
		StartedAt:       parseTime(e, "StartedAt"),
	}
	if s := getString(e, "CompletedAt"); s != "" {
		t := parseTime(e, "CompletedAt")
		log.CompletedAt = &t
	}
	return log
}
