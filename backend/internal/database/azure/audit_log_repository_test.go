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

func TestAuditLogRepository_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		log       *models.AuditLog
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful create",
			log: &models.AuditLog{
				UserID:     "user-1",
				Username:   "alice",
				Action:     "create",
				EntityType: "stack_instance",
				EntityID:   "inst-1",
				Details:    "Created stack instance",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					// PK should be YYYY-MM format
					pk, ok := e["PartitionKey"].(string)
					assert.True(t, ok)
					assert.Regexp(t, `^\d{4}-\d{2}$`, pk)
					assert.NotEmpty(t, e["RowKey"])
					assert.Equal(t, "alice", e["Username"])
					assert.Equal(t, "create", e["Action"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "generates ID when empty",
			log: &models.AuditLog{
				UserID: "user-1",
				Action: "delete",
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
			name: "sets timestamp when zero",
			log: &models.AuditLog{
				UserID: "user-1",
				Action: "update",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.NotEmpty(t, e["Timestamp"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error propagates",
			log: &models.AuditLog{
				UserID: "user-1",
				Action: "create",
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
			repo := azure.NewTestAuditLogRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Create(tt.log)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.log.ID)
				assert.False(t, tt.log.Timestamp.IsZero())
			}
		})
	}
}

func TestAuditLogRepository_List(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		filters   models.AuditLogFilters
		setupMock func(*testhelpers.MockTableClient)
		wantLen   int
		wantErr   bool
		wantTotal int64
	}{
		{
			name:    "returns all logs with no filters",
			filters: models.AuditLogFilters{},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					d1, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "2025-01",
						"RowKey":       "00000000000000000001_id1",
						"ID":           "log-1",
						"UserID":       "user-1",
						"Username":     "alice",
						"Action":       "create",
						"EntityType":   "stack_instance",
						"EntityID":     "inst-1",
						"Timestamp":    "2025-01-15T10:00:00Z",
					})
					d2, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "2025-01",
						"RowKey":       "00000000000000000002_id2",
						"ID":           "log-2",
						"UserID":       "user-2",
						"Username":     "bob",
						"Action":       "delete",
						"EntityType":   "stack_instance",
						"EntityID":     "inst-2",
						"Timestamp":    "2025-01-16T10:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{d1, d2}, nil)
				})
			},
			wantLen:   2,
			wantTotal: 2,
		},
		{
			name: "with user ID filter",
			filters: models.AuditLogFilters{
				UserID: "user-1",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					d1, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "2025-01",
						"RowKey":       "00000000000000000001_id1",
						"ID":           "log-1",
						"UserID":       "user-1",
						"Action":       "create",
						"Timestamp":    "2025-01-15T10:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{d1}, nil)
				})
			},
			wantLen:   1,
			wantTotal: 1,
		},
		{
			name: "with limit and offset",
			filters: models.AuditLogFilters{
				Limit:  1,
				Offset: 1,
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					d1, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "2025-01", "RowKey": "rk1",
						"ID": "log-1", "Timestamp": "2025-01-15T10:00:00Z",
					})
					d2, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "2025-01", "RowKey": "rk2",
						"ID": "log-2", "Timestamp": "2025-01-16T10:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{d1, d2}, nil)
				})
			},
			wantLen:   1,
			wantTotal: -1,
		},
		{
			name:    "empty result",
			filters: models.AuditLogFilters{},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantLen:   0,
			wantTotal: 0,
		},
		{
			name:    "pager error",
			filters: models.AuditLogFilters{},
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
			repo := azure.NewTestAuditLogRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			result, err := repo.List(tt.filters)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Len(t, result.Data, tt.wantLen)
				assert.Equal(t, tt.wantTotal, result.Total)
			}
		})
	}
}

func TestAuditLogRepository_ListWithCursor(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestAuditLogRepository()
	mock := testhelpers.NewMockTableClient()
	mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
		d1, _ := json.Marshal(map[string]interface{}{
			"PartitionKey": "2025-01", "RowKey": "rk3",
			"ID": "log-3", "Timestamp": "2025-01-17T10:00:00Z",
		})
		return testhelpers.NewMockTablePager([][]byte{d1}, nil)
	})
	repo.SetTestClient(mock)

	// Invalid cursor should return validation error.
	_, err := repo.List(models.AuditLogFilters{Cursor: "invalid-base64!!!"})
	assert.Error(t, err)
	assert.ErrorIs(t, err, dberrors.ErrValidation)
}

func TestAuditLogRepository_ListWithEntityTypeFilter(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestAuditLogRepository()
	mock := testhelpers.NewMockTableClient()
	mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
		// The filter should contain EntityType
		d1, _ := json.Marshal(map[string]interface{}{
			"PartitionKey": "2025-01", "RowKey": "rk1",
			"ID": "log-1", "EntityType": "stack_instance",
			"Timestamp": "2025-01-15T10:00:00Z",
		})
		return testhelpers.NewMockTablePager([][]byte{d1}, nil)
	})
	repo.SetTestClient(mock)

	result, err := repo.List(models.AuditLogFilters{EntityType: "stack_instance"})
	require.NoError(t, err)
	assert.Len(t, result.Data, 1)
}
