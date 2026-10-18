package database

import (
	"context"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestMigration_DeployLogReplicaIDAndHeartbeats checks migrations 54 and 55
// after the full chain, and that Up and Down are idempotent.
func TestMigration_DeployLogReplicaIDAndHeartbeats(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())
	m := db.DB.Migrator()

	assert.True(t, m.HasColumn(&models.DeploymentLog{}, "ReplicaID"))
	assert.True(t, m.HasColumn(&models.DeploymentLog{}, "DeadlineAt"))
	assert.True(t, m.HasIndex(&models.DeploymentLog{}, deployLogStatusStartedIndexName))
	assert.True(t, m.HasTable(&models.ReplicaHeartbeat{}))
	assert.True(t, m.HasIndex(&models.ReplicaHeartbeat{}, "idx_replica_heartbeats_last_seen"))

	logMig := deployLogReplicaIDMigration()
	require.NoError(t, logMig.Down(db.DB))
	assert.False(t, m.HasColumn(&models.DeploymentLog{}, "ReplicaID"))
	assert.False(t, m.HasColumn(&models.DeploymentLog{}, "DeadlineAt"))
	assert.False(t, m.HasIndex(&models.DeploymentLog{}, deployLogStatusStartedIndexName))
	require.NoError(t, logMig.Down(db.DB), "Down without the column")
	require.NoError(t, logMig.Up(db.DB))
	assert.True(t, m.HasColumn(&models.DeploymentLog{}, "ReplicaID"))
	assert.True(t, m.HasIndex(&models.DeploymentLog{}, deployLogStatusStartedIndexName))
	require.NoError(t, logMig.Up(db.DB), "Up on an existing column")

	hbMig := replicaHeartbeatsMigration()
	require.NoError(t, hbMig.Down(db.DB))
	assert.False(t, m.HasTable(&models.ReplicaHeartbeat{}))
	require.NoError(t, hbMig.Down(db.DB), "Down without the table")
	require.NoError(t, hbMig.Up(db.DB))
	require.NoError(t, hbMig.Up(db.DB), "Up on an existing table")
	assert.True(t, m.HasTable(&models.ReplicaHeartbeat{}))
}

func setupHeartbeatDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupTestDBWithAllTables(t)
	require.NoError(t, replicaHeartbeatsMigration().Up(db))
	require.NoError(t, deployLogReplicaIDMigration().Up(db))
	return db
}

func TestGORMReplicaHeartbeatRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupHeartbeatDB(t)
	repo := NewGORMReplicaHeartbeatRepository(db)

	// backdate moves last_seen of id back by age, in database time.
	backdate := func(id string, age time.Duration) {
		t.Helper()
		require.NoError(t, db.Exec("UPDATE replica_heartbeats SET last_seen = "+dbNowMinus(db, age)+" WHERE id = ?", id).Error)
	}

	require.NoError(t, repo.Beat(ctx, "pod-a-1"))
	backdate("pod-a-1", 10*time.Minute)
	require.NoError(t, repo.Beat(ctx, "pod-a-1"), "a second beat updates the row to the database time")
	require.NoError(t, repo.Beat(ctx, "pod-b-1"))
	backdate("pod-b-1", 3*time.Minute)
	var rows int64
	require.NoError(t, db.Model(&models.ReplicaHeartbeat{}).Count(&rows).Error)
	assert.Equal(t, int64(2), rows)

	alive, err := repo.SeenWithin(ctx, []string{"pod-a-1", "pod-b-1", "pod-c-1"}, 2*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"pod-a-1": true}, alive)
	alive, err = repo.SeenWithin(ctx, []string{"pod-b-1"}, 3*time.Minute+30*time.Second)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"pod-b-1": true}, alive, "millisecond-precise limit")

	empty, err := repo.SeenWithin(ctx, nil, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, empty)

	deleted, err := repo.DeleteOlderThan(ctx, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted, "only pod-b-1 is old")

	require.NoError(t, repo.Remove(ctx, "pod-a-1"))
	require.NoError(t, repo.Remove(ctx, "pod-a-1"), "removing a missing row is not an error")
	alive, err = repo.SeenWithin(ctx, []string{"pod-a-1"}, time.Hour)
	require.NoError(t, err)
	assert.Empty(t, alive)
}

