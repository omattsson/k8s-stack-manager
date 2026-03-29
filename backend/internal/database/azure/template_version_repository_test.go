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

func TestTemplateVersionRepository_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		version   *models.TemplateVersion
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful create",
			version: &models.TemplateVersion{
				ID:            "v-1",
				TemplateID:    "tmpl-1",
				Version:       "1.0.0",
				Snapshot:      `{"charts":[]}`,
				ChangeSummary: "Initial version",
				CreatedBy:     "admin",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "tmpl-1", e["PartitionKey"])
					assert.Equal(t, "v-1", e["RowKey"])
					assert.Equal(t, "1.0.0", e["Version"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error",
			version: &models.TemplateVersion{
				ID:         "v-1",
				TemplateID: "tmpl-1",
				Version:    "1.0.0",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					return aztables.AddEntityResponse{}, errors.New("create failed")
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestTemplateVersionRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Create(context.Background(), tt.version)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestTemplateVersionRepository_ListByTemplate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		templateID string
		setupMock  func(*testhelpers.MockTableClient)
		wantLen    int
		wantFirst  string
		wantErr    bool
	}{
		{
			name:       "returns sorted newest first",
			templateID: "tmpl-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data1, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":  "tmpl-1",
					"RowKey":        "v-1",
					"TemplateID":    "tmpl-1",
					"Version":       "1.0.0",
					"Snapshot":      "{}",
					"ChangeSummary": "First",
					"CreatedBy":     "admin",
					"CreatedAt":     "2024-01-01T00:00:00Z",
				})
				data2, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":  "tmpl-1",
					"RowKey":        "v-2",
					"TemplateID":    "tmpl-1",
					"Version":       "2.0.0",
					"Snapshot":      "{}",
					"ChangeSummary": "Second",
					"CreatedBy":     "admin",
					"CreatedAt":     "2024-06-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data1, data2}, nil)
				})
			},
			wantLen:   2,
			wantFirst: "2.0.0", // newer comes first
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
					return testhelpers.NewMockTablePager([][]byte{{}}, errors.New("list error"))
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestTemplateVersionRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			results, err := repo.ListByTemplate(context.Background(), tt.templateID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, results, tt.wantLen)
				if tt.wantFirst != "" && len(results) > 0 {
					assert.Equal(t, tt.wantFirst, results[0].Version)
				}
			}
		})
	}
}

func TestTemplateVersionRepository_GetByID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		templateID string
		id         string
		setupMock  func(*testhelpers.MockTableClient)
		wantErr    bool
		errTarget  error
	}{
		{
			name:       "found",
			templateID: "tmpl-1",
			id:         "v-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":  "tmpl-1",
					"RowKey":        "v-1",
					"TemplateID":    "tmpl-1",
					"Version":       "1.0.0",
					"Snapshot":      `{"charts":[]}`,
					"ChangeSummary": "Initial",
					"CreatedBy":     "admin",
					"CreatedAt":     "2024-01-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
			},
		},
		{
			name:       "not found",
			templateID: "tmpl-1",
			id:         "missing",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantErr:   true,
			errTarget: dberrors.ErrNotFound,
		},
		{
			name:       "pager error",
			templateID: "tmpl-1",
			id:         "v-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{{}}, errors.New("find error"))
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestTemplateVersionRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			result, err := repo.GetByID(context.Background(), tt.templateID, tt.id)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errTarget != nil {
					assert.True(t, errors.Is(err, tt.errTarget))
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, "v-1", result.ID)
				assert.Equal(t, "1.0.0", result.Version)
				assert.Equal(t, "admin", result.CreatedBy)
			}
		})
	}
}

func TestTemplateVersionRepository_GetLatestByTemplate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		templateID string
		setupMock  func(*testhelpers.MockTableClient)
		wantVer    string
		wantErr    bool
		errTarget  error
	}{
		{
			name:       "returns newest",
			templateID: "tmpl-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data1, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":  "tmpl-1",
					"RowKey":        "v-1",
					"TemplateID":    "tmpl-1",
					"Version":       "1.0.0",
					"Snapshot":      "{}",
					"ChangeSummary": "First",
					"CreatedBy":     "admin",
					"CreatedAt":     "2024-01-01T00:00:00Z",
				})
				data2, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":  "tmpl-1",
					"RowKey":        "v-2",
					"TemplateID":    "tmpl-1",
					"Version":       "2.0.0",
					"Snapshot":      "{}",
					"ChangeSummary": "Second",
					"CreatedBy":     "admin",
					"CreatedAt":     "2024-06-01T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data1, data2}, nil)
				})
			},
			wantVer: "2.0.0",
		},
		{
			name:       "empty - not found",
			templateID: "tmpl-2",
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
			repo := azure.NewTestTemplateVersionRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			result, err := repo.GetLatestByTemplate(context.Background(), tt.templateID)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errTarget != nil {
					assert.True(t, errors.Is(err, tt.errTarget))
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantVer, result.Version)
			}
		})
	}
}
