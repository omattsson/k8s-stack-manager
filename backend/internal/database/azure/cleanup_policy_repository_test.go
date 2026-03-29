package azure_test

import (
	"context"
	"encoding/json"
	"errors"
"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"testing"

	"backend/internal/database/azure"
	"backend/internal/database/azure/testhelpers"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanupPolicyRepository_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		policy    *models.CleanupPolicy
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful create",
			policy: &models.CleanupPolicy{
				Name:      "daily-cleanup",
				ClusterID: "cluster-1",
				Action:    "stop",
				Condition: "status == 'running'",
				Schedule:  "0 2 * * *",
				Enabled:   true,
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "policy", e["PartitionKey"])
					assert.NotEmpty(t, e["RowKey"])
					assert.Equal(t, "daily-cleanup", e["Name"])
					assert.Equal(t, "stop", e["Action"])
					assert.Equal(t, true, e["Enabled"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "validation error - missing name",
			policy: &models.CleanupPolicy{
				ClusterID: "cluster-1",
				Action:    "stop",
				Condition: "status == 'running'",
				Schedule:  "0 2 * * *",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "validation error - invalid action",
			policy: &models.CleanupPolicy{
				Name:      "test",
				ClusterID: "cluster-1",
				Action:    "invalid",
				Condition: "status == 'running'",
				Schedule:  "0 2 * * *",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "azure error propagates",
			policy: &models.CleanupPolicy{
				Name:      "test",
				ClusterID: "cluster-1",
				Action:    "stop",
				Condition: "status == 'running'",
				Schedule:  "0 2 * * *",
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
			repo := azure.NewTestCleanupPolicyRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Create(tt.policy)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.policy.ID)
				assert.False(t, tt.policy.CreatedAt.IsZero())
			}
		})
	}
}

func TestCleanupPolicyRepository_FindByID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		id        string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
		errTarget error
	}{
		{
			name: "found",
			id:   "pol-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					assert.Equal(t, "policy", pk)
					assert.Equal(t, "pol-1", rk)
					data, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "policy",
						"RowKey":       "pol-1",
						"ID":           "pol-1",
						"Name":         "test-policy",
						"ClusterID":    "cluster-1",
						"Action":       "stop",
						"Condition":    "status == 'running'",
						"Schedule":     "0 2 * * *",
						"Enabled":      true,
						"CreatedAt":    "2024-01-01T00:00:00Z",
						"UpdatedAt":    "2024-01-01T00:00:00Z",
					})
					return aztables.GetEntityResponse{Value: data}, nil
				})
			},
		},
		{
			name: "not found",
			id:   "missing",
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
			repo := azure.NewTestCleanupPolicyRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			result, err := repo.FindByID(tt.id)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errTarget != nil {
					assert.True(t, errors.Is(err, tt.errTarget))
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, "pol-1", result.ID)
				assert.Equal(t, "test-policy", result.Name)
			}
		})
	}
}

func TestCleanupPolicyRepository_Update(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		policy    *models.CleanupPolicy
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful update",
			policy: &models.CleanupPolicy{
				ID:        "pol-1",
				Name:      "updated",
				ClusterID: "cluster-1",
				Action:    "clean",
				Condition: "age > 7d",
				Schedule:  "0 3 * * *",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "pol-1", e["ID"])
					assert.Equal(t, "updated", e["Name"])
					return aztables.UpdateEntityResponse{}, nil
				})
			},
		},
		{
			name: "validation error",
			policy: &models.CleanupPolicy{
				ID: "pol-1",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "azure error",
			policy: &models.CleanupPolicy{
				ID:        "pol-1",
				Name:      "test",
				ClusterID: "cluster-1",
				Action:    "stop",
				Condition: "cond",
				Schedule:  "0 2 * * *",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
					return aztables.UpdateEntityResponse{}, errors.New("update failed")
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestCleanupPolicyRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Update(tt.policy)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.False(t, tt.policy.UpdatedAt.IsZero())
			}
		})
	}
}

func TestCleanupPolicyRepository_Delete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		id        string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful delete",
			id:   "pol-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
					assert.Equal(t, "policy", pk)
					assert.Equal(t, "pol-1", rk)
					return aztables.DeleteEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error",
			id:   "pol-1",
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
			repo := azure.NewTestCleanupPolicyRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Delete(tt.id)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCleanupPolicyRepository_List(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		setupMock func(*testhelpers.MockTableClient)
		wantLen   int
		wantErr   bool
	}{
		{
			name: "returns policies",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey": "policy",
					"RowKey":       "pol-1",
					"ID":           "pol-1",
					"Name":         "test",
					"ClusterID":    "c1",
					"Action":       "stop",
					"Condition":    "cond",
					"Schedule":     "0 2 * * *",
					"Enabled":      true,
					"CreatedAt":    "2024-01-01T00:00:00Z",
					"UpdatedAt":    "2024-01-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
			},
			wantLen: 1,
		},
		{
			name: "empty list",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantLen: 0,
		},
		{
			name: "pager error",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{{}}, errors.New("pager error"))
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestCleanupPolicyRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			results, err := repo.List()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, results, tt.wantLen)
			}
		})
	}
}

func TestCleanupPolicyRepository_ListEnabled(t *testing.T) {
	t.Parallel()

	repo := azure.NewTestCleanupPolicyRepository()
	mock := testhelpers.NewMockTableClient()
	enabled, _ := json.Marshal(map[string]interface{}{
		"PartitionKey": "policy",
		"RowKey":       "pol-1",
		"ID":           "pol-1",
		"Name":         "enabled-policy",
		"ClusterID":    "c1",
		"Action":       "stop",
		"Condition":    "cond",
		"Schedule":     "0 2 * * *",
		"Enabled":      true,
		"CreatedAt":    "2024-01-01T00:00:00Z",
		"UpdatedAt":    "2024-01-01T00:00:00Z",
	})
	disabled, _ := json.Marshal(map[string]interface{}{
		"PartitionKey": "policy",
		"RowKey":       "pol-2",
		"ID":           "pol-2",
		"Name":         "disabled-policy",
		"ClusterID":    "c1",
		"Action":       "clean",
		"Condition":    "cond",
		"Schedule":     "0 3 * * *",
		"Enabled":      false,
		"CreatedAt":    "2024-01-01T00:00:00Z",
		"UpdatedAt":    "2024-01-01T00:00:00Z",
	})
	mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
		return testhelpers.NewMockTablePager([][]byte{enabled, disabled}, nil)
	})
	repo.SetTestClient(mock)

	results, err := repo.ListEnabled()
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "enabled-policy", results[0].Name)
	assert.True(t, results[0].Enabled)
}
