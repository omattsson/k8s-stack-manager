package azure_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"backend/internal/database/azure"
	"backend/internal/database/azure/testhelpers"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeploymentLogRepository_Create(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		log       *models.DeploymentLog
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful create",
			log: &models.DeploymentLog{
				StackInstanceID: "inst-1",
				Action:          models.DeployActionDeploy,
				Status:          models.DeployLogRunning,
				Output:          "deploying...",
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "inst-1", e["PartitionKey"])
					assert.NotEmpty(t, e["RowKey"])
					assert.Equal(t, "deploy", e["Action"])
					assert.Equal(t, "running", e["Status"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "generates ID when empty",
			log: &models.DeploymentLog{
				StackInstanceID: "inst-1",
				Action:          models.DeployActionStop,
				Status:          models.DeployLogRunning,
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
			name: "sets StartedAt when zero",
			log: &models.DeploymentLog{
				StackInstanceID: "inst-1",
				Action:          models.DeployActionClean,
				Status:          models.DeployLogRunning,
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetAddEntity(func(ctx context.Context, entity []byte, opts *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.NotEmpty(t, e["StartedAt"])
					return aztables.AddEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error propagates",
			log: &models.DeploymentLog{
				StackInstanceID: "inst-1",
				Action:          models.DeployActionDeploy,
				Status:          models.DeployLogRunning,
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
			repo := azure.NewTestDeploymentLogRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Create(context.Background(), tt.log)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tt.log.ID)
				assert.False(t, tt.log.StartedAt.IsZero())
			}
		})
	}
}

func TestDeploymentLogRepository_FindByID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		id        string
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
		errIs     error
		wantID    string
	}{
		{
			name: "found",
			id:   "log-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					data, _ := json.Marshal(map[string]interface{}{
						"PartitionKey":    "inst-1",
						"RowKey":          "00000000000000000001_log-1",
						"ID":              "log-1",
						"StackInstanceID": "inst-1",
						"Action":          "deploy",
						"Status":          "success",
						"Output":          "deployed ok",
						"StartedAt":       "2025-01-15T10:00:00Z",
						"CompletedAt":     "2025-01-15T10:05:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
			},
			wantID: "log-1",
		},
		{
			name: "not found",
			id:   "nonexistent",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantErr: true,
			errIs:   dberrors.ErrNotFound,
		},
		{
			name: "pager error",
			id:   "log-1",
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
			repo := azure.NewTestDeploymentLogRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			log, err := repo.FindByID(context.Background(), tt.id)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errIs != nil {
					assert.ErrorIs(t, err, tt.errIs)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantID, log.ID)
				assert.Equal(t, "deploy", log.Action)
				assert.NotNil(t, log.CompletedAt)
			}
		})
	}
}

func TestDeploymentLogRepository_Update(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		log       *models.DeploymentLog
		setupMock func(*testhelpers.MockTableClient)
		wantErr   bool
	}{
		{
			name: "successful update",
			log: &models.DeploymentLog{
				ID:              "log-1",
				StackInstanceID: "inst-1",
				Action:          models.DeployActionDeploy,
				Status:          models.DeployLogSuccess,
				Output:          "completed",
				StartedAt:       time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
			},
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetUpdateEntity(func(ctx context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
					var e map[string]interface{}
					_ = json.Unmarshal(entity, &e)
					assert.Equal(t, "inst-1", e["PartitionKey"])
					assert.NotEmpty(t, e["RowKey"])
					assert.Equal(t, "success", e["Status"])
					return aztables.UpdateEntityResponse{}, nil
				})
			},
		},
		{
			name: "azure error propagates",
			log: &models.DeploymentLog{
				ID:              "log-1",
				StackInstanceID: "inst-1",
				Action:          models.DeployActionDeploy,
				Status:          models.DeployLogError,
				StartedAt:       time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
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
			repo := azure.NewTestDeploymentLogRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			err := repo.Update(context.Background(), tt.log)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestDeploymentLogRepository_ListByInstance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		instanceID string
		setupMock  func(*testhelpers.MockTableClient)
		wantLen    int
		wantErr    bool
	}{
		{
			name:       "returns logs for instance",
			instanceID: "inst-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					d1, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "inst-1", "RowKey": "rk1",
						"ID": "log-1", "StackInstanceID": "inst-1",
						"Action": "deploy", "Status": "success",
						"StartedAt": "2025-01-15T10:00:00Z",
					})
					d2, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "inst-1", "RowKey": "rk2",
						"ID": "log-2", "StackInstanceID": "inst-1",
						"Action": "stop", "Status": "success",
						"StartedAt": "2025-01-16T10:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{d1, d2}, nil)
				})
			},
			wantLen: 2,
		},
		{
			name:       "empty result",
			instanceID: "inst-empty",
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
			repo := azure.NewTestDeploymentLogRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			logs, err := repo.ListByInstance(context.Background(), tt.instanceID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, logs, tt.wantLen)
			}
		})
	}
}

