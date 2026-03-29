package azure_test

import (
	"context"
	"encoding/json"
	"errors"
"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"testing"
	"time"

	"backend/internal/database/azure"
	"backend/internal/database/azure/testhelpers"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyRepository_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		key       *models.APIKey
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful create",
			key: &models.APIKey{
				UserID:  "user-1",
				Name:    "my-key",
				KeyHash: "hash123",
				Prefix:  "abc123",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "user-1", e["PartitionKey"])
					assert.NotEmpty(t, e["RowKey"])
					assert.Equal(t, "my-key", e["Name"])
					assert.Equal(t, "hash123", e["KeyHash"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "validation error - missing UserID",
			key: &models.APIKey{
				Name: "test",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "validation error - missing Name",
			key: &models.APIKey{
				UserID: "user-1",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "with ExpiresAt",
			key: &models.APIKey{
				UserID:  "user-1",
				Name:    "expiring-key",
				KeyHash: "hash",
				Prefix:  "pfx",
				ExpiresAt: func() *time.Time {
					t := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
					return &t
				}(),
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.NotEmpty(t, e["ExpiresAt"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error propagates",
			key: &models.APIKey{
				UserID: "user-1",
				Name:   "fail",
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
			repo := azure.NewTestAPIKeyRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Create(tt.key)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.key.ID)
				assert.False(t, tt.key.CreatedAt.IsZero())
			}
		})
	}
}

func TestAPIKeyRepository_FindByID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		userID    string
		keyID     string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
		errTarget error
	}{
		{
			name:   "found",
			userID: "user-1",
			keyID:  "key-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					assert.Equal(t, "user-1", pk)
					assert.Equal(t, "key-1", rk)
					data, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "user-1",
						"RowKey":       "key-1",
						"ID":           "key-1",
						"UserID":       "user-1",
						"Name":         "my-key",
						"KeyHash":      "hash",
						"Prefix":       "pfx",
						"CreatedAt":    "2024-01-01T00:00:00Z",
					})
					return aztables.GetEntityResponse{Value: data}, nil
				})
			},
		},
		{
			name:   "not found",
			userID: "user-1",
			keyID:  "missing",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					return aztables.GetEntityResponse{}, &azcore.ResponseError{StatusCode: 404}
				})
			},
			wantErr:   true,
			errTarget: dberrors.ErrNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestAPIKeyRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			result, err := repo.FindByID(tt.userID, tt.keyID)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errTarget != nil {
					assert.True(t, errors.Is(err, tt.errTarget))
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, "key-1", result.ID)
				assert.Equal(t, "user-1", result.UserID)
			}
		})
	}
}

func TestAPIKeyRepository_FindByPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		prefix    string
		setupMock func(*testhelpers.MockTableClient)
		wantLen   int
		wantErr   bool
		errTarget error
	}{
		{
			name:   "found matching prefix",
			prefix: "abc123",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey": "user-1",
					"RowKey":       "key-1",
					"ID":           "key-1",
					"UserID":       "user-1",
					"Name":         "my-key",
					"KeyHash":      "hash",
					"Prefix":       "abc123",
					"CreatedAt":    "2024-01-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
			},
			wantLen: 1,
		},
		{
			name:   "no match",
			prefix: "xyz999",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey": "user-1",
					"RowKey":       "key-1",
					"ID":           "key-1",
					"UserID":       "user-1",
					"Name":         "my-key",
					"Prefix":       "abc123",
					"CreatedAt":    "2024-01-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
			},
			wantErr:   true,
			errTarget: dberrors.ErrNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestAPIKeyRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			results, err := repo.FindByPrefix(tt.prefix)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errTarget != nil {
					assert.True(t, errors.Is(err, tt.errTarget))
				}
			} else {
				require.NoError(t, err)
				assert.Len(t, results, tt.wantLen)
			}
		})
	}
}

func TestAPIKeyRepository_ListByUser(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		userID    string
		setupMock func(*testhelpers.MockTableClient)
		wantLen   int
		wantErr   bool
	}{
		{
			name:   "returns user keys",
			userID: "user-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey": "user-1",
					"RowKey":       "key-1",
					"ID":           "key-1",
					"UserID":       "user-1",
					"Name":         "key-1",
					"Prefix":       "pfx",
					"CreatedAt":    "2024-01-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
			},
			wantLen: 1,
		},
		{
			name:   "empty",
			userID: "user-2",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestAPIKeyRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			results, err := repo.ListByUser(tt.userID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, results, tt.wantLen)
			}
		})
	}
}

func TestAPIKeyRepository_UpdateLastUsed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful update",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					data, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "user-1",
						"RowKey":       "key-1",
						"ID":           "key-1",
						"UserID":       "user-1",
						"Name":         "test-key",
						"Prefix":       "pfx",
						"CreatedAt":    "2024-01-01T00:00:00Z",
					})
					return aztables.GetEntityResponse{Value: data}, nil
				})
				m.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.NotEmpty(t, e["LastUsedAt"])
					return aztables.UpdateEntityResponse{}, nil
				})
			},
		},
		{
			name: "get entity fails",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					return aztables.GetEntityResponse{}, &azcore.ResponseError{StatusCode: 404}
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestAPIKeyRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.UpdateLastUsed("user-1", "key-1", time.Now())
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestAPIKeyRepository_Delete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful delete",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
					assert.Equal(t, "user-1", pk)
					assert.Equal(t, "key-1", rk)
					return aztables.DeleteEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
					return aztables.DeleteEntityResponse{}, errors.New("delete failed")
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestAPIKeyRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Delete("user-1", "key-1")
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
