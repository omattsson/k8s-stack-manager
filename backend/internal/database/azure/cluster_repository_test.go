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

func TestClusterRepository_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		cluster   *models.Cluster
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
		errMsg    string
	}{
		{
			name: "successful create with kubeconfig data",
			cluster: &models.Cluster{
				Name:           "prod-cluster",
				APIServerURL:   "https://k8s.example.com",
				KubeconfigData: "apiVersion: v1...",
				Region:         "westus2",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "clusters", e["PartitionKey"])
					assert.NotEmpty(t, e["RowKey"])
					assert.Equal(t, "prod-cluster", e["Name"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "successful create with kubeconfig path",
			cluster: &models.Cluster{
				Name:           "dev-cluster",
				APIServerURL:   "https://dev.k8s.example.com",
				KubeconfigPath: "/etc/kubeconfig/dev",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "generates ID when empty",
			cluster: &models.Cluster{
				Name:           "test",
				APIServerURL:   "https://test.example.com",
				KubeconfigData: "data",
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
			name: "defaults health status to unreachable",
			cluster: &models.Cluster{
				Name:           "new-cluster",
				APIServerURL:   "https://k8s.example.com",
				KubeconfigData: "data",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "unreachable", e["HealthStatus"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error propagates",
			cluster: &models.Cluster{
				Name:           "cluster",
				APIServerURL:   "https://k8s.example.com",
				KubeconfigData: "data",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					return aztables.AddEntityResponse{}, errors.New("azure error")
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestClusterRepository("")
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Create(tt.cluster)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.cluster.ID)
				assert.False(t, tt.cluster.CreatedAt.IsZero())
			}
		})
	}
}

func TestClusterRepository_FindByID(t *testing.T) {
	t.Parallel()
	t.Run("found", func(t *testing.T) {
		t.Parallel()
		repo := azure.NewTestClusterRepository("")
		mock := testhelpers.NewMockTableClient()
		mock.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
			assert.Equal(t, "clusters", pk)
			assert.Equal(t, "c1", rk)
			data, _ := json.Marshal(map[string]interface{}{
				"PartitionKey":   "clusters",
				"RowKey":         "c1",
				"ID":             "c1",
				"Name":           "prod",
				"APIServerURL":   "https://k8s.example.com",
				"KubeconfigPath": "/path",
				"HealthStatus":   "healthy",
				"IsDefault":      true,
				"MaxNamespaces":  float64(50),
			})
			return aztables.GetEntityResponse{Value: data}, nil
		})
		repo.SetTestClient(mock)
		cluster, err := repo.FindByID("c1")
		require.NoError(t, err)
		assert.Equal(t, "c1", cluster.ID)
		assert.Equal(t, "prod", cluster.Name)
		assert.Equal(t, "https://k8s.example.com", cluster.APIServerURL)
		assert.Equal(t, "/path", cluster.KubeconfigPath)
		assert.Equal(t, "healthy", cluster.HealthStatus)
		assert.True(t, cluster.IsDefault)
		assert.Equal(t, 50, cluster.MaxNamespaces)
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		repo := azure.NewTestClusterRepository("")
		mock := testhelpers.NewMockTableClient()
		mock.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
			return aztables.GetEntityResponse{}, &dberrors.DatabaseError{Op: "find_by_id", Err: dberrors.ErrNotFound}
		})
		repo.SetTestClient(mock)
		_, err := repo.FindByID("nonexistent")
		assert.Error(t, err)
	})
}

func TestClusterRepository_Update(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestClusterRepository("")
	mock := testhelpers.NewMockTableClient()
	mock.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
		var e map[string]interface{}
		_ = json.Unmarshal(entity, &e)
		assert.Equal(t, "clusters", e["PartitionKey"])
		assert.Equal(t, "c1", e["RowKey"])
		assert.Equal(t, "updated-name", e["Name"])
		return aztables.UpdateEntityResponse{}, nil
	})
	repo.SetTestClient(mock)
	cluster := &models.Cluster{
		ID:             "c1",
		Name:           "updated-name",
		APIServerURL:   "https://k8s.example.com",
		KubeconfigPath: "/path",
	}
	err := repo.Update(cluster)
	assert.NoError(t, err)
	assert.False(t, cluster.UpdatedAt.IsZero())
}

func TestClusterRepository_Delete(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestClusterRepository("")
	mock := testhelpers.NewMockTableClient()
	mock.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
		assert.Equal(t, "clusters", pk)
		assert.Equal(t, "c1", rk)
		return aztables.DeleteEntityResponse{}, nil
	})
	repo.SetTestClient(mock)
	err := repo.Delete("c1")
	assert.NoError(t, err)
}

func TestClusterRepository_List(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestClusterRepository("")
	mock := testhelpers.NewMockTableClient()
	mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
		d1, _ := json.Marshal(map[string]interface{}{"ID": "c1", "Name": "prod", "HealthStatus": "healthy"})
		d2, _ := json.Marshal(map[string]interface{}{"ID": "c2", "Name": "dev", "HealthStatus": "unreachable"})
		return testhelpers.NewMockTablePager([][]byte{d1, d2}, nil)
	})
	repo.SetTestClient(mock)
	clusters, err := repo.List()
	require.NoError(t, err)
	assert.Len(t, clusters, 2)
}

