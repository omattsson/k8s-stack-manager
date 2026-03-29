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

func TestStackInstanceRepository_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		instance  *models.StackInstance
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful create",
			instance: &models.StackInstance{
				StackDefinitionID: "def-1",
				Name:              "my-stack",
				Namespace:         "stack-my-stack-alice",
				OwnerID:           "alice",
				Branch:            "main",
				Status:            models.StackStatusDraft,
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "global", e["PartitionKey"])
					assert.NotEmpty(t, e["RowKey"])
					assert.Equal(t, "my-stack", e["Name"])
					assert.Equal(t, "alice", e["OwnerID"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "generates ID when empty",
			instance: &models.StackInstance{
				Name:    "test-stack",
				OwnerID: "bob",
				Status:  models.StackStatusDraft,
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
			instance: &models.StackInstance{
				Name:    "err-stack",
				OwnerID: "alice",
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
			repo := azure.NewTestStackInstanceRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Create(tt.instance)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.instance.ID)
				assert.False(t, tt.instance.CreatedAt.IsZero())
				assert.False(t, tt.instance.UpdatedAt.IsZero())
			}
		})
	}
}

func TestStackInstanceRepository_FindByID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		id       string
		setup    func(*testhelpers.MockTableClient)
		wantErr  bool
		wantName string
	}{
		{
			name: "found",
			id:   "inst-1",
			setup: func(m *testhelpers.MockTableClient) {
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					assert.Equal(t, "global", pk)
					assert.Equal(t, "inst-1", rk)
					data, _ := json.Marshal(map[string]interface{}{
						"PartitionKey":      "global",
						"RowKey":            "inst-1",
						"ID":                "inst-1",
						"Name":              "my-stack",
						"StackDefinitionID": "def-1",
						"OwnerID":           "alice",
						"Status":            "running",
						"TTLMinutes":        float64(60),
						"CreatedAt":         "2025-01-01T00:00:00Z",
						"UpdatedAt":         "2025-01-01T00:00:00Z",
					})
					return aztables.GetEntityResponse{Value: data}, nil
				})
			},
			wantName: "my-stack",
		},
		{
			name: "not found",
			id:   "nonexistent",
			setup: func(m *testhelpers.MockTableClient) {
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					return aztables.GetEntityResponse{}, &dberrors.DatabaseError{Err: dberrors.ErrNotFound}
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestStackInstanceRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setup != nil {
				tt.setup(mock)
			}
			repo.SetTestClient(mock)
			inst, err := repo.FindByID(tt.id)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, inst)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantName, inst.Name)
				assert.Equal(t, 60, inst.TTLMinutes)
			}
		})
	}
}

func TestStackInstanceRepository_Update(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		instance *models.StackInstance
		setup    func(*testhelpers.MockTableClient)
		wantErr  bool
	}{
		{
			name: "successful update",
			instance: &models.StackInstance{
				ID:      "inst-1",
				Name:    "updated-stack",
				OwnerID: "alice",
				Status:  models.StackStatusRunning,
			},
			setup: func(m *testhelpers.MockTableClient) {
				m.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "global", e["PartitionKey"])
					assert.Equal(t, "inst-1", e["RowKey"])
					assert.Equal(t, "updated-stack", e["Name"])
					return aztables.UpdateEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error propagates",
			instance: &models.StackInstance{
				ID:   "inst-1",
				Name: "fail-stack",
			},
			setup: func(m *testhelpers.MockTableClient) {
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
			repo := azure.NewTestStackInstanceRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setup != nil {
				tt.setup(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Update(tt.instance)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.False(t, tt.instance.UpdatedAt.IsZero())
			}
		})
	}
}

