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

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserFavoriteRepository_Add(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		fav       *models.UserFavorite
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful add",
			fav: &models.UserFavorite{
				UserID:     "user-1",
				EntityType: "template",
				EntityID:   "tmpl-1",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetUpsertEntity(func(ctx context.Context, entity []byte, opts *aztables.UpsertEntityOptions) (aztables.UpsertEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "user-1", e["PartitionKey"])
					assert.Equal(t, "template:tmpl-1", e["RowKey"])
					assert.Equal(t, "template", e["EntityType"])
					return aztables.UpsertEntityResponse{}, nil
				})
			},
		},
		{
			name: "validation error - missing user_id",
			fav: &models.UserFavorite{
				EntityType: "template",
				EntityID:   "tmpl-1",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "validation error - missing entity_type",
			fav: &models.UserFavorite{
				UserID:   "user-1",
				EntityID: "tmpl-1",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "validation error - invalid entity_type",
			fav: &models.UserFavorite{
				UserID:     "user-1",
				EntityType: "invalid",
				EntityID:   "tmpl-1",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "validation error - missing entity_id",
			fav: &models.UserFavorite{
				UserID:     "user-1",
				EntityType: "instance",
			},
			setupMock: func(m *testhelpers.MockTableClient) {},
			wantErr:   true,
		},
		{
			name: "azure error",
			fav: &models.UserFavorite{
				UserID:     "user-1",
				EntityType: "definition",
				EntityID:   "def-1",
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
			repo := azure.NewTestUserFavoriteRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Add(tt.fav)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.fav.ID)
				assert.False(t, tt.fav.CreatedAt.IsZero())
			}
		})
	}
}

func TestUserFavoriteRepository_Remove(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		userID     string
		entityType string
		entityID   string
		setupMock  func(*testhelpers.MockTableClient)
		wantErr    bool
	}{
		{
			name:       "successful remove",
			userID:     "user-1",
			entityType: "template",
			entityID:   "tmpl-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
					assert.Equal(t, "user-1", pk)
					assert.Equal(t, "template:tmpl-1", rk)
					return aztables.DeleteEntityResponse{}, nil
				})
			},
		},
		{
			name:       "azure error",
			userID:     "user-1",
			entityType: "template",
			entityID:   "tmpl-1",
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
			repo := azure.NewTestUserFavoriteRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Remove(tt.userID, tt.entityType, tt.entityID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestUserFavoriteRepository_List(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		userID    string
		setupMock func(*testhelpers.MockTableClient)
		wantLen   int
		wantErr   bool
	}{
		{
			name:   "returns favorites",
			userID: "user-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data1, _ := json.Marshal(map[string]interface{}{
					"PartitionKey": "user-1",
					"RowKey":       "template:tmpl-1",
					"ID":           "fav-1",
					"UserID":       "user-1",
					"EntityType":   "template",
					"EntityID":     "tmpl-1",
					"CreatedAt":    "2024-01-01T00:00:00Z",
				})
				data2, _ := json.Marshal(map[string]interface{}{
					"PartitionKey": "user-1",
					"RowKey":       "instance:inst-1",
					"ID":           "fav-2",
					"UserID":       "user-1",
					"EntityType":   "instance",
					"EntityID":     "inst-1",
					"CreatedAt":    "2024-01-02T00:00:00Z",
				})
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager([][]byte{data1, data2}, nil)
				})
			},
			wantLen: 2,
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
		{
			name:   "pager error",
			userID: "user-1",
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
			repo := azure.NewTestUserFavoriteRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			results, err := repo.List(tt.userID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, results, tt.wantLen)
			}
		})
	}
}

func TestUserFavoriteRepository_IsFavorite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		userID     string
		entityType string
		entityID   string
		setupMock  func(*testhelpers.MockTableClient)
		want       bool
		wantErr    bool
	}{
		{
			name:       "is favorite",
			userID:     "user-1",
			entityType: "template",
			entityID:   "tmpl-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey": "user-1",
					"RowKey":       "template:tmpl-1",
				})
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					assert.Equal(t, "user-1", pk)
					assert.Equal(t, "template:tmpl-1", rk)
					return aztables.GetEntityResponse{Value: data}, nil
				})
			},
			want: true,
		},
		{
			name:       "not favorite - not found",
			userID:     "user-1",
			entityType: "template",
			entityID:   "tmpl-2",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					return aztables.GetEntityResponse{}, &azcore.ResponseError{StatusCode: 404}
				})
			},
			want: false,
		},
		{
			name:       "azure error",
			userID:     "user-1",
			entityType: "template",
			entityID:   "tmpl-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					return aztables.GetEntityResponse{}, errors.New("connection failed")
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestUserFavoriteRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			result, err := repo.IsFavorite(tt.userID, tt.entityType, tt.entityID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, result)
			}
		})
	}
}
