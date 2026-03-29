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

const tableNotifications = "Notifications"
const opMarkRead = "mark_read"


// NotificationRepository implements models.NotificationRepository for Azure Table Storage.
// Partition key: UserID, Row key: reverse_timestamp + uuid (newest first).
type NotificationRepository struct {
	client    AzureTableClient
	tableName string
}

// NewNotificationRepository creates a new Azure Table Storage notification repository.
func NewNotificationRepository(accountName, accountKey, endpoint string, useAzurite bool) (*NotificationRepository, error) {
	client, err := createTableClient(accountName, accountKey, endpoint, tableNotifications, useAzurite)
	if err != nil {
		return nil, err
	}
	return &NotificationRepository{client: client, tableName: tableNotifications}, nil
}

// NewTestNotificationRepository creates a repository for unit testing without connecting.
func NewTestNotificationRepository() *NotificationRepository {
	return &NotificationRepository{tableName: tableNotifications}
}

// SetTestClient injects a mock client for testing.
func (r *NotificationRepository) SetTestClient(client AzureTableClient) {
	r.client = client
}

func notificationReverseTimestamp(t time.Time) string {
	return fmt.Sprintf("%020d", math.MaxInt64-t.UnixNano())
}

// notificationEntity is the typed Azure Table entity for notifications.
type notificationEntity struct {
	PartitionKey string `json:"PartitionKey"`
	RowKey       string `json:"RowKey"`
	ID           string `json:"ID"`
	UserID       string `json:"UserID"`
	Type         string `json:"Type"`
	Title        string `json:"Title"`
	Message      string `json:"Message"`
	IsRead       bool   `json:"IsRead"`
	EntityType   string `json:"EntityType"`
	EntityID     string `json:"EntityID"`
	CreatedAt    string `json:"CreatedAt"`
}

func (e *notificationEntity) toModel() *models.Notification {
	createdAt, _ := time.Parse(time.RFC3339, e.CreatedAt)
	return &models.Notification{
		ID:         e.ID,
		UserID:     e.UserID,
		Type:       e.Type,
		Title:      e.Title,
		Message:    e.Message,
		IsRead:     e.IsRead,
		EntityType: e.EntityType,
		EntityID:   e.EntityID,
		CreatedAt:  createdAt,
	}
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
		return dberrors.NewDatabaseError(opMarshal, err)
	}

	_, err = r.client.AddEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError(opCreate, err)
	}
	return nil
}

func (r *NotificationRepository) ListByUser(ctx context.Context, userID string, unreadOnly bool, limit, offset int) ([]models.Notification, int64, error) {
	filter := odataPartitionKeyEq + escapeODataString(userID) + "'"
	if unreadOnly {
		filter += " and IsRead eq false"
	}

	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[notificationEntity](ctx, pager, nil, 0)
	if err != nil {
		return nil, 0, mapAzureError(opList, err)
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
		results = append(results, *e.toModel())
	}
	return results, total, nil
}

func (r *NotificationRepository) CountUnread(ctx context.Context, userID string) (int64, error) {
	filter := odataPartitionKeyEq + escapeODataString(userID) + "' and IsRead eq false"

	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[notificationEntity](ctx, pager, nil, 0)
	if err != nil {
		return 0, mapAzureError("count_unread", err)
	}
	return int64(len(entities)), nil
}

func (r *NotificationRepository) MarkAsRead(ctx context.Context, id string, userID string) error {
	// Find the entity first by scanning the user's partition.
	filter := odataPartitionKeyEq + escapeODataString(userID) + "' and ID eq '" + escapeODataString(id) + "'"
	top := int32(1)
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
		Top:    &top,
	})

	entities, err := collectEntitiesTyped[notificationEntity](ctx, pager, nil, 1)
	if err != nil {
		return mapAzureError(opMarkRead, err)
	}
	if len(entities) == 0 {
		return dberrors.NewDatabaseError(opMarkRead, dberrors.ErrNotFound)
	}

	e := entities[0]
	e.IsRead = true

	entityBytes, err := json.Marshal(e)
	if err != nil {
		return dberrors.NewDatabaseError(opMarshal, err)
	}

	_, err = r.client.UpsertEntity(ctx, entityBytes, nil)
	if err != nil {
		return mapAzureError(opMarkRead, err)
	}
	return nil
}

func (r *NotificationRepository) MarkAllAsRead(ctx context.Context, userID string) error {
	filter := odataPartitionKeyEq + escapeODataString(userID) + "' and IsRead eq false"
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{
		Filter: &filter,
	})

	entities, err := collectEntitiesTyped[notificationEntity](ctx, pager, nil, 0)
	if err != nil {
		return mapAzureError("mark_all_read", err)
	}

	for _, e := range entities {
		e.IsRead = true
		entityBytes, marshalErr := json.Marshal(e)
		if marshalErr != nil {
			return dberrors.NewDatabaseError(opMarshal, marshalErr)
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
