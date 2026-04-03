package database

import (
	"context"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupDeploymentLogRepo(t *testing.T) *GORMDeploymentLogRepository {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	return NewGORMDeploymentLogRepository(db)
}

func TestGORMDeploymentLogRepository_CRUD(t *testing.T) {
	t.Parallel()

	repo := setupDeploymentLogRepo(t)
	ctx := context.Background()

	// Create
	log := &models.DeploymentLog{
		StackInstanceID: "si-1",
		Action:          models.DeployActionDeploy,
		Status:          models.DeployLogRunning,
		Output:          "deploying...",
	}
	err := repo.Create(ctx, log)
	require.NoError(t, err)
	assert.NotEmpty(t, log.ID)
	assert.False(t, log.StartedAt.IsZero())

	// FindByID
	found, err := repo.FindByID(ctx, log.ID)
	require.NoError(t, err)
	assert.Equal(t, "deploying...", found.Output)

	// Update
	now := time.Now().UTC()
	found.Status = models.DeployLogSuccess
	found.CompletedAt = &now
	err = repo.Update(ctx, found)
	require.NoError(t, err)

	updated, err := repo.FindByID(ctx, log.ID)
	require.NoError(t, err)
	assert.Equal(t, models.DeployLogSuccess, updated.Status)
	assert.NotNil(t, updated.CompletedAt)
}

func TestGORMDeploymentLogRepository_FindByID_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupDeploymentLogRepo(t)
	_, err := repo.FindByID(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestGORMDeploymentLogRepository_ListByInstance(t *testing.T) {
	t.Parallel()

	repo := setupDeploymentLogRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &models.DeploymentLog{StackInstanceID: "si-a", Action: "deploy", Status: "success"}))
	require.NoError(t, repo.Create(ctx, &models.DeploymentLog{StackInstanceID: "si-a", Action: "stop", Status: "success"}))
	require.NoError(t, repo.Create(ctx, &models.DeploymentLog{StackInstanceID: "si-b", Action: "deploy", Status: "success"}))

	logs, err := repo.ListByInstance(ctx, "si-a")
	require.NoError(t, err)
	assert.Len(t, logs, 2)
}

func TestGORMDeploymentLogRepository_GetLatestByInstance(t *testing.T) {
	t.Parallel()

	repo := setupDeploymentLogRepo(t)
	ctx := context.Background()

	// Create older then newer.
	older := &models.DeploymentLog{StackInstanceID: "si-latest", Action: "deploy", Status: "success", StartedAt: time.Now().Add(-1 * time.Hour)}
	newer := &models.DeploymentLog{StackInstanceID: "si-latest", Action: "stop", Status: "success", StartedAt: time.Now()}
	require.NoError(t, repo.Create(ctx, older))
	require.NoError(t, repo.Create(ctx, newer))

	latest, err := repo.GetLatestByInstance(ctx, "si-latest")
	require.NoError(t, err)
	assert.Equal(t, "stop", latest.Action)
}

func TestGORMDeploymentLogRepository_GetLatestByInstance_NotFound(t *testing.T) {
	t.Parallel()

	repo := setupDeploymentLogRepo(t)
	_, err := repo.GetLatestByInstance(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestGORMDeploymentLogRepository_SummarizeByInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(t *testing.T, repo *GORMDeploymentLogRepository)
		instanceID  string
		wantDeploy  int
		wantSuccess int
		wantError   int
		wantLastNil bool
	}{
		{
			name: "mix of success and failed deploys",
			setup: func(t *testing.T, repo *GORMDeploymentLogRepository) {
				t.Helper()
				ctx := context.Background()
				now := time.Now().UTC()
				completed := now.Add(-1 * time.Minute)
				require.NoError(t, repo.Create(ctx, &models.DeploymentLog{
					StackInstanceID: "si-sum-1",
					Action:          models.DeployActionDeploy,
					Status:          models.DeployLogSuccess,
					StartedAt:       now.Add(-2 * time.Hour),
					CompletedAt:     &completed,
				}))
				require.NoError(t, repo.Create(ctx, &models.DeploymentLog{
					StackInstanceID: "si-sum-1",
					Action:          models.DeployActionDeploy,
					Status:          models.DeployLogError,
					StartedAt:       now.Add(-1 * time.Hour),
					CompletedAt:     &completed,
				}))
				require.NoError(t, repo.Create(ctx, &models.DeploymentLog{
					StackInstanceID: "si-sum-1",
					Action:          models.DeployActionStop,
					Status:          models.DeployLogSuccess,
					StartedAt:       now,
					CompletedAt:     &completed,
				}))
			},
			instanceID:  "si-sum-1",
			wantDeploy:  2,
			wantSuccess: 1,
			wantError:   1,
			wantLastNil: false,
		},
		{
			name:        "empty case - no deployment logs",
			setup:       func(_ *testing.T, _ *GORMDeploymentLogRepository) {},
			instanceID:  "si-sum-empty",
			wantDeploy:  0,
			wantSuccess: 0,
			wantError:   0,
			wantLastNil: true,
		},
		{
			name: "only failed deploys",
			setup: func(t *testing.T, repo *GORMDeploymentLogRepository) {
				t.Helper()
				ctx := context.Background()
				now := time.Now().UTC()
				completed := now.Add(-1 * time.Minute)
				require.NoError(t, repo.Create(ctx, &models.DeploymentLog{
					StackInstanceID: "si-sum-fail",
					Action:          models.DeployActionDeploy,
					Status:          models.DeployLogError,
					StartedAt:       now.Add(-1 * time.Hour),
					CompletedAt:     &completed,
				}))
				require.NoError(t, repo.Create(ctx, &models.DeploymentLog{
					StackInstanceID: "si-sum-fail",
					Action:          models.DeployActionDeploy,
					Status:          models.DeployLogError,
					StartedAt:       now,
					CompletedAt:     &completed,
				}))
			},
			instanceID:  "si-sum-fail",
			wantDeploy:  2,
			wantSuccess: 0,
			wantError:   2,
			wantLastNil: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := setupDeploymentLogRepo(t)
			tt.setup(t, repo)

			summary, err := repo.SummarizeByInstance(context.Background(), tt.instanceID)
			require.NoError(t, err)
			assert.Equal(t, tt.instanceID, summary.InstanceID)
			assert.Equal(t, tt.wantDeploy, summary.DeployCount)
			assert.Equal(t, tt.wantSuccess, summary.SuccessCount)
			assert.Equal(t, tt.wantError, summary.ErrorCount)
			if tt.wantLastNil {
				assert.Nil(t, summary.LastDeployAt)
			} else {
				assert.NotNil(t, summary.LastDeployAt)
			}
		})
	}
}
