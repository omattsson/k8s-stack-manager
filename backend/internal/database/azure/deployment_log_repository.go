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

// Deployment log repository constants.
const (
	tableDeploymentLogs = "DeploymentLogs"
	fieldDLAction       = "Action"
	fieldDLStatus       = "Status"
	fieldDLStartedAt    = "StartedAt"
	fieldDLPartitionKey = "PartitionKey"
	fieldDLRowKey       = "RowKey"
)


// escapeODataString escapes single quotes in a string value for use in OData
// filter expressions. OData uses doubled single quotes (”) as the escape
// sequence for a literal single quote within a string literal.
func escapeODataString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// DeploymentLogRepository implements models.DeploymentLogRepository for Azure Table Storage.
// Partition key: stack_instance_id, Row key: reverse_timestamp + uuid.
// This ensures recent-first ordering within each instance partition.
type DeploymentLogRepository struct {
	client    AzureTableClient
	tableName string
}

// NewDeploymentLogRepository creates a new Azure Table Storage deployment log repository.
func NewDeploymentLogRepository(accountName, accountKey, endpoint string, useAzurite bool) (*DeploymentLogRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, tableDeploymentLogs, useAzurite)
	if err != nil {
		return nil, err
	}
	return &DeploymentLogRepository{client: client, tableName: tableDeploymentLogs}, nil
}

// NewTestDeploymentLogRepository creates a repository for unit testing.
func NewTestDeploymentLogRepository() *DeploymentLogRepository {
	return &DeploymentLogRepository{tableName: tableDeploymentLogs}
}

// SetTestClient injects a mock client for testing.
func (r *DeploymentLogRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// deploymentLogEntity is the typed Azure Table entity for deployment logs.
type deploymentLogEntity struct {
	PartitionKey    string `json:"PartitionKey"`
	RowKey          string `json:"RowKey"`
	ID              string `json:"ID"`
	StackInstanceID string `json:"StackInstanceID"`
	Action          string `json:"Action"`
	Status          string `json:"Status"`
	Output          string `json:"Output"`
	ErrorMessage    string `json:"ErrorMessage"`
	StartedAt       string `json:"StartedAt"`
	CompletedAt     string `json:"CompletedAt,omitempty"`
}

func (e *deploymentLogEntity) toModel() *models.DeploymentLog {
	log := &models.DeploymentLog{
		ID:              e.ID,
		StackInstanceID: e.StackInstanceID,
		Action:          e.Action,
		Status:          e.Status,
		Output:          e.Output,
		ErrorMessage:    e.ErrorMessage,
	}
	log.StartedAt, _ = time.Parse(time.RFC3339, e.StartedAt)
	if e.CompletedAt != "" {
		t, err := time.Parse(time.RFC3339, e.CompletedAt)
		if err == nil {
			log.CompletedAt = &t
		}
	}
	return log
}

func (r *DeploymentLogRepository) Create(ctx context.Context, log *models.DeploymentLog) error {

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
		return dberrors.NewDatabaseError(opMarshal, err)
	}

	_, err = r.client.AddEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError(opCreate, err)
	}
	return nil
}

func (r *DeploymentLogRepository) FindByID(ctx context.Context, id string) (*models.DeploymentLog, error) {

	// No secondary index available; scan all partitions filtering by ID property.
	filter := "ID eq '" + escapeODataString(id) + "'"
	top := int32(1)
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
		Top:    &top,
	})

	entities, err := collectEntitiesTyped[deploymentLogEntity](ctx, pager, nil, 1)
	if err != nil {
		return nil, mapAzureError("find_by_id", err)
	}

	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("find_by_id", dberrors.ErrNotFound)
	}

	return entities[0].toModel(), nil
}

func (r *DeploymentLogRepository) Update(ctx context.Context, log *models.DeploymentLog) error {

	// Reconstruct PK/RK directly from the log fields (same formula as Create)
	// to avoid an O(partition) scan query.
	pk := log.StackInstanceID
	rk := reverseTimestamp(log.StartedAt) + "_" + log.ID

	entity := deploymentLogToEntity(log, pk, rk)
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

func (r *DeploymentLogRepository) ListByInstance(ctx context.Context, instanceID string) ([]models.DeploymentLog, error) {

	filter := odataPartitionKeyEq + escapeODataString(instanceID) + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[deploymentLogEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError("list_by_instance", err)
	}

	results := make([]models.DeploymentLog, 0, len(entities))
	for _, e := range entities {
		results = append(results, *e.toModel())
	}
	return results, nil
}

func (r *DeploymentLogRepository) GetLatestByInstance(ctx context.Context, instanceID string) (*models.DeploymentLog, error) {

	filter := odataPartitionKeyEq + escapeODataString(instanceID) + "'"
	top := int32(1)
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
		Top:    &top,
	})

	entities, err := collectEntitiesTyped[deploymentLogEntity](ctx, pager, nil, 1)
	if err != nil {
		return nil, mapAzureError("get_latest", err)
	}

	if len(entities) == 0 {
		return nil, dberrors.NewDatabaseError("get_latest", dberrors.ErrNotFound)
	}

	return entities[0].toModel(), nil
}