func TestDeploymentLogRepository_GetLatestByInstance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		instanceID string
		setupMock  func(*testhelpers.MockTableClient)
		wantErr    bool
		errIs      error
		wantID     string
	}{
		{
			name:       "returns latest log",
			instanceID: "inst-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					data, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "inst-1", "RowKey": "rk1",
						"ID": "log-latest", "StackInstanceID": "inst-1",
						"Action": "deploy", "Status": "success",
						"StartedAt": "2025-01-20T10:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{data}, nil)
				})
			},
			wantID: "log-latest",
		},
		{
			name:       "not found when no logs",
			instanceID: "inst-empty",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantErr: true,
			errIs:   dberrors.ErrNotFound,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := azure.NewTestDeploymentLogRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			log, err := repo.GetLatestByInstance(context.Background(), tt.instanceID)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errIs != nil {
					assert.ErrorIs(t, err, tt.errIs)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantID, log.ID)
			}
		})
	}
}

func TestDeploymentLogRepository_SummarizeByInstance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		instanceID    string
		setupMock     func(*testhelpers.MockTableClient)
		wantDeploy    int
		wantSuccess   int
		wantError     int
		wantErr       bool
		hasLastDeploy bool
	}{
		{
			name:       "summarizes deploy counts",
			instanceID: "inst-1",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					d1, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "inst-1", "RowKey": "rk1",
						"Action": "deploy", "Status": "success",
						"StartedAt": "2025-01-15T10:00:00Z", "CompletedAt": "2025-01-15T10:05:00Z",
					})
					d2, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "inst-1", "RowKey": "rk2",
						"Action": "deploy", "Status": "error",
						"StartedAt": "2025-01-14T10:00:00Z", "CompletedAt": "2025-01-14T10:05:00Z",
					})
					d3, _ := json.Marshal(map[string]interface{}{
						"PartitionKey": "inst-1", "RowKey": "rk3",
						"Action": "stop", "Status": "success",
						"StartedAt": "2025-01-16T10:00:00Z",
					})
					return testhelpers.NewMockTablePager([][]byte{d1, d2, d3}, nil)
				})
			},
			wantDeploy:    2,
			wantSuccess:   1,
			wantError:     1,
			hasLastDeploy: true,
		},
		{
			name:       "empty logs",
			instanceID: "inst-empty",
			setupMock: func(m *testhelpers.MockTableClient) {
				m.SetNewListEntitiesPager(func(opts *aztables.ListEntitiesOptions) azure.ListEntitiesPager {
					return testhelpers.NewMockTablePager(nil, nil)
				})
			},
			wantDeploy:    0,
			wantSuccess:   0,
			wantError:     0,
			hasLastDeploy: false,
		},
		{
			name:       "pager error",
			instanceID: "inst-1",
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
			repo := azure.NewTestDeploymentLogRepository()
			mock := testhelpers.NewMockTableClient()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			repo.SetTestClient(mock)
			summary, err := repo.SummarizeByInstance(context.Background(), tt.instanceID)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.instanceID, summary.InstanceID)
				assert.Equal(t, tt.wantDeploy, summary.DeployCount)
				assert.Equal(t, tt.wantSuccess, summary.SuccessCount)
				assert.Equal(t, tt.wantError, summary.ErrorCount)
				if tt.hasLastDeploy {
					assert.NotNil(t, summary.LastDeployAt)
				} else {
					assert.Nil(t, summary.LastDeployAt)
				}
			}
		})
	}
}
