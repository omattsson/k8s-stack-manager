package azure_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"backend/internal/database/azure"
	"backend/internal/database/azure/testhelpers"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotificationRepository_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		notification *models.Notification
		setupMock    func(*testhelpers.MockTableClient)
		wantErr      bool
	}{
		{
			name: "successful create",
			notification: &models.Notification{
				UserID:     "user-1",
				Type:       "deploy",
				Title:      "Deployment started",
				Message:    "Stack my-stack is deploying",
				EntityType: "stack_instance",
				EntityID:   "inst-1",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					// PK should be user ID
					assert.Equal(t, "user-1", e["PartitionKey"])
					assert.NotEmpty(t, e["RowKey"])
					assert.Equal(t, "Deployment started", e["Title"])
					assert.Equal(t, "deploy", e["Type"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "generates ID when empty",
			notification: &models.Notification{
				UserID: "user-1",
				Type:   "stop",
				Title:  "Stack stopped",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.NotEmpty(t, e["ID"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error propagates",
			notification: &models.Notification{
				UserID: "user-1",
				Type:   "deploy",
				Title:  "Test",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					return aztables.AddEntityResponse{}, errors.New("azure failure")
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestNotificationRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Create(context.Background(), tt.notification)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.notification.ID)
				assert.False(t, tt.notification.CreatedAt.IsZero())
			}
		})
	}
}

func TestNotificationRepository_ListByUser(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		userID     string
		unreadOnly bool
		limit      int
		offset     int
		setupMock  func(*testhelpers.MockTableClient)
		wantLen    int
		wantTotal  int64
		wantErr    bool
	}{
		{
			name:   "returns all notifications",
			userID: "user-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					d1, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "user-1", "RowKey": "rk1",
						"ID": "notif-1", "UserID": "user-1",
						"Type": "deploy", "Title": "Deploy 1",
						"IsRead": false, "CreatedAt": "2025-01-15T10:00:00Z",
					})
					d2, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "user-1", "RowKey": "rk2",
						"ID": "notif-2", "UserID": "user-1",
						"Type": "stop", "Title": "Stop 1",
						"IsRead": true, "CreatedAt": "2025-01-16T10:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{d1, d2}, nil)
				})
			},
			wantLen:   2,
			wantTotal: 2,
		},
		{
			name:   "with limit and offset",
			userID: "user-1",
			limit:  1,
			offset: 1,
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					d1, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "user-1", "RowKey": "rk1",
						"ID": "notif-1", "UserID": "user-1",
						"Type": "deploy", "Title": "Deploy 1",
						"IsRead": false, "CreatedAt": "2025-01-15T10:00:00Z",
					})
					d2, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "user-1", "RowKey": "rk2",
						"ID": "notif-2", "UserID": "user-1",
						"Type": "stop", "Title": "Stop 1",
						"IsRead": true, "CreatedAt": "2025-01-16T10:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{d1, d2}, nil)
				})
			},
			wantLen:   1,
			wantTotal: 2,
		},
		{
			name:   "empty result",
			userID: "user-none",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantLen:   0,
			wantTotal: 0,
		},
		{
			name:   "pager error",
			userID: "user-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
return testhelpers.NewMockTablePager([][]byte{{}}, errors.New("pager failed"))
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestNotificationRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			notifs, total, err := repo.ListByUser(context.Background(), tt.userID, tt.unreadOnly, tt.limit, tt.offset)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, notifs, tt.wantLen)
				assert.Equal(t, tt.wantTotal, total)
			}
		})
	}
}

func TestNotificationRepository_CountUnread(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		userID    string
		setupMock func(*testhelpers.MockTableClient)
		wantCount int64
		wantErr   bool
	}{
		{
			name:   "returns unread count",
			userID: "user-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					d1, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "user-1", "RowKey": "rk1",
						"ID": "notif-1", "IsRead": false,
						"CreatedAt": "2025-01-15T10:00:00Z",
					})
					d2, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "user-1", "RowKey": "rk2",
						"ID": "notif-2", "IsRead": false,
						"CreatedAt": "2025-01-16T10:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{d1, d2}, nil)
				})
			},
			wantCount: 2,
		},
		{
			name:   "zero unread",
			userID: "user-none",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantCount: 0,
		},
		{
			name:   "pager error",
			userID: "user-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{{}}, errors.New("pager failed"))
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestNotificationRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			count, err := repo.CountUnread(context.Background(), tt.userID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantCount, count)
			}
		})
	}
}

func TestNotificationRepository_MarkAsRead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		id        string
		userID    string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
		errIs     error
	}{
		{
			name:   "successful mark as read",
			id:     "notif-1",
			userID: "user-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					data, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "user-1", "RowKey": "rk1",
						"ID": "notif-1", "UserID": "user-1",
						"Type": "deploy", "Title": "Test",
						"IsRead": false, "CreatedAt": "2025-01-15T10:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
				m.SetUpsertEntity(func(ctx context.Context, entity []byte, opts *aztables.UpsertEntityOptions) (aztables.UpsertEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, true, e["IsRead"])
					return aztables.UpsertEntityResponse{}, nil
				})
			},
		},
		{
			name:   "not found",
			id:     "nonexistent",
			userID: "user-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantErr: true,
			errIs:   dberrors.ErrNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestNotificationRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.MarkAsRead(context.Background(), tt.id, tt.userID)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errIs != nil {
					assert.ErrorIs(t, err, tt.errIs)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNotificationRepository_MarkAllAsRead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		userID    string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name:   "marks all unread as read",
			userID: "user-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					d1, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "user-1", "RowKey": "rk1",
						"ID": "notif-1", "IsRead": false,
						"CreatedAt": "2025-01-15T10:00:00Z",
					})
					d2, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "user-1", "RowKey": "rk2",
						"ID": "notif-2", "IsRead": false,
						"CreatedAt": "2025-01-16T10:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{d1, d2}, nil)
				})
				upsertCount := 0
				m.SetUpsertEntity(func(ctx context.Context, entity []byte, opts *aztables.UpsertEntityOptions) (aztables.UpsertEntityResponse, error) {
					upsertCount++
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, true, e["IsRead"])
					return aztables.UpsertEntityResponse{}, nil
				})
			},
		},
		{
			name:   "no unread notifications",
			userID: "user-clean",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
		},
		{
			name:   "pager error",
			userID: "user-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{{}}, errors.New("pager failed"))
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestNotificationRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.MarkAllAsRead(context.Background(), tt.userID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNotificationRepository_GetPreferences(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestNotificationRepository()
	mock := testhelpers.NewMockTableClient()
	repo.SetTestClient(mock)

	prefs, err := repo.GetPreferences(context.Background(), "user-1")
	assert.Error(t, err)
	assert.ErrorIs(t, err, dberrors.ErrNotImplemented)
	assert.Nil(t, prefs)
}

func TestNotificationRepository_UpdatePreference(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestNotificationRepository()
	mock := testhelpers.NewMockTableClient()
	repo.SetTestClient(mock)

	err := repo.UpdatePreference(context.Background(), &models.NotificationPreference{
		ID:        "pref-1",
		UserID:    "user-1",
		EventType: "deploy",
		Enabled:   true,
	})
	assert.Error(t, err)
	assert.ErrorIs(t, err, dberrors.ErrNotImplemented)
}
