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

func TestTemplateChartConfigRepository_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		config    *models.TemplateChartConfig
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful create",
			config: &models.TemplateChartConfig{
				StackTemplateID: "tmpl-1",
				ChartName:       "nginx",
				RepositoryURL:   "https://charts.example.com",
				ChartVersion:    "1.0.0",
				DeployOrder:     1,
				Required:        true,
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "tmpl-1", e["PartitionKey"])
					assert.NotEmpty(t, e["RowKey"])
					assert.Equal(t, "nginx", e["ChartName"])
					assert.Equal(t, true, e["Required"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "validation error - missing StackTemplateID",
			config: &models.TemplateChartConfig{
				ChartName: "nginx",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "azure error propagates",
			config: &models.TemplateChartConfig{
				StackTemplateID: "tmpl-1",
				ChartName:       "nginx",
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
			repo := azure.NewTestTemplateChartConfigRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Create(tt.config)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.config.ID)
				assert.False(t, tt.config.CreatedAt.IsZero())
			}
		})
	}
}

func TestTemplateChartConfigRepository_FindByID(t *testing.T) {
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
			id:   "cc-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":    "tmpl-1",
					"RowKey":          "cc-1",
					"ID":              "cc-1",
					"StackTemplateID": "tmpl-1",
					"ChartName":       "nginx",
					"ChartVersion":    "1.0.0",
					"DeployOrder":     1,
					"Required":        true,
					"CreatedAt":       "2024-01-01T00:00:00Z",
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
			repo := azure.NewTestTemplateChartConfigRepository()
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
				assert.Equal(t, "cc-1", result.ID)
				assert.Equal(t, "nginx", result.ChartName)
			}
		})
	}
}

func TestTemplateChartConfigRepository_Update(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		config    *models.TemplateChartConfig
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful update",
			config: &models.TemplateChartConfig{
				ID:              "cc-1",
				StackTemplateID: "tmpl-1",
				ChartName:       "updated-chart",
				ChartVersion:    "2.0.0",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "updated-chart", e["ChartName"])
					return aztables.UpdateEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error",
			config: &models.TemplateChartConfig{
				ID:              "cc-1",
				StackTemplateID: "tmpl-1",
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
			repo := azure.NewTestTemplateChartConfigRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Update(tt.config)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestTemplateChartConfigRepository_Delete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		id        string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful delete",
			id:   "cc-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				// FindByID scan first
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":    "tmpl-1",
					"RowKey":          "cc-1",
					"ID":              "cc-1",
					"StackTemplateID": "tmpl-1",
					"ChartName":       "nginx",
					"CreatedAt":       "2024-01-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
				m.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
					assert.Equal(t, "tmpl-1", pk)
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
			repo := azure.NewTestTemplateChartConfigRepository()
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

func TestTemplateChartConfigRepository_ListByTemplate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		templateID string
		setupMock  func(*testhelpers.MockTableClient)
		wantLen    int
		wantErr    bool
	}{
		{
			name:       "returns configs",
			templateID: "tmpl-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":    "tmpl-1",
					"RowKey":          "cc-1",
					"ID":              "cc-1",
					"StackTemplateID": "tmpl-1",
					"ChartName":       "nginx",
					"DeployOrder":     1,
					"CreatedAt":       "2024-01-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
			},
			wantLen: 1,
		},
		{
			name:       "empty",
			templateID: "tmpl-2",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantLen: 0,
		},
		{
			name:       "pager error",
			templateID: "tmpl-1",
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
			repo := azure.NewTestTemplateChartConfigRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			results, err := repo.ListByTemplate(tt.templateID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, results, tt.wantLen)
			}
		})
	}
}
