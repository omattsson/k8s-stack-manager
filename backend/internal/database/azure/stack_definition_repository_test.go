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

func TestStackDefinitionRepository_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		definition *models.StackDefinition
		setupMock  func(*testhelpers.MockTableClient)
		wantErr    bool
	}{
		{
			name: "successful create",
			definition: &models.StackDefinition{
				Name:        "my-stack",
				Description: "A stack definition",
				OwnerID:     "user-1",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "global", e["PartitionKey"])
					assert.NotEmpty(t, e["RowKey"])
					assert.Equal(t, "my-stack", e["Name"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error propagates",
			definition: &models.StackDefinition{
				Name:    "my-stack",
				OwnerID: "user-1",
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
			repo := azure.NewTestStackDefinitionRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Create(tt.definition)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.definition.ID)
				assert.False(t, tt.definition.CreatedAt.IsZero())
			}
		})
	}
}

func TestStackDefinitionRepository_FindByID(t *testing.T) {
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
			id:   "def-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":     "global",
					"RowKey":           "def-1",
					"ID":               "def-1",
					"Name":             "my-stack",
					"Description":      "desc",
					"OwnerID":          "user-1",
					"DefaultBranch":    "main",
					"SourceTemplateID": "",
					"CreatedAt":        "2024-01-01T00:00:00Z",
					"UpdatedAt":        "2024-01-01T00:00:00Z",
				})
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					assert.Equal(t, "global", pk)
					assert.Equal(t, "def-1", rk)
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
			repo := azure.NewTestStackDefinitionRepository()
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
				assert.Equal(t, "def-1", result.ID)
				assert.Equal(t, "my-stack", result.Name)
			}
		})
	}
}

func TestStackDefinitionRepository_Update(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		definition *models.StackDefinition
		setupMock  func(*testhelpers.MockTableClient)
		wantErr    bool
	}{
		{
			name: "successful update",
			definition: &models.StackDefinition{
				ID:      "def-1",
				Name:    "updated-stack",
				OwnerID: "user-1",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "updated-stack", e["Name"])
					return aztables.UpdateEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error",
			definition: &models.StackDefinition{
				ID:      "def-1",
				Name:    "updated",
				OwnerID: "user-1",
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
			repo := azure.NewTestStackDefinitionRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Update(tt.definition)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestStackDefinitionRepository_Delete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		id        string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful delete",
			id:   "def-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
					assert.Equal(t, "global", pk)
					assert.Equal(t, "def-1", rk)
					return aztables.DeleteEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error",
			id:   "def-1",
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
			repo := azure.NewTestStackDefinitionRepository()
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

func TestStackDefinitionRepository_List(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		setupMock func(*testhelpers.MockTableClient)
		wantLen   int
		wantErr   bool
	}{
		{
			name: "returns definitions",
			setupMock: func(m *testhelpers.MockTableClient) {
				data1, _ := json.Marshal(map[string]interface{}{
					"PartitionKey": "global",
					"RowKey":       "def-1",
					"ID":           "def-1",
					"Name":         "stack-1",
					"OwnerID":      "user-1",
					"CreatedAt":    "2024-01-01T00:00:00Z",
					"UpdatedAt":    "2024-01-01T00:00:00Z",
				})
				data2, _ := json.Marshal(map[string]interface{}{
					"PartitionKey": "global",
					"RowKey":       "def-2",
					"ID":           "def-2",
					"Name":         "stack-2",
					"OwnerID":      "user-2",
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
			repo := azure.NewTestStackDefinitionRepository()
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

func TestStackDefinitionRepository_ListByOwner(t *testing.T) {
	t.Parallel()

	repo := azure.NewTestStackDefinitionRepository()
	mock := testhelpers.NewMockTableClient()
	data, _ := json.Marshal(map[string]interface{}{
		"PartitionKey": "global",
		"RowKey":       "def-1",
		"ID":           "def-1",
		"Name":         "stack-1",
		"OwnerID":      "user-1",
		"CreatedAt":    "2024-01-01T00:00:00Z",
		"UpdatedAt":    "2024-01-01T00:00:00Z",
	})
	mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
		return testhelpers.NewMockTablePager([][]byte{data}, nil)
	})
	repo.SetTestClient(mock)

	results, err := repo.ListByOwner("user-1")
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "user-1", results[0].OwnerID)
}

func TestStackDefinitionRepository_ListByTemplate(t *testing.T) {
	t.Parallel()

	repo := azure.NewTestStackDefinitionRepository()
	mock := testhelpers.NewMockTableClient()
	data, _ := json.Marshal(map[string]interface{}{
		"PartitionKey":     "global",
		"RowKey":           "def-1",
		"ID":               "def-1",
		"Name":             "stack-1",
		"OwnerID":          "user-1",
		"SourceTemplateID": "tmpl-1",
		"CreatedAt":        "2024-01-01T00:00:00Z",
		"UpdatedAt":        "2024-01-01T00:00:00Z",
	})
	mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
		return testhelpers.NewMockTablePager([][]byte{data}, nil)
	})
	repo.SetTestClient(mock)

	results, err := repo.ListByTemplate("tmpl-1")
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "tmpl-1", results[0].SourceTemplateID)
}
