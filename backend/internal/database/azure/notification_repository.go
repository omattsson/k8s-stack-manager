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

// NotificationRepository implements models.NotificationRepository for Azure Table Storage.
// Partition key: UserID, Row key: reverse_timestamp + uuid (newest first).
type NotificationRepository struct {
	client    AzureTableClient
	tableName string
}

// NewNotificationRepository creates a new Azure Table Storage notification repository.
func NewNotificationRepository(accountName, accountKey, endpoint string, useAzurite bool) (*NotificationRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, "Notifications", useAzurite)
	if err != nil {
		return nil, err
	}
	return &NotificationRepository{client: client, tableName: "Notifications"}, nil
}

// NewTestNotificationRepository creates a repository for unit testing without connecting.
func NewTestNotificationRepository() *NotificationRepository {
	return &NotificationRepository{tableName: "Notifications"}
}

// SetTestClient injects a mock client for testing.
func (r *NotificationRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

func notificationReverseTimestamp(t time.Time) string {
	return fmt.Sprintf("%020d", math.MaxInt64-t.UnixNano())
}

func (r *NotificationRepository) Create(ctx context.Context, notification *models.Notification) error {
	if notification.ID == "" {
		notification.ID = uuid.New().String()
	}
	if notification.CreatedAt.IsZero() {
		notification.CreatedAt = time.Now().UTC()
	}

	rk := notificationReverseTimestamp(notification.CreatedAt) + "_" + notification.ID

	entity := map[string]interface{}{
		"PartitionKey": notification.UserID,
		"RowKey":       rk,
		"ID":           notification.ID,
		"UserID":       notification.UserID,
		"Type":         notification.Type,
		"Title":        notification.Title,
		"Message":      notification.Message,
		"IsRead":       notification.IsRead,
		"EntityType":   notification.EntityType,
		"EntityID":     notification.EntityID,
		"CreatedAt":    notification.CreatedAt.Format(time.RFC3339),
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

func (r *NotificationRepository) ListByUser(ctx context.Context, userID string, unreadOnly bool, limit, offset int) ([]models.Notification, int64, error) {
	filter := "PartitionKey eq '" + escapeODataString(userID) + "'"
	if unreadOnly {
		filter += " and IsRead eq false"
	}

	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return nil, 0, mapAzureError("list", err)
	}

	total := int64(len(entities))

	// Apply offset and limit.
	if offset > len(entities) {
		offset = len(entities)
	}
	entities = entities[offset:]
	if limit > 0 && limit < len(entities) {
		entities = entities[:limit]
	}

	results := make([]models.Notification, 0, len(entities))
	for _, e := range entities {
		results = append(results, *notificationFromEntity(e))
	}
	return results, total, nil
}

func (r *NotificationRepository) CountUnread(ctx context.Context, userID string) (int64, error) {
	filter := "PartitionKey eq '" + escapeODataString(userID) + "' and IsRead eq false"

	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return 0, mapAzureError("count_unread", err)
	}
	return int64(len(entities)), nil
}

func (r *NotificationRepository) MarkAsRead(ctx context.Context, id string, userID string) error {
	// Find the entity first by scanning the user's partition.
	filter := "PartitionKey eq '" + escapeODataString(userID) + "' and ID eq '" + escapeODataString(id) + "'"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return mapAzureError("mark_read", err)
	}
	if len(entities) == 0 {
		return dberrors.NewDatabaseError("mark_read", dberrors.ErrNotFound)
	}

	e := entities[0]
	e["IsRead"] = true

	entityBytes, err := json.Marshal(e)
	if err != nil {
		return dberrors.NewDatabaseError("marshal", err)
	}

	_, err = r.client.UpsertEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError("mark_read", err)
	}
	return nil
}

func (r *NotificationRepository) MarkAllAsRead(ctx context.Context, userID string) error {
	filter := "PartitionKey eq '" + escapeODataString(userID) + "' and IsRead eq false"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntities(ctx, pager, nil)
	if err != nil {
		return mapAzureError("mark_all_read", err)
	}

	for _, e := range entities {
		e["IsRead"] = true
		entityBytes, marshalErr := json.Marshal(e)
		if marshalErr != nil {
			return dberrors.NewDatabaseError("marshal", marshalErr)
		}
		if _, upsertErr := r.client.UpsertEntity(ctx, entityBytes, nil); upsertErr != nil {
			return mapAzureError("mark_all_read", upsertErr)
		}
	}
	return nil
}

// NotificationPreferenceRepository implements the preferences part of
// NotificationRepository. For simplicity we use a separate Azure table.
// However, both are combined into a single repository struct below.

func (r *NotificationRepository) GetPreferences(_ context.Context, _ string) ([]models.NotificationPreference, error) {
	// Preferences are not yet implemented for Azure Table Storage.
	return nil, dberrors.NewDatabaseError("get_preferences", dberrors.ErrNotImplemented)
}

func (r *NotificationRepository) UpdatePreference(_ context.Context, _ *models.NotificationPreference) error {
	// Preferences are not yet implemented for Azure Table Storage.
	return dberrors.NewDatabaseError("update_preference", dberrors.ErrNotImplemented)
}

func notificationFromEntity(e map[string]interface{}) *models.Notification {
	return &models.Notification{
		ID:         getString(e, "ID"),
		UserID:     getString(e, "UserID"),
		Type:       getString(e, "Type"),
		Title:      getString(e, "Title"),
		Message:    getString(e, "Message"),
		IsRead:     getBool(e, "IsRead"),
		EntityType: getString(e, "EntityType"),
		EntityID:   getString(e, "EntityID"),
		CreatedAt:  parseTime(e, "CreatedAt"),
	}
}