func TestClusterRepository_FindDefault(t *testing.T) {
	t.Parallel()
	t.Run("found", func(t *testing.T) {
		t.Parallel()
		repo := azure.NewTestClusterRepository("")
		mock := testhelpers.NewMockTableClient()
		mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
			d1, _ := json.Marshal(map[string]interface{}{"ID": "c1", "Name": "prod", "IsDefault": false})
			d2, _ := json.Marshal(map[string]interface{}{"ID": "c2", "Name": "default-cluster", "IsDefault": true})
			return testhelpers.NewMockTablePager([][]byte{d1, d2}, nil)
		})
		repo.SetTestClient(mock)
		cluster, err := repo.FindDefault()
		require.NoError(t, err)
		assert.Equal(t, "c2", cluster.ID)
		assert.Equal(t, "default-cluster", cluster.Name)
	})

	t.Run("no default", func(t *testing.T) {
		t.Parallel()
		repo := azure.NewTestClusterRepository("")
		mock := testhelpers.NewMockTableClient()
		mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
			d1, _ := json.Marshal(map[string]interface{}{"ID": "c1", "Name": "prod", "IsDefault": false})
			return testhelpers.NewMockTablePager([][]byte{d1}, nil)
		})
		repo.SetTestClient(mock)
		_, err := repo.FindDefault()
		assert.Error(t, err)
		assert.ErrorIs(t, err, dberrors.ErrNotFound)
	})
}

func TestClusterRepository_SetDefault(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestClusterRepository("")
	mock := testhelpers.NewMockTableClient()
	updateCount := 0

	listCallCount := 0
	mock.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
		listCallCount++
		if listCallCount == 1 {
			// Current default
			d1, _ := json.Marshal(map[string]interface{}{
				"PartitionKey": "clusters",
				"RowKey":       "c1",
				"ID":           "c1",
				"Name":         "old-default",
				"IsDefault":    true,
			})
			return testhelpers.NewMockTablePager([][]byte{d1}, nil)
		}
		return testhelpers.NewMockTablePager(nil, nil)
	})

	mock.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
		updateCount++
		return aztables.UpdateEntityResponse{}, nil
	})

	// GetEntity for FindByID of the target cluster
	mock.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
		data, _ := json.Marshal(map[string]interface{}{
			"PartitionKey": "clusters",
			"RowKey":       "c2",
			"ID":           "c2",
			"Name":         "new-default",
			"IsDefault":    false,
		})
		return aztables.GetEntityResponse{Value: data}, nil
	})

	repo.SetTestClient(mock)
	err := repo.SetDefault("c2")
	assert.NoError(t, err)
	// Should have updated: 1 to unset old default + 1 to set new default
	assert.Equal(t, 2, updateCount)
}

func TestClusterRepository_Encryption(t *testing.T) {
	t.Parallel()
	encKey := "test-encryption-key"

	t.Run("encrypt on create, decrypt on read", func(t *testing.T) {
		t.Parallel()
		repo := azure.NewTestClusterRepository(encKey)
		mock := testhelpers.NewMockTableClient()
		var storedEntity []byte

		mock.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
			storedEntity = entity
			return aztables.AddEntityResponse{}, nil
		})
		repo.SetTestClient(mock)

		cluster := &models.Cluster{
			Name:           "encrypted-cluster",
			APIServerURL:   "https://k8s.example.com",
			KubeconfigData: "super-secret-kubeconfig",
		}
		err := repo.Create(cluster)
		require.NoError(t, err)

		// Verify stored data is encrypted (not plaintext)
		var e map[string]interface{}
		require.NoError(t, json.Unmarshal(storedEntity, &e))
		assert.NotEqual(t, "super-secret-kubeconfig", e["KubeconfigData"])
		assert.NotEmpty(t, e["KubeconfigData"])

		// Now read it back
		mock.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
			return aztables.GetEntityResponse{Value: storedEntity}, nil
		})
		found, err := repo.FindByID(cluster.ID)
		require.NoError(t, err)
		assert.Equal(t, "super-secret-kubeconfig", found.KubeconfigData)
	})

	t.Run("no encryption when key is empty", func(t *testing.T) {
		t.Parallel()
		repo := azure.NewTestClusterRepository("")
		mock := testhelpers.NewMockTableClient()
		var storedEntity []byte

		mock.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
			storedEntity = entity
			return aztables.AddEntityResponse{}, nil
		})
		repo.SetTestClient(mock)

		cluster := &models.Cluster{
			Name:           "plain-cluster",
			APIServerURL:   "https://k8s.example.com",
			KubeconfigData: "plain-kubeconfig",
		}
		err := repo.Create(cluster)
		require.NoError(t, err)

		var e map[string]interface{}
		require.NoError(t, json.Unmarshal(storedEntity, &e))
		assert.Equal(t, "plain-kubeconfig", e["KubeconfigData"])
	})
}
