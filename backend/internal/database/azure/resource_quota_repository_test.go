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

func TestResourceQuotaRepository_GetByClusterID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		clusterID string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
		errTarget error
	}{
		{
			name:      "found",
			clusterID: "cluster-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":  "cluster-1",
					"RowKey":        "quota",
					"ID":            "rq-1",
					"ClusterID":     "cluster-1",
					"CPURequest":    "1",
					"CPULimit":      "4",
					"MemoryRequest": "2Gi",
					"MemoryLimit":   "8Gi",
					"StorageLimit":  "50Gi",
					"PodLimit":      100,
					"CreatedAt":     "2024-01-01T00:00:00Z",
					"UpdatedAt":     "2024-01-01T00:00:00Z",
				})
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					assert.Equal(t, "cluster-1", pk)
					assert.Equal(t, "quota", rk)
					return aztables.GetEntityResponse{Value: data}, nil
				})
			},
		},
		{
			name:      "not found",
			clusterID: "missing",
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
			repo := azure.NewTestResourceQuotaRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			result, err := repo.GetByClusterID(context.Background(), tt.clusterID)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errTarget != nil {
					assert.True(t, errors.Is(err, tt.errTarget))
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, "rq-1", result.ID)
				assert.Equal(t, "cluster-1", result.ClusterID)
				assert.Equal(t, "4", result.CPULimit)
				assert.Equal(t, 100, result.PodLimit)
			}
		})
	}
}

func TestResourceQuotaRepository_Upsert_Create(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestResourceQuotaRepository()
	mock := testhelpers.NewMockTableClient()

	mock.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
		return aztables.GetEntityResponse{}, &azcore.ResponseError{StatusCode: 404}
	})
	mock.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
		var e map[string]interface{}
		_ = json.Unmarshal(entity, &e)
		assert.Equal(t, "cluster-1", e["PartitionKey"])
		assert.Equal(t, "quota", e["RowKey"])
		return aztables.AddEntityResponse{}, nil
	})
	repo.SetTestClient(mock)

	config := &models.ResourceQuotaConfig{
		ClusterID:     "cluster-1",
		CPURequest:    "1",
		CPULimit:      "4",
		MemoryRequest: "2Gi",
		MemoryLimit:   "8Gi",
		StorageLimit:  "50Gi",
		PodLimit:      100,
	}
	err := repo.Upsert(context.Background(), config)
	assert.NoError(t, err)
	assert.NotEmpty(t, config.ID)
	assert.False(t, config.CreatedAt.IsZero())
}

func TestResourceQuotaRepository_Upsert_Update(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestResourceQuotaRepository()
	mock := testhelpers.NewMockTableClient()

	existingData, _ := json.Marshal(map[string]interface{}{
		"PartitionKey": "cluster-1",
		"RowKey":       "quota",
		"ID":           "rq-1",
		"ClusterID":    "cluster-1",
		"CreatedAt":    "2024-01-01T00:00:00Z",
		"UpdatedAt":    "2024-01-01T00:00:00Z",
	})
	mock.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
		return aztables.GetEntityResponse{Value: existingData}, nil
	})
	mock.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
		var e map[string]interface{}
		_ = json.Unmarshal(entity, &e)
		assert.Equal(t, "8", e["CPULimit"])
		return aztables.UpdateEntityResponse{}, nil
	})
	repo.SetTestClient(mock)

	config := &models.ResourceQuotaConfig{
		ID:        "rq-1",
		ClusterID: "cluster-1",
		CPULimit:  "8",
	}
	err := repo.Upsert(context.Background(), config)
	assert.NoError(t, err)
}

func TestResourceQuotaRepository_Upsert_CheckError(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestResourceQuotaRepository()
	mock := testhelpers.NewMockTableClient()

	mock.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
		return aztables.GetEntityResponse{}, errors.New("connection failed")
	})
	repo.SetTestClient(mock)

	config := &models.ResourceQuotaConfig{ClusterID: "cluster-1"}
	err := repo.Upsert(context.Background(), config)
	assert.Error(t, err)
}

func TestResourceQuotaRepository_Delete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		clusterID string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name:      "successful delete",
			clusterID: "cluster-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
					assert.Equal(t, "cluster-1", pk)
					assert.Equal(t, "quota", rk)
					return aztables.DeleteEntityResponse{}, nil
				})
			},
		},
		{
			name:      "azure error",
			clusterID: "cluster-1",
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
			repo := azure.NewTestResourceQuotaRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Delete(context.Background(), tt.clusterID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