func TestStackInstanceRepository_Delete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		id      string
		setup   func(*testhelpers.MockTableClient)
		wantErr bool
	}{
		{
			name: "successful delete",
			id:   "inst-1",
			setup: func(m *testhelpers.MockTableClient) {
				m.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
					assert.Equal(t, "global", pk)
					assert.Equal(t, "inst-1", rk)
					return aztables.DeleteEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error propagates",
			id:   "inst-fail",
			setup: func(m *testhelpers.MockTableClient) {
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
			repo := azure.NewTestStackInstanceRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setup != nil {
				tt.setup(mock)
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

func TestStackInstanceRepository_FindByNamespace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		namespace string
		setup     func(*testhelpers.MockTableClient)
		wantErr   bool
		errIs     error
		wantID    string
	}{
		{
			name:      "found",
			namespace: "stack-my-stack-alice",
			setup: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					data, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "global",
						"RowKey":       "inst-1",
						"ID":           "inst-1",
						"Name":         "my-stack",
						"Namespace":    "stack-my-stack-alice",
						"OwnerID":      "alice",
						"Status":       "running",
						"CreatedAt":    "2025-01-01T00:00:00Z",
						"UpdatedAt":    "2025-01-01T00:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
			},
			wantID: "inst-1",
		},
		{
			name:      "not found",
			namespace: "nonexistent-ns",
			setup: func(m *testhelpers.MockTableClient) {
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
			repo := azure.NewTestStackInstanceRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setup != nil {
				tt.setup(mock)
			}
			repo.SetTestClient(mock)
			inst, err := repo.FindByNamespace(tt.namespace)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errIs != nil {
					assert.ErrorIs(t, err, tt.errIs)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantID, inst.ID)
			}
		})
	}
}

func TestStackInstanceRepository_List(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(*testhelpers.MockTableClient)
		wantLen int
		wantErr bool
	}{
		{
			name: "returns multiple instances",
			setup: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					d1, _ := json.Marshal(map[string]interface{}{
						"ID": "inst-1", "Name": "stack-1", "OwnerID": "alice",
						"Status": "running", "CreatedAt": "2025-01-01T00:00:00Z", "UpdatedAt": "2025-01-01T00:00:00Z",
					})
					d2, _ := json.Marshal(map[string]interface{}{
						"ID": "inst-2", "Name": "stack-2", "OwnerID": "bob",
						"Status": "stopped", "CreatedAt": "2025-01-01T00:00:00Z", "UpdatedAt": "2025-01-01T00:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{d1, d2}, nil)
				})
			},
			wantLen: 2,
		},
		{
			name: "empty result",
			setup: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantLen: 0,
		},
		{
			name: "pager error",
			setup: func(m *testhelpers.MockTableClient) {
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
			repo := azure.NewTestStackInstanceRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setup != nil {
				tt.setup(mock)
			}
			repo.SetTestClient(mock)
			instances, err := repo.List()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, instances, tt.wantLen)
			}
		})
	}
}

func TestStackInstanceRepository_ListByOwner(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestStackInstanceRepository()
	mock := testhelpers.NewMockTableClient()
	mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
		d1, _ := json.Marshal(map[string]interface{}{
			"ID": "inst-1", "Name": "stack-1", "OwnerID": "alice",
			"Status": "running", "CreatedAt": "2025-01-01T00:00:00Z", "UpdatedAt": "2025-01-01T00:00:00Z",
		})
		return testhelpers.NewMockTablePager([][]byte{d1}, nil)
	})
	repo.SetTestClient(mock)

	instances, err := repo.ListByOwner("alice")
	require.NoError(t, err)
	assert.Len(t, instances, 1)
	assert.Equal(t, "alice", instances[0].OwnerID)
}

