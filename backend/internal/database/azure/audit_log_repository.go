package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/google/uuid"
)

// Audit log repository constants.
const (
	tableAuditLogs   = "AuditLogs"
	auditDateFormat  = "2006-01"
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
	client, err := createTableClient(accountName, accountKey, endpoint, tableAuditLogs, useAzurite)
	if err != nil {
		return nil, err
	}
	return &AuditLogRepository{client: client, tableName: tableAuditLogs}, nil
}

// NewTestAuditLogRepository creates a repository for unit testing.
func NewTestAuditLogRepository() *AuditLogRepository {
	return &AuditLogRepository{tableName: tableAuditLogs}
}

// SetTestClient injects a mock client for testing.
func (r *AuditLogRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

// encodeCursor creates an opaque cursor token from a PartitionKey and RowKey.
func encodeCursor(pk, rk string) string {
	return base64.StdEncoding.EncodeToString([]byte(pk + "|" + rk))
}

// decodeCursor extracts PartitionKey and RowKey from an opaque cursor token.
func decodeCursor(cursor string) (pk, rk string, err error) {
	data, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", fmt.Errorf("invalid cursor: %w", err)
	}
	parts := strings.SplitN(string(data), "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid cursor format")
	}
	return parts[0], parts[1], nil
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

	pk := log.Timestamp.Format(auditDateFormat)
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
		return dberrors.NewDatabaseError(opMarshal, err)
	}

	_, err = r.client.AddEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError(opCreate, err)
	}
	return nil
}

func (r *AuditLogRepository) List(filters models.AuditLogFilters) (*models.AuditLogResult, error) {
	ctx := context.Background()

	filterParts, err := buildAuditODataFilters(filters)
	if err != nil {
		return nil, err
	}

	opts := combineODataFilters(filterParts)
	pager := r.client.NewListEntitiesPager(opts)

	// Only apply fine-grained timestamp filter when dates are set
	// (partition key filtering is coarse — monthly granularity).
	var fn func(*auditLogEntity) bool
	if filters.StartDate != nil || filters.EndDate != nil {
		fn = auditTimestampFilter(filters.StartDate, filters.EndDate)
	}

	maxResults := auditMaxResults(filters)
	entities, err := collectEntitiesTyped[auditLogEntity](ctx, pager, fn, maxResults)
	if err != nil {
		return nil, mapAzureError(opList, err)
	}

	return buildAuditLogResult(entities, filters), nil
}

// buildAuditODataFilters constructs OData filter clauses from the given filters.
func buildAuditODataFilters(filters models.AuditLogFilters) ([]string, error) {
	var parts []string

	// Date range maps to partition key range.
	if filters.StartDate != nil && filters.EndDate != nil {
		startPK := filters.StartDate.Format(auditDateFormat)
		endPK := filters.EndDate.Format(auditDateFormat)
		parts = append(parts, "PartitionKey ge '"+startPK+"' and PartitionKey le '"+endPK+"'")
	} else if filters.StartDate != nil {
		startPK := filters.StartDate.Format(auditDateFormat)
		parts = append(parts, "PartitionKey ge '"+startPK+"'")
	} else if filters.EndDate != nil {
		endPK := filters.EndDate.Format(auditDateFormat)
		parts = append(parts, "PartitionKey le '"+endPK+"'")
	}

	if filters.UserID != "" {
		parts = append(parts, "UserID eq '"+escapeODataString(filters.UserID)+"'")
	}
	if filters.EntityType != "" {
		parts = append(parts, "EntityType eq '"+escapeODataString(filters.EntityType)+"'")
	}
	if filters.EntityID != "" {
		parts = append(parts, "EntityID eq '"+escapeODataString(filters.EntityID)+"'")
	}
	if filters.Action != "" {
		parts = append(parts, "Action eq '"+escapeODataString(filters.Action)+"'")
	}

	// Cursor-based pagination: decode the opaque cursor into PK+RK and build a
	// composite filter that correctly handles cross-partition pagination.
	if filters.Cursor != "" {
		cursorPK, cursorRK, err := decodeCursor(filters.Cursor)
		if err != nil {
			return nil, dberrors.NewDatabaseError(opList, fmt.Errorf("%w: %s", dberrors.ErrValidation, err.Error()))
		}
		escapedPK := escapeODataString(cursorPK)
		escapedRK := escapeODataString(cursorRK)
		cursorFilter := fmt.Sprintf(
			"(PartitionKey gt '%s') or (PartitionKey eq '%s' and RowKey gt '%s')",
			escapedPK, escapedPK, escapedRK,
		)
		parts = append(parts, cursorFilter)
	}

	return parts, nil
}

// combineODataFilters joins filter parts with " and " and returns ListEntitiesOptions.
func combineODataFilters(parts []string) *aztables.ListEntitiesOptions {
	if len(parts) == 0 {
		return nil
	}
	combined := parts[0]
	for i := 1; i < len(parts); i++ {
		combined += " and " + parts[i]
	}
	return &aztables.ListEntitiesOptions{Filter: &combined}
}

// auditTimestampFilter returns a client-side filter function that checks
// whether an entity's timestamp falls within the given date range.
func auditTimestampFilter(startDate, endDate *time.Time) func(*auditLogEntity) bool {
	return func(e *auditLogEntity) bool {
		ts, _ := time.Parse(time.RFC3339, e.Timestamp)
		if startDate != nil && ts.Before(*startDate) {
			return false
		}
		if endDate != nil && ts.After(*endDate) {
			return false
		}
		return true
	}
}

// auditMaxResults calculates the maximum entities to fetch based on pagination mode.
func auditMaxResults(filters models.AuditLogFilters) int {
	if filters.Cursor != "" {
		// Cursor mode: fetch limit+1 to detect if more results exist.
		if filters.Limit > 0 {
			return filters.Limit + 1
		}
		return 0
	}
	// Offset/limit mode: fetch offset+limit with early termination.
	if filters.Limit > 0 {
		return filters.Offset + filters.Limit
	}
	return 0
}

// buildAuditLogResult applies pagination (cursor or offset/limit) and converts entities to models.
func buildAuditLogResult(entities []auditLogEntity, filters models.AuditLogFilters) *models.AuditLogResult {
	result := &models.AuditLogResult{}

	if filters.Cursor != "" {
		result.Total = -1
		if filters.Limit > 0 && len(entities) > filters.Limit {
			entities = entities[:filters.Limit]
			lastEntity := entities[filters.Limit-1]
			result.NextCursor = encodeCursor(lastEntity.PartitionKey, lastEntity.RowKey)
		}
	} else {
		applyOffsetLimitPagination(result, &entities, filters)
	}

	result.Data = make([]models.AuditLog, 0, len(entities))
	for _, e := range entities {
		result.Data = append(result.Data, e.toModel())
	}
	return result
}

// applyOffsetLimitPagination sets Total and trims the entities slice for offset/limit mode.
func applyOffsetLimitPagination(result *models.AuditLogResult, entities *[]auditLogEntity, filters models.AuditLogFilters) {
	maxResults := 0
	if filters.Limit > 0 {
		maxResults = filters.Offset + filters.Limit
	}

	if maxResults > 0 && len(*entities) >= maxResults {
		result.Total = -1 // exact total unknown due to early termination
	} else {
		result.Total = int64(len(*entities))
	}

	// Apply offset.
	offset := filters.Offset
	if offset > len(*entities) {
		offset = len(*entities)
	}
	*entities = (*entities)[offset:]

	// Apply limit.
	if filters.Limit > 0 && filters.Limit < len(*entities) {
		*entities = (*entities)[:filters.Limit]
	}
}
