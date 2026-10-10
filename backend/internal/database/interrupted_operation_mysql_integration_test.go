//go:build integration

package database

import (
	"context"
	"os"
	"testing"
	"time"

	"backend/internal/models"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupMySQLInterruptDB creates an empty MySQL database and runs the full
// migration chain (migrations 54 and 55 included).
func setupMySQLInterruptDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)

	admin, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "Failed to connect to MySQL — is the container running?")

	const dbName = "migration_test_interrupted_ops"
	require.NoError(t, admin.Exec("DROP DATABASE IF EXISTS "+dbName).Error)
	require.NoError(t, admin.Exec("CREATE DATABASE "+dbName).Error)
	t.Cleanup(func() { _ = admin.Exec("DROP DATABASE IF EXISTS " + dbName).Error })

	cfg.DBName = dbName
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	// No migration creates the clusters table (see setupMySQLMigrationDB).
	require.NoError(t, db.AutoMigrate(&models.Cluster{}))
	require.NoError(t, (&Database{DB: db}).AutoMigrate())
	return db
}

// TestDatabaseInterruptedOperations runs the heartbeat upsert (ON DUPLICATE
// KEY UPDATE), the running log query with its index, the optimistic lock on
// stack_instances.updated_at (DATETIME(3) round trip) and the rollback of a
// conflict on MySQL. It also runs migration 54 Down and Up.
func TestDatabaseInterruptedOperations(t *testing.T) {
	db := setupMySQLInterruptDB(t)
	ctx := context.Background()
	m := db.Migrator()

	// Migrations 54 and 55.
	require.True(t, m.HasColumn(&models.DeploymentLog{}, "ReplicaID"))
	require.True(t, m.HasIndex(&models.DeploymentLog{}, deployLogStatusStartedIndexName))
	require.True(t, m.HasTable(&models.ReplicaHeartbeat{}))
	var nullable string
	require.NoError(t, db.Raw(
		"SELECT IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'deployment_logs' AND COLUMN_NAME = 'replica_id'",
	).Scan(&nullable).Error)
	assert.Equal(t, "YES", nullable, "replica_id is nullable")
	mig := deployLogReplicaIDMigration()
	require.NoError(t, mig.Down(db))
	require.False(t, m.HasColumn(&models.DeploymentLog{}, "ReplicaID"))
	require.NoError(t, mig.Down(db))
	require.NoError(t, mig.Up(db))
	require.NoError(t, mig.Up(db))
	require.True(t, m.HasIndex(&models.DeploymentLog{}, deployLogStatusStartedIndexName))

	// Heartbeats: database time (UTC_TIMESTAMP(3)) only.
	hb := NewGORMReplicaHeartbeatRepository(db)
	require.NoError(t, hb.Beat(ctx, "pod-a-11111111"))
	require.NoError(t, db.Exec("UPDATE replica_heartbeats SET last_seen = UTC_TIMESTAMP(3) - INTERVAL 10 MINUTE").Error)
	require.NoError(t, hb.Beat(ctx, "pod-a-11111111"), "upsert of an existing row")
	require.NoError(t, hb.Beat(ctx, "pod-b-22222222"))
	require.NoError(t, db.Exec("UPDATE replica_heartbeats SET last_seen = UTC_TIMESTAMP(3) - INTERVAL 5 MINUTE WHERE id = 'pod-b-22222222'").Error)
	var rows int64
	require.NoError(t, db.Model(&models.ReplicaHeartbeat{}).Count(&rows).Error)
	assert.Equal(t, int64(2), rows)
	var fresh int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM replica_heartbeats WHERE id = 'pod-a-11111111' AND last_seen > UTC_TIMESTAMP(3) - INTERVAL 5 SECOND").Scan(&fresh).Error)
	assert.Equal(t, int64(1), fresh, "Beat writes the database time")
	alive, err := hb.SeenWithin(ctx, []string{"pod-a-11111111", "pod-b-22222222", "pod-c"}, 2*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"pod-a-11111111": true}, alive)
	alive, err = hb.SeenWithin(ctx, []string{"pod-b-22222222"}, 5*time.Minute+30*time.Second)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"pod-b-22222222": true}, alive)
	deleted, err := hb.DeleteOlderThan(ctx, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	now := time.Now().UTC()

	// Running logs and the interrupt.
	instRepo := NewGORMStackInstanceRepository(db)
	ops := NewGORMInterruptedOperationRepository(db)
	marker := now.Add(time.Hour)
	for _, id := range []string{"inst-ok", "inst-conflict"} {
		require.NoError(t, instRepo.Create(&models.StackInstance{
			ID: id, Name: id, Namespace: "stack-" + id, OwnerID: "u1", StackDefinitionID: "d1",
			Status: models.StackStatusStabilizing, PostDeployHookUntil: &marker,
		}))
	}
	started := now.Add(-time.Hour)
	deadline := now.Add(-time.Minute)
	// pod-b-22222222 has no heartbeat row now (deleted above); pod-a has a
	// fresh one.
	for _, l := range []models.DeploymentLog{
		{ID: "log-live", StackInstanceID: "inst-live", Action: models.DeployActionDeploy, Status: models.DeployLogRunning, StartedAt: started.Add(-2 * time.Hour), ReplicaID: "pod-a-11111111", DeadlineAt: &deadline},
		{ID: "log-ok", StackInstanceID: "inst-ok", Action: models.DeployActionDeploy, Status: models.DeployLogRunning, StartedAt: started, ReplicaID: "pod-b-22222222", DeadlineAt: &deadline},
		{ID: "log-conflict", StackInstanceID: "inst-conflict", Action: models.DeployActionDeploy, Status: models.DeployLogRunning, StartedAt: started.Add(time.Second), ReplicaID: "pod-b-22222222", DeadlineAt: &deadline},
		{ID: "log-legacy", StackInstanceID: "inst-ok", Action: models.DeployActionDeploy, Status: models.DeployLogRunning, StartedAt: started.Add(-time.Hour), DeadlineAt: &deadline},
	} {
		l := l
		require.NoError(t, db.Create(&l).Error)
	}
	require.NoError(t, db.Exec("UPDATE deployment_logs SET replica_id = NULL WHERE id = 'log-legacy'").Error)

	running, err := ops.ListInterruptCandidates(ctx, now, "pod-self", 2*time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, running, 2, "no log of a live replica, no log without a replica ID (NULL)")
	assert.Equal(t, "log-ok", running[0].ID)
	one, err := ops.ListInterruptCandidates(ctx, now, "pod-self", 2*time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, one, 1)
	assert.Equal(t, "log-ok", one[0].ID, "the older live log does not fill the batch")

	var explain []map[string]any
	require.NoError(t, db.Raw(
		"EXPLAIN SELECT id FROM deployment_logs WHERE status = 'running' AND deadline_at < ? AND replica_id IS NOT NULL AND replica_id <> ''", now,
	).Scan(&explain).Error)
	require.NotEmpty(t, explain)
	assert.Contains(t, explain[0]["possible_keys"], deployLogStatusStartedIndexName)

	// The updated_at value read from MySQL matches the column (DATETIME(3)).
	inst, err := instRepo.FindByID("inst-ok")
	require.NoError(t, err)
	msg := models.InterruptedOperationMessage(models.DeployActionDeploy)
	res, err := ops.InterruptOperation(ctx, models.InterruptRequest{
		LogID: "log-ok", LogStartedAt: started, InstanceID: inst.ID,
		InstanceStatus: inst.Status, InstanceUpdatedAt: inst.UpdatedAt, Message: msg, Now: now,
		ReplicaID: "pod-b-22222222", StaleAfter: 2 * time.Minute,
	})
	require.NoError(t, err)
	assert.Equal(t, models.InterruptResult{LogClosed: true, InstanceUpdated: true}, res)
	inst, err = instRepo.FindByID("inst-ok")
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusError, inst.Status)
	assert.Equal(t, msg, inst.ErrorMessage)
	assert.Nil(t, inst.PostDeployHookUntil)
	var okLog models.DeploymentLog
	require.NoError(t, db.First(&okLog, "id = ?", "log-ok").Error)
	assert.Equal(t, models.DeployLogError, okLog.Status)
	assert.Equal(t, "pod-b-22222222", okLog.ReplicaID)

	// A second call is a no-op: the log is no longer running.
	res, err = ops.InterruptOperation(ctx, models.InterruptRequest{
		LogID: "log-ok", LogStartedAt: started, InstanceID: inst.ID,
		InstanceStatus: models.StackStatusStabilizing, InstanceUpdatedAt: inst.UpdatedAt, Message: msg, Now: now,
		ReplicaID: "pod-b-22222222", StaleAfter: 2 * time.Minute,
	})
	require.NoError(t, err)
	assert.Equal(t, models.InterruptResult{}, res)

	// Conflict: another process updated the instance after the read. The
	// transaction rolls back, also the log update.
	stale, err := instRepo.FindByID("inst-conflict")
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)
	changed := *stale
	require.NoError(t, instRepo.Update(&changed))
	_, err = ops.InterruptOperation(ctx, models.InterruptRequest{
		LogID: "log-conflict", LogStartedAt: started.Add(time.Second), InstanceID: stale.ID,
		InstanceStatus: stale.Status, InstanceUpdatedAt: stale.UpdatedAt, Message: msg, Now: now,
		ReplicaID: "pod-b-22222222", StaleAfter: 2 * time.Minute,
	})
	require.ErrorIs(t, err, models.ErrInterruptConflict)
	var conflictLog models.DeploymentLog
	require.NoError(t, db.First(&conflictLog, "id = ?", "log-conflict").Error)
	assert.Equal(t, models.DeployLogRunning, conflictLog.Status, "the log update rolled back")
	after, err := instRepo.FindByID("inst-conflict")
	require.NoError(t, err)
	assert.Equal(t, models.StackStatusStabilizing, after.Status)

	// A fresh heartbeat stops the interrupt (checked in the UPDATE).
	res, err = ops.InterruptOperation(ctx, models.InterruptRequest{
		LogID: "log-live", LogStartedAt: started.Add(-2 * time.Hour), InstanceID: "inst-live",
		Message: msg, Now: now, ReplicaID: "pod-a-11111111", StaleAfter: 2 * time.Minute,
	})
	require.NoError(t, err)
	assert.Equal(t, models.InterruptResult{}, res)

	// Conditional Update on MySQL: an unchanged running log (0 changed
	// rows) is fine; a finished log returns ErrDeployLogNotRunning.
	logs := NewGORMDeploymentLogRepository(db)
	live, err := logs.FindByID(ctx, "log-live")
	require.NoError(t, err)
	require.NoError(t, logs.Update(ctx, live), "no change of a running log")
	require.NoError(t, logs.ExtendDeadline(ctx, "log-live", now.Add(time.Hour)))
	require.NoError(t, logs.ExtendDeadline(ctx, "log-live", now), "an earlier deadline of a running log is no error")
	require.ErrorIs(t, logs.ExtendDeadline(ctx, "log-ok", now.Add(time.Hour)), models.ErrDeployLogNotRunning)
	finished, err := logs.FindByID(ctx, "log-ok")
	require.NoError(t, err)
	finished.Status = models.DeployLogSuccess
	finished.ErrorMessage = ""
	require.ErrorIs(t, logs.Update(ctx, finished), models.ErrDeployLogNotRunning)
	require.NoError(t, db.First(&okLog, "id = ?", "log-ok").Error)
	assert.Equal(t, models.DeployLogError, okLog.Status, "the recovered log stays")
	assert.Equal(t, msg, okLog.ErrorMessage)
}