// deployLogSummaryEntity is a lightweight Azure Table entity that only
// deserializes the fields needed for counting — it intentionally omits the
// heavy Output and ErrorMessage columns.
type deployLogSummaryEntity struct {
	PartitionKey string `json:"PartitionKey"`
	RowKey       string `json:"RowKey"`
	Action       string `json:"Action"`
	Status       string `json:"Status"`
	StartedAt    string `json:"StartedAt"`
	CompletedAt  string `json:"CompletedAt,omitempty"`
}

// SummarizeByInstance returns aggregate deploy-action counts and the latest
// activity timestamp for the given instance. Only logs from the last 90 days
// are considered, using a RowKey upper-bound filter (RowKeys are reverse-
// timestamp based, so recent rows have smaller RowKey values).
func (r *DeploymentLogRepository) SummarizeByInstance(ctx context.Context, instanceID string) (*models.DeployLogSummary, error) {
	// RowKey format: reverseTimestamp(startedAt) + "_" + id
	// reverseTimestamp = fmt.Sprintf("%020d", math.MaxInt64 - t.UnixNano())
	// To filter for the last 90 days we need RowKey < reverseTimestamp(now - 90 days).
	// Since reverse timestamps are smaller for more recent times, recent rows
	// have RowKey < cutoffRK.
	cutoff := time.Now().UTC().Add(-90 * 24 * time.Hour)
	cutoffRK := reverseTimestamp(cutoff)

	filter := odataPartitionKeyEq + escapeODataString(instanceID) + "' and RowKey lt '" + cutoffRK + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[deployLogSummaryEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, mapAzureError("summarize_by_instance", err)
	}

	summary := &models.DeployLogSummary{InstanceID: instanceID}
	for _, e := range entities {
		accumulateDeployLogSummary(summary, &e)
	}

	return summary, nil
}

// accumulateDeployLogSummary updates the summary counters and latest timestamp
// from a single deployment log entity.
func accumulateDeployLogSummary(summary *models.DeployLogSummary, e *deployLogSummaryEntity) {
	// Only deploy actions contribute to deploy-related counters.
	if e.Action == models.DeployActionDeploy {
		summary.DeployCount++
		switch e.Status {
		case models.DeployLogSuccess:
			summary.SuccessCount++
		case models.DeployLogError:
			summary.ErrorCount++
		}
	}

	// Track the latest timestamp across all actions (deploy, stop, clean, etc.).
	ts := parseDeployLogTimestamp(e.CompletedAt, e.StartedAt)
	if !ts.IsZero() && (summary.LastDeployAt == nil || ts.After(*summary.LastDeployAt)) {
		cp := ts
		summary.LastDeployAt = &cp
	}
}

// parseDeployLogTimestamp returns the best available timestamp from a deployment log,
// preferring CompletedAt over StartedAt. Returns zero time if neither parses.
func parseDeployLogTimestamp(completedAt, startedAt string) time.Time {
	if completedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, completedAt); err == nil {
			return parsed
		}
	}
	if startedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, startedAt); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func deploymentLogToEntity(log *models.DeploymentLog, pk, rk string) map[string]interface{} {
	entity := map[string]interface{}{
		fieldDLPartitionKey:    pk,
		fieldDLRowKey:          rk,
		"ID":              log.ID,
		"StackInstanceID": log.StackInstanceID,
		fieldDLAction:          log.Action,
		fieldDLStatus:          log.Status,
		"Output":          log.Output,
		"ErrorMessage":    log.ErrorMessage,
		fieldDLStartedAt:       log.StartedAt.Format(time.RFC3339),
	}
	if log.CompletedAt != nil {
		entity["CompletedAt"] = log.CompletedAt.Format(time.RFC3339)
	}
	return entity
}