func TestDBNowMinus(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	assert.Equal(t, "strftime('%Y-%m-%d %H:%M:%f', 'now', '-150.250 seconds')", dbNowMinus(db, 150250*time.Millisecond))
}

func TestGORMInterruptedOperationRepository_ListInterruptCandidates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupHeartbeatDB(t)
	repo := NewGORMInterruptedOperationRepository(db)
	hb := NewGORMReplicaHeartbeatRepository(db)
	now := time.Now().UTC()
	past := now.Add(-10 * time.Minute)
	future := now.Add(10 * time.Minute)

	// pod-live has a fresh heartbeat, pod-stale an old one, pod-gone none.
	require.NoError(t, hb.Beat(ctx, "pod-live"))
	require.NoError(t, hb.Beat(ctx, "pod-stale"))
	require.NoError(t, db.Exec("UPDATE replica_heartbeats SET last_seen = "+dbNowMinus(db, 5*time.Minute)+" WHERE id = 'pod-stale'").Error)

	logs := []models.DeploymentLog{
		// Live logs started first: they must not fill a batch of 1.
		{ID: "live", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogRunning, StartedAt: now.Add(-4 * time.Hour), ReplicaID: "pod-live", DeadlineAt: &past},
		{ID: "own", StackInstanceID: "i2", Action: models.DeployActionDeploy, Status: models.DeployLogRunning, StartedAt: now.Add(-3 * time.Hour), ReplicaID: "pod-self", DeadlineAt: &past},
		{ID: "stale", StackInstanceID: "i3", Action: models.DeployActionStop, Status: models.DeployLogRunning, StartedAt: now.Add(-2 * time.Hour), ReplicaID: "pod-stale", DeadlineAt: &past, Output: "large"},
		{ID: "gone", StackInstanceID: "i4", Action: models.DeployActionDeploy, Status: models.DeployLogRunning, StartedAt: now.Add(-time.Hour), ReplicaID: "pod-gone", DeadlineAt: &past},
		{ID: "before-deadline", StackInstanceID: "i5", Action: models.DeployActionDeploy, Status: models.DeployLogRunning, StartedAt: now.Add(-5 * time.Hour), ReplicaID: "pod-gone", DeadlineAt: &future},
		{ID: "no-deadline", StackInstanceID: "i6", Action: models.DeployActionDeploy, Status: models.DeployLogRunning, StartedAt: now.Add(-5 * time.Hour), ReplicaID: "pod-gone"},
		{ID: "no-replica", StackInstanceID: "i7", Action: models.DeployActionDeploy, Status: models.DeployLogRunning, StartedAt: now.Add(-5 * time.Hour), DeadlineAt: &past},
		{ID: "done", StackInstanceID: "i8", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: now.Add(-5 * time.Hour), ReplicaID: "pod-gone", DeadlineAt: &past},
	}
	for i := range logs {
		require.NoError(t, db.Create(&logs[i]).Error)
	}

	got, err := repo.ListInterruptCandidates(ctx, now, "pod-self", 2*time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "stale", got[0].ID, "oldest first")
	assert.Equal(t, "gone", got[1].ID)
	assert.Equal(t, "pod-stale", got[0].ReplicaID)
	require.NotNil(t, got[0].DeadlineAt)
	assert.Empty(t, got[0].Output, "only the small columns")

	limited, err := repo.ListInterruptCandidates(ctx, now, "pod-self", 2*time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	assert.Equal(t, "stale", limited[0].ID, "live and own logs do not fill the batch")
}

