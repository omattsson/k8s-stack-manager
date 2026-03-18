package azure_test

import (
	"context"
	"encoding/json"
	"testing"

	"backend/internal/database/azure"
	"backend/internal/database/azure/testhelpers"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserRepository_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		user      *models.User
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
		errMsg    string
	}{
		{
			name: "successful create",
			user: &models.User{Username: "alice", DisplayName: "Alice", Role: "admin"},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name:    "missing username returns validation error",
			user:    &models.User{DisplayName: "NoUsername"},
			wantErr: true,
			errMsg:  "validation",
		},
		{
			name: "generates ID when empty",
			user: &models.User{Username: "bob", Role: "user"},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.NotEmpty(t, e["ID"])
					assert.Equal(t, "users", e["PartitionKey"])
					assert.Equal(t, "bob", e["RowKey"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestUserRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Create(tt.user)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.user.ID)
				assert.False(t, tt.user.CreatedAt.IsZero())
			}
		})
	}
}

func TestUserRepository_FindByUsername(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestUserRepository()
	mock := testhelpers.NewMockTableClient()
	mock.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
		assert.Equal(t, "users", pk)
		assert.Equal(t, "alice", rk)
		data, _ := json.Marshal(map[string]interface{}{
			"PartitionKey": "users",
			"RowKey":       "alice",
			"ID":           "uid-1",
			"Username":     "alice",
			"DisplayName":  "Alice",
			"Role":         "admin",
		})
		return aztables.GetEntityResponse{Value: data}, nil
	})
	repo.SetTestClient(mock)
	user, err := repo.FindByUsername("alice")
	require.NoError(t, err)
	assert.Equal(t, "uid-1", user.ID)
	assert.Equal(t, "alice", user.Username)
	assert.Equal(t, "admin", user.Role)
}

func TestUserRepository_FindByID(t *testing.T) {
	t.Parallel()
	t.Run("found", func(t *testing.T) {
		t.Parallel()
		repo := azure.NewTestUserRepository()
		mock := testhelpers.NewMockTableClient()
		mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
			data, _ := json.Marshal(map[string]interface{}{
				"PartitionKey": "users",
				"RowKey":       "alice",
				"ID":           "uid-1",
				"Username":     "alice",
			})
			return testhelpers.NewMockTablePager([][]byte{data}, nil)
		})
		repo.SetTestClient(mock)
		user, err := repo.FindByID("uid-1")
		require.NoError(t, err)
		assert.Equal(t, "uid-1", user.ID)
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		repo := azure.NewTestUserRepository()
		mock := testhelpers.NewMockTableClient()
		mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
			return testhelpers.NewMockTablePager(nil, nil)
		})
		repo.SetTestClient(mock)
		_, err := repo.FindByID("nonexistent")
		assert.Error(t, err)
		assert.ErrorIs(t, err, dberrors.ErrNotFound)
	})
}

func TestUserRepository_Update(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestUserRepository()
	mock := testhelpers.NewMockTableClient()
	mock.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
		var e map[string]interface{}
		_ = json.Unmarshal(entity, &e)
		assert.Equal(t, "users", e["PartitionKey"])
		assert.Equal(t, "alice", e["RowKey"])
		assert.Equal(t, "Alice Updated", e["DisplayName"])
		return aztables.UpdateEntityResponse{}, nil
	})
	repo.SetTestClient(mock)
	user := &models.User{ID: "uid-1", Username: "alice", DisplayName: "Alice Updated", Role: "admin"}
	err := repo.Update(user)
	assert.NoError(t, err)
	assert.False(t, user.UpdatedAt.IsZero())
}

func TestUserRepository_Delete(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestUserRepository()
	mock := testhelpers.NewMockTableClient()
	// FindByID scan
	mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
		data, _ := json.Marshal(map[string]interface{}{
			"PartitionKey": "users",
			"RowKey":       "alice",
			"ID":           "uid-1",
			"Username":     "alice",
		})
		return testhelpers.NewMockTablePager([][]byte{data}, nil)
	})
	mock.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
		assert.Equal(t, "users", pk)
		assert.Equal(t, "alice", rk)
		return aztables.DeleteEntityResponse{}, nil
	})
	repo.SetTestClient(mock)
	err := repo.Delete("uid-1")
	assert.NoError(t, err)
}

func TestUserRepository_List(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestUserRepository()
	mock := testhelpers.NewMockTableClient()
	mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
		d1, _ := json.Marshal(map[string]interface{}{"ID": "1", "Username": "alice"})
		d2, _ := json.Marshal(map[string]interface{}{"ID": "2", "Username": "bob"})
		return testhelpers.NewMockTablePager([][]byte{d1, d2}, nil)
	})
	repo.SetTestClient(mock)
	users, err := repo.List()
	require.NoError(t, err)
	assert.Len(t, users, 2)
}
