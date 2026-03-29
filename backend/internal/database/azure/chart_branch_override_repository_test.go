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

func TestChartBranchOverrideRepository_List(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		instanceID string
		setupMock  func(*testhelpers.MockTableClient)
		wantLen    int
		wantErr    bool
	}{
		{
			name:       "returns overrides",
			instanceID: "inst-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data1, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":    "inst-1",
					"RowKey":          "chart-1",
					"ID":              "bo-1",
					"StackInstanceID": "inst-1",
					"ChartConfigID":   "chart-1",
					"Branch":          "feature/test",
					"UpdatedAt":       "2024-01-01T00:00:00Z",
				})
				data2, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":    "inst-1",
					"RowKey":          "chart-2",
					"ID":              "bo-2",
					"StackInstanceID": "inst-1",
					"ChartConfigID":   "chart-2",
					"Branch":          "develop",
					"UpdatedAt":       "2024-01-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data1, data2}, nil)
				})
			},
			wantLen: 2,
		},
		{
			name:       "empty",
			instanceID: "inst-2",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantLen: 0,
		},
		{
			name:       "pager error",
			instanceID: "inst-1",
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
			repo := azure.NewTestChartBranchOverrideRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			results, err := repo.List(tt.instanceID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, results, tt.wantLen)
			}
		})
	}
}

func TestChartBranchOverrideRepository_Get(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		instanceID    string
		chartConfigID string
		setupMock     func(*testhelpers.MockTableClient)
		wantErr       bool
		errTarget     error
	}{
		{
			name:          "found",
			instanceID:    "inst-1",
			chartConfigID: "chart-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":    "inst-1",
					"RowKey":          "chart-1",
					"ID":              "bo-1",
					"StackInstanceID": "inst-1",
					"ChartConfigID":   "chart-1",
					"Branch":          "feature/test",
					"UpdatedAt":       "2024-01-01T00:00:00Z",
				})
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					assert.Equal(t, "inst-1", pk)
					assert.Equal(t, "chart-1", rk)
					return aztables.GetEntityResponse{Value: data}, nil
				})
			},
		},
		{
			name:          "not found",
			instanceID:    "inst-1",
			chartConfigID: "missing",
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
			repo := azure.NewTestChartBranchOverrideRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			result, err := repo.Get(tt.instanceID, tt.chartConfigID)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errTarget != nil {
					assert.True(t, errors.Is(err, tt.errTarget))
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, "bo-1", result.ID)
				assert.Equal(t, "feature/test", result.Branch)
			}
		})
	}
}

func TestChartBranchOverrideRepository_Set(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		override  *models.ChartBranchOverride
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful set",
			override: &models.ChartBranchOverride{
				StackInstanceID: "inst-1",
				ChartConfigID:   "chart-1",
				Branch:          "feature/new",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetUpsertEntity(func(ctx context.Context, entity []byte, opts *aztables.UpsertEntityOptions) (aztables.UpsertEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "inst-1", e["PartitionKey"])
					assert.Equal(t, "chart-1", e["RowKey"])
					assert.Equal(t, "feature/new", e["Branch"])
					return aztables.UpsertEntityResponse{}, nil
				})
			},
		},
		{
			name: "validation error - missing branch",
			override: &models.ChartBranchOverride{
				StackInstanceID: "inst-1",
				ChartConfigID:   "chart-1",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "validation error - missing stack_instance_id",
			override: &models.ChartBranchOverride{
				ChartConfigID: "chart-1",
				Branch:        "develop",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "validation error - missing chart_config_id",
			override: &models.ChartBranchOverride{
				StackInstanceID: "inst-1",
				Branch:          "develop",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "azure error",
			override: &models.ChartBranchOverride{
				StackInstanceID: "inst-1",
				ChartConfigID:   "chart-1",
				Branch:          "main",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetUpsertEntity(func(ctx context.Context, entity []byte, opts *aztables.UpsertEntityOptions) (aztables.UpsertEntityResponse, error) {
					return aztables.UpsertEntityResponse{}, errors.New("upsert failed")
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestChartBranchOverrideRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Set(tt.override)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.override.ID)
			}
		})
	}
}

func TestChartBranchOverrideRepository_Delete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		instanceID    string
		chartConfigID string
		setupMock     func(*testhelpers.MockTableClient)
		wantErr       bool
	}{
		{
			name:          "successful delete",
			instanceID:    "inst-1",
			chartConfigID: "chart-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
					assert.Equal(t, "inst-1", pk)
					assert.Equal(t, "chart-1", rk)
					return aztables.DeleteEntityResponse{}, nil
				})
			},
		},
		{
			name:          "azure error",
			instanceID:    "inst-1",
			chartConfigID: "chart-1",
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
			repo := azure.NewTestChartBranchOverrideRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Delete(tt.instanceID, tt.chartConfigID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestChartBranchOverrideRepository_DeleteByInstance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		instanceID string
		setupMock  func(*testhelpers.MockTableClient)
		wantErr    bool
	}{
		{
			name:       "deletes all overrides for instance",
			instanceID: "inst-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data1, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":    "inst-1",
					"RowKey":          "chart-1",
					"ID":              "bo-1",
					"StackInstanceID": "inst-1",
					"ChartConfigID":   "chart-1",
					"Branch":          "develop",
					"UpdatedAt":       "2024-01-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data1}, nil)
				})
				m.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
					assert.Equal(t, "inst-1", pk)
					assert.Equal(t, "chart-1", rk)
					return aztables.DeleteEntityResponse{}, nil
				})
			},
		},
		{
			name:       "empty list - no deletions",
			instanceID: "inst-2",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestChartBranchOverrideRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.DeleteByInstance(tt.instanceID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