func TestGORMDeploymentLogRepository_ConditionalUpdateAndDeadline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupHeartbeatDB(t)
	repo := NewGORMDeploymentLogRepository(db)
	now := time.Now().UTC().Truncate(time.Millisecond)
	deadline := now.Add(time.Minute)

	l := &models.DeploymentLog{ID: "log-1", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogRunning, StartedAt: now, ReplicaID: "pod-a", DeadlineAt: &deadline}
	require.NoError(t, repo.Create(ctx, l))

	// A deadline extension only moves the deadline later.
	later := now.Add(time.Hour)
	require.NoError(t, repo.ExtendDeadline(ctx, l.ID, later))
	require.NoError(t, repo.ExtendDeadline(ctx, l.ID, now.Add(time.Minute)), "an earlier deadline is ignored")
	got, err := repo.FindByID(ctx, l.ID)
	require.NoError(t, err)
	require.NotNil(t, got.DeadlineAt)
	assert.WithinDuration(t, later, *got.DeadlineAt, time.Millisecond)

	// An update of a running log without a change succeeds.
	require.NoError(t, repo.Update(ctx, got))

	// The final write of a running log succeeds and writes zero values too.
	got.Status = models.DeployLogSuccess
	got.Output = "done"
	got.ErrorMessage = ""
	require.NoError(t, repo.Update(ctx, got))
	stored, err := repo.FindByID(ctx, l.ID)
	require.NoError(t, err)
	assert.Equal(t, models.DeployLogSuccess, stored.Status)
	assert.Equal(t, "done", stored.Output)
	assert.Equal(t, "pod-a", stored.ReplicaID)

	// A late writer (the log is no longer running) changes nothing.
	late := *stored
	late.Status = models.DeployLogError
	late.Output = "late"
	require.ErrorIs(t, repo.Update(ctx, &late), models.ErrDeployLogNotRunning)
	stored, err = repo.FindByID(ctx, l.ID)
	require.NoError(t, err)
	assert.Equal(t, models.DeployLogSuccess, stored.Status)
	assert.Equal(t, "done", stored.Output)

	// A missing log is not created (Save would insert it).
	require.ErrorIs(t, repo.Update(ctx, &models.DeploymentLog{ID: "missing", Status: models.DeployLogSuccess}), models.ErrDeployLogNotRunning)
	_, err = repo.FindByID(ctx, "missing")
	require.Error(t, err)

	// ExtendDeadline of a finished or missing log changes nothing and
	// reports it.
	require.ErrorIs(t, repo.ExtendDeadline(ctx, l.ID, now.Add(48*time.Hour)), models.ErrDeployLogNotRunning)
	require.ErrorIs(t, repo.ExtendDeadline(ctx, "missing", now), models.ErrDeployLogNotRunning)
	stored, err = repo.FindByID(ctx, l.ID)
	require.NoError(t, err)
	assert.WithinDuration(t, later, *stored.DeadlineAt, time.Millisecond)
}

