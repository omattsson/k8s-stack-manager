package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/google/uuid"
)

// AuditLogRepository implements models.AuditLogRepository for Azure Table Storage.
// Partition key: YYYY-MM (from timestamp), Row key: reverse_timestamp + uuid.
// This ensures recent-first ordering within each monthly partition.
type AuditLogRepository struct {
	client    AzureTableClient
	tableName string
}

// NewAuditLogRepository creates a new Azure Table Storage audit log repository.
func NewAuditLogRepository(accountName, accountKey, endpoint string, useAzurite bool) (*AuditLogRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "AuditLogs", useAzurite)
	if err != nil {
		return nil, err
	}
	return &AuditLogRepository{client: client, tableName: "AuditLogs"}, nil
}

// NewTestAuditLogRepository creates a repository for unit testing.
func NewTestAuditLogRepository() *AuditLogRepository {
	return &AuditLogRepository{tableName: "AuditLogs"}
}

// SetTestClient injects a mock client for testing.
func (r *AuditLogRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// reverseTimestamp generates a reverse timestamp string for recent-first ordering.
func reverseTimestamp(t time.Time) string {
	return fmt.Sprintf("%020d", math.MaxInt64-t.UnixNano())
}

func (r *AuditLogRepository) Create(log *models.AuditLog) error {
	ctx := context.Background()

	if log.Timestamp.IsZero() {
		log.Timestamp = time.Now().UTC()
	}
	if log.ID == "" {
		log.ID = uuid.New().String()
	}

	pk := log.Timestamp.Format("2006-01")
	rk := reverseTimestamp(log.Timestamp) + "_" + log.ID

	entity := map[string]interface{}{
		"PartitionKey": pk,
		"RowKey":       rk,
		"ID":           log.ID,
		"UserID":       log.UserID,
		"Username":     log.Username,
		"Action":       log.Action,
		"EntityType":   log.EntityType,
		"EntityID":     log.EntityID,
		"Details":      log.Details,
		"Timestamp":    log.Timestamp.Format(time.RFC3339),
	}

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

func (r *AuditLogRepository) List(filters models.AuditLogFilters) ([]models.AuditLog, int64, error) {
	ctx := context.Background()

	// Build filter parts.
	var filterParts []string

	// Date range maps to partition key range.
	if filters.StartDate != nil && filters.EndDate != nil {
		startPK := filters.StartDate.Format("2006-01")
		endPK := filters.EndDate.Format("2006-01")
		filterParts = append(filterParts, "PartitionKey ge '"+startPK+"' and PartitionKey le '"+endPK+"'")
	} else if filters.StartDate != nil {
		startPK := filters.StartDate.Format("2006-01")
		filterParts = append(filterParts, "PartitionKey ge '"+startPK+"'")
	} else if filters.EndDate != nil {
		endPK := filters.EndDate.Format("2006-01")
		filterParts = append(filterParts, "PartitionKey le '"+endPK+"'")
	}

	if filters.UserID != "" {
		filterParts = append(filterParts, "UserID eq '"+filters.UserID+"'")
	}
	if filters.EntityType != "" {
		filterParts = append(filterParts, "EntityType eq '"+filters.EntityType+"'")
	}
	if filters.EntityID != "" {
		filterParts = append(filterParts, "EntityID eq '"+filters.EntityID+"'")
	}
	if filters.Action != "" {
		filterParts = append(filterParts, "Action eq '"+filters.Action+"'")
	}

	var opts *aztables.ListEntitiesOptions
	if len(filterParts) > 0 {
		combined := filterParts[0]
		for i := 1; i < len(filterParts); i++ {
			combined += " and " + filterParts[i]
		}
		opts = &aztables.ListEntitiesOptions{Filter: &combined}
	}

	pager := r.client.NewListEntitiesPager(opts)

	// Apply fine-grained timestamp filtering client-side if needed.
	filterFn := func(e map[string]interface{}) bool {
		ts := parseTime(e, "Timestamp")
		if filters.StartDate != nil && ts.Before(*filters.StartDate) {
			return false
		}
		if filters.EndDate != nil && ts.After(*filters.EndDate) {
			return false
		}
		return true
	}

	// Only apply timestamp filter if dates are set (partition key filtering is coarse).
	var fn func(map[string]interface{}) bool
	if filters.StartDate != nil || filters.EndDate != nil {
		fn = filterFn
	}

	entities, err := collectEntities(ctx, pager, fn)
	if err != nil {
		return nil, 0, mapAzureError("list", err)
	}

	total := int64(len(entities))

	// Apply offset and limit for pagination.
	offset := filters.Offset
	if offset > len(entities) {
		offset = len(entities)
	}
	entities = entities[offset:]

	limit := filters.Limit
	if limit > 0 && limit < len(entities) {
		entities = entities[:limit]
	}

	results := make([]models.AuditLog, 0, len(entities))
	for _, e := range entities {
		results = append(results, *auditLogFromEntity(e))
	}
	return results, total, nil
}

func auditLogFromEntity(e map[string]interface{}) *models.AuditLog {
	return &models.AuditLog{
		ID:         getString(e, "ID"),
		UserID:     getString(e, "UserID"),
		Username:   getString(e, "Username"),
		Action:     getString(e, "Action"),
		EntityType: getString(e, "EntityType"),
		EntityID:   getString(e, "EntityID"),
		Details:    getString(e, "Details"),
		Timestamp:  parseTime(e, "Timestamp"),
	}
}
