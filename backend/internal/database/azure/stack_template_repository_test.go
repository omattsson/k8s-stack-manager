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

func TestStackTemplateRepository_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		template  *models.StackTemplate
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful create",
			template: &models.StackTemplate{
				Name:          "web-app",
				Description:   "A web application template",
				Category:      "web",
				Version:       "1.0.0",
				OwnerID:       "alice",
				DefaultBranch: "main",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "global", e["PartitionKey"])
					assert.NotEmpty(t, e["RowKey"])
					assert.Equal(t, "web-app", e["Name"])
					assert.Equal(t, "alice", e["OwnerID"])
					assert.Equal(t, "main", e["DefaultBranch"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "generates ID when empty",
			template: &models.StackTemplate{
				Name:    "test-template",
				OwnerID: "bob",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.NotEmpty(t, e["ID"])
					assert.Equal(t, e["ID"], e["RowKey"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error propagates",
			template: &models.StackTemplate{
				Name:    "fail-template",
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
			repo := azure.NewTestStackTemplateRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Create(tt.template)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.template.ID)
				assert.False(t, tt.template.CreatedAt.IsZero())
				assert.False(t, tt.template.UpdatedAt.IsZero())
			}
		})
	}
}

func TestStackTemplateRepository_FindByID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		id        string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
		wantName  string
	}{
		{
			name: "found",
			id:   "tmpl-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					assert.Equal(t, "global", pk)
					assert.Equal(t, "tmpl-1", rk)
					data, _ := json.Marshal(map[string]interface{}{
						"PartitionKey":  "global",
						"RowKey":        "tmpl-1",
						"ID":            "tmpl-1",
						"Name":          "web-app",
						"Description":   "A web app template",
						"Category":      "web",
						"Version":       "1.0.0",
						"OwnerID":       "alice",
						"DefaultBranch": "main",
						"IsPublished":   true,
						"CreatedAt":     "2025-01-01T00:00:00Z",
						"UpdatedAt":     "2025-01-01T00:00:00Z",
					})
					return aztables.GetEntityResponse{Value: data}, nil
				})
			},
			wantName: "web-app",
		},
		{
			name: "not found",
			id:   "nonexistent",
			setupMock: func(m *testhelpers.MockTableClient) {
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
			repo := azure.NewTestStackTemplateRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			tmpl, err := repo.FindByID(tt.id)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantName, tmpl.Name)
				assert.True(t, tmpl.IsPublished)
			}
		})
	}
}

func TestStackTemplateRepository_Update(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		template  *models.StackTemplate
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful update",
			template: &models.StackTemplate{
				ID:          "tmpl-1",
				Name:        "updated-template",
				OwnerID:     "alice",
				IsPublished: true,
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "global", e["PartitionKey"])
					assert.Equal(t, "tmpl-1", e["RowKey"])
					assert.Equal(t, "updated-template", e["Name"])
					return aztables.UpdateEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error propagates",
			template: &models.StackTemplate{
				ID:   "tmpl-1",
				Name: "fail-update",
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
			repo := azure.NewTestStackTemplateRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Update(tt.template)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.False(t, tt.template.UpdatedAt.IsZero())
			}
		})
	}
}

func TestStackTemplateRepository_Delete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		id        string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful delete",
			id:   "tmpl-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
					assert.Equal(t, "global", pk)
					assert.Equal(t, "tmpl-1", rk)
					return aztables.DeleteEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error propagates",
			id:   "tmpl-fail",
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
			repo := azure.NewTestStackTemplateRepository()
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

func TestStackTemplateRepository_List(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		setupMock func(*testhelpers.MockTableClient)
		wantLen   int
		wantErr   bool
	}{
		{
			name: "returns multiple templates",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					d1, _ := json.Marshal(map[string]interface{}{
						"ID": "tmpl-1", "Name": "web-app", "OwnerID": "alice",
						"IsPublished": true, "CreatedAt": "2025-01-01T00:00:00Z", "UpdatedAt": "2025-01-01T00:00:00Z",
					})
					d2, _ := json.Marshal(map[string]interface{}{
						"ID": "tmpl-2", "Name": "api-service", "OwnerID": "bob",
						"IsPublished": false, "CreatedAt": "2025-01-01T00:00:00Z", "UpdatedAt": "2025-01-01T00:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{d1, d2}, nil)
				})
			},
			wantLen: 2,
		},
		{
			name: "empty result",
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
			repo := azure.NewTestStackTemplateRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			templates, err := repo.List()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, templates, tt.wantLen)
			}
		})
	}
}

func TestStackTemplateRepository_ListPublished(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestStackTemplateRepository()
	mock := testhelpers.NewMockTableClient()
	mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
		d1, _ := json.Marshal(map[string]interface{}{
			"ID": "tmpl-1", "Name": "web-app", "IsPublished": true,
			"CreatedAt": "2025-01-01T00:00:00Z", "UpdatedAt": "2025-01-01T00:00:00Z",
		})
		return testhelpers.NewMockTablePager([][]byte{d1}, nil)
	})
	repo.SetTestClient(mock)

	templates, err := repo.ListPublished()
	require.NoError(t, err)
	assert.Len(t, templates, 1)
	assert.True(t, templates[0].IsPublished)
}

func TestStackTemplateRepository_ListByOwner(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestStackTemplateRepository()
	mock := testhelpers.NewMockTableClient()
	mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
		d1, _ := json.Marshal(map[string]interface{}{
			"ID": "tmpl-1", "Name": "web-app", "OwnerID": "alice",
			"CreatedAt": "2025-01-01T00:00:00Z", "UpdatedAt": "2025-01-01T00:00:00Z",
		})
		return testhelpers.NewMockTablePager([][]byte{d1}, nil)
	})
	repo.SetTestClient(mock)

	templates, err := repo.ListByOwner("alice")
	require.NoError(t, err)
	assert.Len(t, templates, 1)
	assert.Equal(t, "alice", templates[0].OwnerID)
}
