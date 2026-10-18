package database

import (
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanedAtMigration(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	m := db.Migrator()
	mig := cleanedAtMigration()
	assert.Equal(t, "20261014000059", mig.Version)

	require.NoError(t, mig.Down(db))
	assert.False(t, m.HasColumn(&models.StackInstance{}, "CleanedAt"))
	require.NoError(t, mig.Down(db), "Down must be idempotent")

	// Rows from before the migration: a cleaned draft, a never-deployed
	// draft and a running instance with an old clean log.
	cleanedTime := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	older := cleanedTime.Add(-48 * time.Hour)
	for _, inst := range []map[string]interface{}{
		{"id": "cleaned", "name": "cleaned", "status": models.StackStatusDraft},
		{"id": "never", "name": "never", "status": models.StackStatusDraft},
		{"id": "running", "name": "running", "status": models.StackStatusRunning},
	} {
		require.NoError(t, db.Table("stack_instances").Create(inst).Error)
	}
	for _, l := range []map[string]interface{}{
		{"id": "l1", "stack_instance_id": "cleaned", "action": models.DeployActionClean, "status": models.DeployLogSuccess, "completed_at": older},
		{"id": "l2", "stack_instance_id": "cleaned", "action": models.DeployActionClean, "status": models.DeployLogSuccess, "completed_at": cleanedTime},
		{"id": "l3", "stack_instance_id": "cleaned", "action": models.DeployActionClean, "status": models.DeployLogError, "completed_at": cleanedTime.Add(time.Hour)},
		{"id": "l4", "stack_instance_id": "running", "action": models.DeployActionClean, "status": models.DeployLogSuccess, "completed_at": older},
	} {
		require.NoError(t, db.Table("deployment_logs").Create(l).Error)
	}

	require.NoError(t, mig.Up(db))
	require.NoError(t, mig.Up(db), "Up must be idempotent")
	assert.True(t, m.HasColumn(&models.StackInstance{}, "CleanedAt"))

	var got []models.StackInstance
	require.NoError(t, db.Order("id").Find(&got).Error)
	byID := map[string]models.StackInstance{}
	for _, inst := range got {
		byID[inst.ID] = inst
	}
	require.NotNil(t, byID["cleaned"].CleanedAt, "a cleaned draft gets the time of its last successful clean")
	assert.True(t, byID["cleaned"].CleanedAt.Equal(cleanedTime), "got %v", byID["cleaned"].CleanedAt)
	assert.Nil(t, byID["never"].CleanedAt, "a draft without a clean log keeps NULL")
	assert.Nil(t, byID["running"].CleanedAt, "only drafts are backfilled")
}
