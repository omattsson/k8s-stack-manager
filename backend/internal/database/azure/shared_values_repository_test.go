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

func TestSharedValuesRepository_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		sv        *models.SharedValues
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful create",
			sv: &models.SharedValues{
				ClusterID: "cluster-1",
				Name:      "global-values",
				Values:    "key: value",
				Priority:  10,
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "cluster-1", e["PartitionKey"])
					assert.NotEmpty(t, e["RowKey"])
					assert.Equal(t, "global-values", e["Name"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "validation error - missing name",
			sv: &models.SharedValues{
				ClusterID: "cluster-1",
				Values:    "key: value",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "validation error - missing cluster_id",
			sv: &models.SharedValues{
				Name:   "test",
				Values: "key: value",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "azure error propagates",
			sv: &models.SharedValues{
				ClusterID: "cluster-1",
				Name:      "test",
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
			repo := azure.NewTestSharedValuesRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Create(tt.sv)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.sv.ID)
				assert.False(t, tt.sv.CreatedAt.IsZero())
			}
		})
	}
}

func TestSharedValuesRepository_FindByID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		id        string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
		errTarget error
	}{
		{
			name: "found via scan",
			id:   "sv-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey": "cluster-1",
					"RowKey":       "sv-1",
					"ID":           "sv-1",
					"ClusterID":    "cluster-1",
					"Name":         "test-values",
					"Values":       "key: val",
					"Priority":     5,
					"CreatedAt":    "2024-01-01T00:00:00Z",
					"UpdatedAt":    "2024-01-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
			},
		},
		{
			name: "not found",
			id:   "missing",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
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
			repo := azure.NewTestSharedValuesRepository()
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
				assert.Equal(t, "sv-1", result.ID)
			}
		})
	}
}

func TestSharedValuesRepository_FindByClusterAndID(t *testing.T) {
	t.Parallel()

	repo := azure.NewTestSharedValuesRepository()
	mock := testhelpers.NewMockTableClient()
	mock.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
		assert.Equal(t, "cluster-1", pk)
		assert.Equal(t, "sv-1", rk)
		data, _ := json.Marshal(map[string]interface{}{
			"PartitionKey": "cluster-1",
			"RowKey":       "sv-1",
			"ID":           "sv-1",
			"ClusterID":    "cluster-1",
			"Name":         "test",
			"Priority":     1,
			"CreatedAt":    "2024-01-01T00:00:00Z",
			"UpdatedAt":    "2024-01-01T00:00:00Z",
		})
		return aztables.GetEntityResponse{Value: data}, nil
	})
	repo.SetTestClient(mock)

	result, err := repo.FindByClusterAndID("cluster-1", "sv-1")
	require.NoError(t, err)
	assert.Equal(t, "sv-1", result.ID)
	assert.Equal(t, "cluster-1", result.ClusterID)
}

func TestSharedValuesRepository_Update(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		sv        *models.SharedValues
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful update",
			sv: &models.SharedValues{
				ID:        "sv-1",
				ClusterID: "cluster-1",
				Name:      "updated",
				Values:    "new: value",
				Priority:  5,
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "updated", e["Name"])
					return aztables.UpdateEntityResponse{}, nil
				})
			},
		},
		{
			name: "validation error",
			sv: &models.SharedValues{
				ID:        "sv-1",
				ClusterID: "cluster-1",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestSharedValuesRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Update(tt.sv)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.False(t, tt.sv.UpdatedAt.IsZero())
			}
		})
	}
}

func TestSharedValuesRepository_Delete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		id        string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful delete",
			id:   "sv-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				// FindByID scan first
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey": "cluster-1",
					"RowKey":       "sv-1",
					"ID":           "sv-1",
					"ClusterID":    "cluster-1",
					"Name":         "test",
					"Priority":     1,
					"CreatedAt":    "2024-01-01T00:00:00Z",
					"UpdatedAt":    "2024-01-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
				m.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
					assert.Equal(t, "cluster-1", pk)
					assert.Equal(t, "sv-1", rk)
					return aztables.DeleteEntityResponse{}, nil
				})
			},
		},
		{
			name: "not found on lookup",
			id:   "missing",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestSharedValuesRepository()
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

func TestSharedValuesRepository_ListByCluster(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		clusterID string
		setupMock func(*testhelpers.MockTableClient)
		wantLen   int
		wantErr   bool
	}{
		{
			name:      "returns sorted by priority",
			clusterID: "cluster-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data1, _ := json.Marshal(map[string]interface{}{
					"PartitionKey": "cluster-1",
					"RowKey":       "sv-1",
					"ID":           "sv-1",
					"ClusterID":    "cluster-1",
					"Name":         "high-priority",
					"Priority":     20,
					"CreatedAt":    "2024-01-01T00:00:00Z",
					"UpdatedAt":    "2024-01-01T00:00:00Z",
				})
				data2, _ := json.Marshal(map[string]interface{}{
					"PartitionKey": "cluster-1",
					"RowKey":       "sv-2",
					"ID":           "sv-2",
					"ClusterID":    "cluster-1",
					"Name":         "low-priority",
					"Priority":     5,
					"CreatedAt":    "2024-01-01T00:00:00Z",
					"UpdatedAt":    "2024-01-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data1, data2}, nil)
				})
			},
			wantLen: 2,
		},
		{
			name:      "empty",
			clusterID: "cluster-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantLen: 0,
		},
		{
			name:      "pager error",
			clusterID: "cluster-1",
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
			repo := azure.NewTestSharedValuesRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			results, err := repo.ListByCluster(tt.clusterID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, results, tt.wantLen)
				if tt.wantLen == 2 {
					// Should be sorted by priority ascending
					assert.True(t, results[0].Priority <= results[1].Priority)
				}
			}
		})
	}
}