func TestStackInstanceRepository_FindByCluster(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		clusterID string
		setup     func(*testhelpers.MockTableClient)
		wantLen   int
		wantErr   bool
	}{
		{
			name:      "with cluster ID",
			clusterID: "cluster-1",
			setup: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					d1, _ := json.Marshal(map[string]interface{}{
						"ID": "inst-1", "Name": "stack-1", "ClusterID": "cluster-1",
						"Status": "running", "CreatedAt": "2025-01-01T00:00:00Z", "UpdatedAt": "2025-01-01T00:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{d1}, nil)
				})
			},
			wantLen: 1,
		},
		{
			name:      "empty cluster ID filters in-memory",
			clusterID: "",
			setup: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					d1, _ := json.Marshal(map[string]interface{}{
						"ID": "inst-1", "Name": "stack-1", "ClusterID": "",
						"Status": "running", "CreatedAt": "2025-01-01T00:00:00Z", "UpdatedAt": "2025-01-01T00:00:00Z",
					})
					d2, _ := json.Marshal(map[string]interface{}{
						"ID": "inst-2", "Name": "stack-2", "ClusterID": "cluster-1",
						"Status": "running", "CreatedAt": "2025-01-01T00:00:00Z", "UpdatedAt": "2025-01-01T00:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{d1, d2}, nil)
				})
			},
			wantLen: 1,
		},
		{
			name:      "pager error",
			clusterID: "cluster-1",
			setup: func(m *testhelpers.MockTableClient) {
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
			repo := azure.NewTestStackInstanceRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setup != nil {
				tt.setup(mock)
			}
			repo.SetTestClient(mock)
			instances, err := repo.FindByCluster(tt.clusterID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, instances, tt.wantLen)
			}
		})
	}
}

func TestStackInstanceRepository_CountByClusterAndOwner(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestStackInstanceRepository()
	mock := testhelpers.NewMockTableClient()
	mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
		d1, _ := json.Marshal(map[string]interface{}{
			"ID": "inst-1", "OwnerID": "alice", "ClusterID": "cluster-1",
			"Status": "running", "CreatedAt": "2025-01-01T00:00:00Z", "UpdatedAt": "2025-01-01T00:00:00Z",
		})
		d2, _ := json.Marshal(map[string]interface{}{
			"ID": "inst-2", "OwnerID": "bob", "ClusterID": "cluster-1",
			"Status": "running", "CreatedAt": "2025-01-01T00:00:00Z", "UpdatedAt": "2025-01-01T00:00:00Z",
		})
		d3, _ := json.Marshal(map[string]interface{}{
			"ID": "inst-3", "OwnerID": "alice", "ClusterID": "cluster-1",
			"Status": "stopped", "CreatedAt": "2025-01-01T00:00:00Z", "UpdatedAt": "2025-01-01T00:00:00Z",
		})
		return testhelpers.NewMockTablePager([][]byte{d1, d2, d3}, nil)
	})
	repo.SetTestClient(mock)

	count, err := repo.CountByClusterAndOwner("cluster-1", "alice")
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestStackInstanceRepository_ListExpired(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(*testhelpers.MockTableClient)
		wantLen int
		wantErr bool
	}{
		{
			name: "returns expired running instances",
			setup: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					d1, _ := json.Marshal(map[string]interface{}{
						"ID": "inst-1", "Status": "running",
						"ExpiresAt": "2020-01-01T00:00:00Z",
						"CreatedAt": "2020-01-01T00:00:00Z", "UpdatedAt": "2020-01-01T00:00:00Z",
					})
					d2, _ := json.Marshal(map[string]interface{}{
						"ID": "inst-2", "Status": "running",
						"ExpiresAt": "2099-01-01T00:00:00Z",
						"CreatedAt": "2020-01-01T00:00:00Z", "UpdatedAt": "2020-01-01T00:00:00Z",
					})
					d3, _ := json.Marshal(map[string]interface{}{
						"ID": "inst-3", "Status": "running",
						"CreatedAt": "2020-01-01T00:00:00Z", "UpdatedAt": "2020-01-01T00:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{d1, d2, d3}, nil)
				})
			},
			wantLen: 1,
		},
		{
			name: "empty when none expired",
			setup: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantLen: 0,
		},
		{
			name: "pager error",
			setup: func(m *testhelpers.MockTableClient) {
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
			repo := azure.NewTestStackInstanceRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setup != nil {
				tt.setup(mock)
			}
			repo.SetTestClient(mock)
			expired, err := repo.ListExpired()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, expired, tt.wantLen)
			}
		})
	}
}