func TestGORMInterruptedOperationRepository_InterruptOperation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// logStatus is the status of the log of the operation.
		logStatus string
		// instanceStatus is the stored instance status.
		instanceStatus string
		// reqStatus is the status in the request; "" leaves the instance.
		reqStatus string
		// staleUpdatedAt makes the request carry another updated_at.
		staleUpdatedAt bool
		newerLog       bool
		freshHeartbeat bool
		wantErr        error
		wantResult     models.InterruptResult
		wantLog        string
		wantInstance   string
	}{
		{
			name: "running log and deploying instance are set to error", logStatus: models.DeployLogRunning,
			instanceStatus: models.StackStatusDeploying, reqStatus: models.StackStatusDeploying,
			wantResult: models.InterruptResult{LogClosed: true, InstanceUpdated: true},
			wantLog:    models.DeployLogError, wantInstance: models.StackStatusError,
		},
		{
			name: "stabilizing instance loses the hook marker", logStatus: models.DeployLogRunning,
			instanceStatus: models.StackStatusStabilizing, reqStatus: models.StackStatusStabilizing,
			wantResult: models.InterruptResult{LogClosed: true, InstanceUpdated: true},
			wantLog:    models.DeployLogError, wantInstance: models.StackStatusError,
		},
		{
			name: "finished log changes nothing", logStatus: models.DeployLogSuccess,
			instanceStatus: models.StackStatusDeploying, reqStatus: models.StackStatusDeploying,
			wantLog: models.DeployLogSuccess, wantInstance: models.StackStatusDeploying,
		},
		{
			name: "status changed: conflict, nothing changes", logStatus: models.DeployLogRunning,
			instanceStatus: models.StackStatusStopping, reqStatus: models.StackStatusDeploying,
			wantErr: models.ErrInterruptConflict,
			wantLog: models.DeployLogRunning, wantInstance: models.StackStatusStopping,
		},
		{
			name: "updated_at changed: conflict, nothing changes", logStatus: models.DeployLogRunning,
			instanceStatus: models.StackStatusDeploying, reqStatus: models.StackStatusDeploying, staleUpdatedAt: true,
			wantErr: models.ErrInterruptConflict,
			wantLog: models.DeployLogRunning, wantInstance: models.StackStatusDeploying,
		},
		{
			name: "newer log owns the instance: only the log is closed", logStatus: models.DeployLogRunning,
			instanceStatus: models.StackStatusStopping, reqStatus: models.StackStatusStopping, newerLog: true,
			wantResult: models.InterruptResult{LogClosed: true},
			wantLog:    models.DeployLogError, wantInstance: models.StackStatusStopping,
		},
		{
			name: "fresh heartbeat of the replica: nothing changes", logStatus: models.DeployLogRunning,
			instanceStatus: models.StackStatusDeploying, reqStatus: models.StackStatusDeploying, freshHeartbeat: true,
			wantLog: models.DeployLogRunning, wantInstance: models.StackStatusDeploying,
		},
		{
			name: "no instance status: only the log is closed", logStatus: models.DeployLogRunning,
			instanceStatus: models.StackStatusRunning,
			wantResult:     models.InterruptResult{LogClosed: true},
			wantLog:        models.DeployLogError, wantInstance: models.StackStatusRunning,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			db := setupHeartbeatDB(t)
			repo := NewGORMInterruptedOperationRepository(db)
			instRepo := NewGORMStackInstanceRepository(db)
			now := time.Now().UTC()
			started := now.Add(-time.Hour)

			marker := now.Add(time.Hour)
			inst := &models.StackInstance{
				ID: "inst-1", Name: "demo", Namespace: "stack-demo-alice", OwnerID: "u1",
				StackDefinitionID: "d1", Status: tt.instanceStatus, PostDeployHookUntil: &marker,
			}
			require.NoError(t, instRepo.Create(inst))
			// A clean of a delete was running: the recovery clears the mark.
			require.NoError(t, db.Model(&models.StackInstance{}).Where("id = ?", inst.ID).
				UpdateColumn("delete_after_clean", true).Error)
			stored, err := instRepo.FindByID(inst.ID)
			require.NoError(t, err)

			require.NoError(t, db.Create(&models.DeploymentLog{
				ID: "log-1", StackInstanceID: inst.ID, Action: models.DeployActionDeploy,
				Status: tt.logStatus, StartedAt: started, ReplicaID: "pod-a-1",
			}).Error)
			if tt.newerLog {
				require.NoError(t, db.Create(&models.DeploymentLog{
					ID: "log-2", StackInstanceID: inst.ID, Action: models.DeployActionStop,
					Status: models.DeployLogRunning, StartedAt: started.Add(time.Minute), ReplicaID: "pod-b-1",
				}).Error)
			}

			updatedAt := stored.UpdatedAt
			if tt.staleUpdatedAt {
				updatedAt = updatedAt.Add(-time.Second)
			}
			msg := models.InterruptedOperationMessage(models.DeployActionDeploy)
			if tt.freshHeartbeat {
				require.NoError(t, NewGORMReplicaHeartbeatRepository(db).Beat(ctx, "pod-a-1"))
			}
			res, err := repo.InterruptOperation(ctx, models.InterruptRequest{
				LogID: "log-1", LogStartedAt: started, InstanceID: inst.ID,
				InstanceStatus: tt.reqStatus, InstanceUpdatedAt: updatedAt,
				ReplicaID: "pod-a-1", StaleAfter: 2 * time.Minute,
				Message: msg, Now: now,
			})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantResult, res)

			var gotLog models.DeploymentLog
			require.NoError(t, db.First(&gotLog, "id = ?", "log-1").Error)
			assert.Equal(t, tt.wantLog, gotLog.Status)
			if tt.wantResult.LogClosed {
				assert.Equal(t, msg, gotLog.ErrorMessage)
				require.NotNil(t, gotLog.CompletedAt)
			}

			after, err := instRepo.FindByID(inst.ID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantInstance, after.Status)
			if tt.wantResult.InstanceUpdated {
				assert.Equal(t, msg, after.ErrorMessage)
				assert.Nil(t, after.PostDeployHookUntil, "the hook marker is cleared")
				assert.False(t, after.DeleteAfterClean, "an interrupted delete does not delete later")
			} else {
				assert.NotNil(t, after.PostDeployHookUntil, "an unchanged instance keeps its marker")
				assert.True(t, after.DeleteAfterClean)
			}
		})
	}
}
