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

func TestValueOverrideRepository_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		override  *models.ValueOverride
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful create",
			override: &models.ValueOverride{
				StackInstanceID: "inst-1",
				ChartConfigID:   "cc-1",
				Values:          "key: value",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "inst-1", e["PartitionKey"])
					assert.Equal(t, "cc-1", e["RowKey"])
					assert.Equal(t, "key: value", e["Values"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "validation error - missing StackInstanceID",
			override: &models.ValueOverride{
				ChartConfigID: "cc-1",
				Values:        "key: value",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "validation error - missing ChartConfigID",
			override: &models.ValueOverride{
				StackInstanceID: "inst-1",
				Values:          "key: value",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "azure error propagates",
			override: &models.ValueOverride{
				StackInstanceID: "inst-1",
				ChartConfigID:   "cc-1",
				Values:          "key: value",
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
			repo := azure.NewTestValueOverrideRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Create(tt.override)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.override.ID)
				assert.False(t, tt.override.UpdatedAt.IsZero())
			}
		})
	}
}

func TestValueOverrideRepository_FindByID(t *testing.T) {
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
			id:   "vo-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":    "inst-1",
					"RowKey":          "cc-1",
					"ID":              "vo-1",
					"StackInstanceID": "inst-1",
					"ChartConfigID":   "cc-1",
					"Values":          "key: value",
					"UpdatedAt":       "2024-01-01T00:00:00Z",
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
			repo := azure.NewTestValueOverrideRepository()
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
				assert.Equal(t, "vo-1", result.ID)
			}
		})
	}
}

func TestValueOverrideRepository_FindByInstanceAndChart(t *testing.T) {
	t.Parallel()

	repo := azure.NewTestValueOverrideRepository()
	mock := testhelpers.NewMockTableClient()
	mock.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
		assert.Equal(t, "inst-1", pk)
		assert.Equal(t, "cc-1", rk)
		data, _ := json.Marshal(map[string]interface{}{
			"PartitionKey":    "inst-1",
			"RowKey":          "cc-1",
			"ID":              "vo-1",
			"StackInstanceID": "inst-1",
			"ChartConfigID":   "cc-1",
			"Values":          "key: value",
			"UpdatedAt":       "2024-01-01T00:00:00Z",
		})
		return aztables.GetEntityResponse{Value: data}, nil
	})
	repo.SetTestClient(mock)

	result, err := repo.FindByInstanceAndChart("inst-1", "cc-1")
	require.NoError(t, err)
	assert.Equal(t, "vo-1", result.ID)
	assert.Equal(t, "inst-1", result.StackInstanceID)
	assert.Equal(t, "cc-1", result.ChartConfigID)
}

func TestValueOverrideRepository_Update(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		override  *models.ValueOverride
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful update",
			override: &models.ValueOverride{
				ID:              "vo-1",
				StackInstanceID: "inst-1",
				ChartConfigID:   "cc-1",
				Values:          "updated: value",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "updated: value", e["Values"])
					return aztables.UpdateEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error",
			override: &models.ValueOverride{
				ID:              "vo-1",
				StackInstanceID: "inst-1",
				ChartConfigID:   "cc-1",
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
			repo := azure.NewTestValueOverrideRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Update(tt.override)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.False(t, tt.override.UpdatedAt.IsZero())
			}
		})
	}
}

func TestValueOverrideRepository_Delete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		id        string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful delete",
			id:   "vo-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":    "inst-1",
					"RowKey":          "cc-1",
					"ID":              "vo-1",
					"StackInstanceID": "inst-1",
					"ChartConfigID":   "cc-1",
					"Values":          "key: value",
					"UpdatedAt":       "2024-01-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
				m.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
					assert.Equal(t, "inst-1", pk)
					assert.Equal(t, "cc-1", rk)
					return aztables.DeleteEntityResponse{}, nil
				})
			},
		},
		{
			name: "not found during lookup",
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
			repo := azure.NewTestValueOverrideRepository()
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

func TestValueOverrideRepository_ListByInstance(t *testing.T) {
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
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":    "inst-1",
					"RowKey":          "cc-1",
					"ID":              "vo-1",
					"StackInstanceID": "inst-1",
					"ChartConfigID":   "cc-1",
					"Values":          "key: value",
					"UpdatedAt":       "2024-01-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
			},
			wantLen: 1,
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
			repo := azure.NewTestValueOverrideRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			results, err := repo.ListByInstance(tt.instanceID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, results, tt.wantLen)
			}
		})
	}
}
