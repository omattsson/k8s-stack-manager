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

func TestInstanceQuotaOverrideRepository_GetByInstanceID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		instanceID string
		setupMock  func(*testhelpers.MockTableClient)
		wantErr    bool
		errTarget  error
	}{
		{
			name:       "found",
			instanceID: "inst-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				data, _ := json.Marshal(map[string]interface{}{
					"PartitionKey":    "inst-1",
					"RowKey":          "quota",
					"ID":              "q-1",
					"StackInstanceID": "inst-1",
					"CPURequest":      "100m",
					"CPULimit":        "500m",
					"MemoryRequest":   "128Mi",
					"MemoryLimit":     "512Mi",
					"StorageLimit":    "1Gi",
					"PodLimit":        10,
					"CreatedAt":       "2024-01-01T00:00:00Z",
					"UpdatedAt":       "2024-01-01T00:00:00Z",
				})
				m.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
					assert.Equal(t, "inst-1", pk)
					assert.Equal(t, "quota", rk)
					return aztables.GetEntityResponse{Value: data}, nil
				})
			},
		},
		{
			name:       "not found",
			instanceID: "missing",
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
			repo := azure.NewTestInstanceQuotaOverrideRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			result, err := repo.GetByInstanceID(context.Background(), tt.instanceID)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errTarget != nil {
					assert.True(t, errors.Is(err, tt.errTarget))
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, "q-1", result.ID)
				assert.Equal(t, "inst-1", result.StackInstanceID)
				assert.Equal(t, "100m", result.CPURequest)
				assert.Equal(t, "512Mi", result.MemoryLimit)
			}
		})
	}
}

func TestInstanceQuotaOverrideRepository_Upsert_Create(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestInstanceQuotaOverrideRepository()
	mock := testhelpers.NewMockTableClient()

	// GetEntity returns not found → triggers create path
	mock.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
		return aztables.GetEntityResponse{}, &azcore.ResponseError{StatusCode: 404}
	})
	mock.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
		var e map[string]interface{}
		_ = json.Unmarshal(entity, &e)
		assert.Equal(t, "inst-1", e["PartitionKey"])
		assert.Equal(t, "quota", e["RowKey"])
		assert.NotEmpty(t, e["ID"])
		return aztables.AddEntityResponse{}, nil
	})
	repo.SetTestClient(mock)

	podLimit := 5
	override := &models.InstanceQuotaOverride{
		StackInstanceID: "inst-1",
		CPURequest:      "100m",
		CPULimit:        "500m",
		MemoryRequest:   "128Mi",
		MemoryLimit:     "512Mi",
		PodLimit:        &podLimit,
	}
	err := repo.Upsert(context.Background(), override)
	assert.NoError(t, err)
	assert.NotEmpty(t, override.ID)
	assert.False(t, override.CreatedAt.IsZero())
}

func TestInstanceQuotaOverrideRepository_Upsert_Update(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestInstanceQuotaOverrideRepository()
	mock := testhelpers.NewMockTableClient()

	// GetEntity succeeds → triggers update path
	existingData, _ := json.Marshal(map[string]interface{}{
		"PartitionKey":    "inst-1",
		"RowKey":          "quota",
		"ID":              "q-1",
		"StackInstanceID": "inst-1",
		"CreatedAt":       "2024-01-01T00:00:00Z",
		"UpdatedAt":       "2024-01-01T00:00:00Z",
	})
	mock.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
		return aztables.GetEntityResponse{Value: existingData}, nil
	})
	mock.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
		var e map[string]interface{}
		_ = json.Unmarshal(entity, &e)
		assert.Equal(t, "200m", e["CPURequest"])
		return aztables.UpdateEntityResponse{}, nil
	})
	repo.SetTestClient(mock)

	override := &models.InstanceQuotaOverride{
		ID:              "q-1",
		StackInstanceID: "inst-1",
		CPURequest:      "200m",
	}
	err := repo.Upsert(context.Background(), override)
	assert.NoError(t, err)
}

func TestInstanceQuotaOverrideRepository_Upsert_CheckError(t *testing.T) {
	t.Parallel()
	repo := azure.NewTestInstanceQuotaOverrideRepository()
	mock := testhelpers.NewMockTableClient()

	mock.SetGetEntity(func(ctx context.Context, pk, rk string, opts *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
		return aztables.GetEntityResponse{}, errors.New("connection failed")
	})
	repo.SetTestClient(mock)

	override := &models.InstanceQuotaOverride{StackInstanceID: "inst-1"}
	err := repo.Upsert(context.Background(), override)
	assert.Error(t, err)
}

func TestInstanceQuotaOverrideRepository_Delete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		instanceID string
		setupMock  func(*testhelpers.MockTableClient)
		wantErr    bool
	}{
		{
			name:       "successful delete",
			instanceID: "inst-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetDeleteEntity(func(ctx context.Context, pk, rk string, opts *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
					assert.Equal(t, "inst-1", pk)
					assert.Equal(t, "quota", rk)
					return aztables.DeleteEntityResponse{}, nil
				})
			},
		},
		{
			name:       "azure error",
			instanceID: "inst-1",
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
			repo := azure.NewTestInstanceQuotaOverrideRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Delete(context.Background(), tt.instanceID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
