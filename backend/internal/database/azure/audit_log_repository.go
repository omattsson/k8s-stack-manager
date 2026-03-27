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

// auditLogEntity is the typed Azure Table entity for audit logs.
type auditLogEntity struct {
	PartitionKey string `json:"PartitionKey"`
	RowKey       string `json:"RowKey"`
	ID           string `json:"ID"`
	UserID       string `json:"UserID"`
	Username     string `json:"Username"`
	Action       string `json:"Action"`
	EntityType   string `json:"EntityType"`
	EntityID     string `json:"EntityID"`
	Details      string `json:"Details"`
	Timestamp    string `json:"Timestamp"`
}

func (e *auditLogEntity) toModel() models.AuditLog {
	t, _ := time.Parse(time.RFC3339, e.Timestamp)
	return models.AuditLog{
		ID:         e.ID,
		UserID:     e.UserID,
		Username:   e.Username,
		Action:     e.Action,
		EntityType: e.EntityType,
		EntityID:   e.EntityID,
		Details:    e.Details,
		Timestamp:  t,
	}
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

func (r *AuditLogRepository) List(filters models.AuditLogFilters) (*models.AuditLogResult, error) {
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
		filterParts = append(filterParts, "UserID eq '"+escapeODataString(filters.UserID)+"'")
	}
	if filters.EntityType != "" {
		filterParts = append(filterParts, "EntityType eq '"+escapeODataString(filters.EntityType)+"'")
	}
	if filters.EntityID != "" {
		filterParts = append(filterParts, "EntityID eq '"+escapeODataString(filters.EntityID)+"'")
	}
	if filters.Action != "" {
		filterParts = append(filterParts, "Action eq '"+escapeODataString(filters.Action)+"'")
	}

	// Cursor-based pagination: skip to entities after the cursor RowKey.
	// RowKeys are reverse-timestamp based, so lexicographic gt skips older entries already seen.
	if filters.Cursor != "" {
		filterParts = append(filterParts, "RowKey gt '"+escapeODataString(filters.Cursor)+"'")
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
	filterFn := func(e *auditLogEntity) bool {
		ts, _ := time.Parse(time.RFC3339, e.Timestamp)
		if filters.StartDate != nil && ts.Before(*filters.StartDate) {
			return false
		}
		if filters.EndDate != nil && ts.After(*filters.EndDate) {
			return false
		}
		return true
	}

	// Only apply timestamp filter if dates are set (partition key filtering is coarse).
	var fn func(*auditLogEntity) bool
	if filters.StartDate != nil || filters.EndDate != nil {
		fn = filterFn
	}

	// Determine how many entities to collect based on pagination mode.
	var maxResults int
	useCursor := filters.Cursor != ""

	if useCursor {
		// Cursor mode: fetch limit+1 to detect if more results exist.
		if filters.Limit > 0 {
			maxResults = filters.Limit + 1
		}
	} else {
		// Offset/limit mode: fetch offset+limit with early termination.
		if filters.Limit > 0 {
			maxResults = filters.Offset + filters.Limit
		}
	}

	entities, err := collectEntitiesTyped[auditLogEntity](ctx, pager, fn, maxResults)
	if err != nil {
		return nil, mapAzureError("list", err)
	}

	result := &models.AuditLogResult{}

	if useCursor {
		// Cursor-based: total is unknown (would require full scan).
		result.Total = -1

		if filters.Limit > 0 && len(entities) > filters.Limit {
			// More results exist beyond this page.
			entities = entities[:filters.Limit]
			lastEntity := entities[filters.Limit-1]
			result.NextCursor = lastEntity.RowKey
		}
	} else {
		// Offset/limit mode: if we hit maxResults, total is at least that many (but unknown exact).
		// If we got fewer than maxResults, we know the exact total.
		if maxResults > 0 && len(entities) >= maxResults {
			result.Total = int64(len(entities))
		} else {
			result.Total = int64(len(entities))
		}

		// Apply offset.
		offset := filters.Offset
		if offset > len(entities) {
			offset = len(entities)
		}
		entities = entities[offset:]

		// Apply limit.
		if filters.Limit > 0 && filters.Limit < len(entities) {
			entities = entities[:filters.Limit]
		}
	}

	result.Data = make([]models.AuditLog, 0, len(entities))
	for _, e := range entities {
		result.Data = append(result.Data, e.toModel())
	}
	return result, nil
}
