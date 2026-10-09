package database

import (
	"context"
	"fmt"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigration_DeployLogUserID checks migration 49: the column and index
// exist after AutoMigrate, older deploy logs get the instance owner, and Up
// and Down can run again without an error.
func TestMigration_DeployLogUserID(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())

	m := db.DB.Migrator()
	assert.True(t, m.HasColumn(&models.DeploymentLog{}, "UserID"))
	assert.True(t, m.HasIndex(&models.DeploymentLog{}, deployLogUserIndexName))

	require.NoError(t, db.DB.Create(&models.StackInstance{ID: "i1", Name: "app", Namespace: "stack-app-a", OwnerID: "owner-1", StackDefinitionID: "d1", Status: models.StackStatusRunning}).Error)
	now := time.Now().UTC()
	for _, l := range []models.DeploymentLog{
		{ID: "old-deploy", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: now},
		{ID: "new-deploy", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: now, UserID: "user-2"},
		{ID: "old-stop", StackInstanceID: "i1", Action: models.DeployActionStop, Status: models.DeployLogSuccess, StartedAt: now},
		{ID: "deleted-instance", StackInstanceID: "gone", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: now},
	} {
		l := l
		require.NoError(t, db.DB.Create(&l).Error)
	}

	mig := deployLogUserIDMigration()
	require.NoError(t, mig.Up(db.DB))

	userOf := func(id string) string {
		var l models.DeploymentLog
		require.NoError(t, db.DB.First(&l, "id = ?", id).Error)
		return l.UserID
	}
	assert.Equal(t, "owner-1", userOf("old-deploy"), "an older deploy gets the instance owner")
	assert.Equal(t, "user-2", userOf("new-deploy"), "a stored user is kept")
	assert.Equal(t, "", userOf("old-stop"), "only deploy logs are filled")
	assert.Equal(t, "", userOf("deleted-instance"), "no owner is known for a deleted instance")

	require.NoError(t, mig.Down(db.DB))
	assert.False(t, m.HasIndex(&models.DeploymentLog{}, deployLogUserIndexName))
	assert.False(t, m.HasColumn(&models.DeploymentLog{}, "UserID"))
	require.NoError(t, mig.Down(db.DB), "Down without the column")
	require.NoError(t, mig.Up(db.DB))
	assert.True(t, m.HasColumn(&models.DeploymentLog{}, "UserID"))
	assert.True(t, m.HasIndex(&models.DeploymentLog{}, deployLogUserIndexName))
	require.NoError(t, mig.Up(db.DB), "Up on an existing column")
}

// TestBackfillDeployLogUsers_Batches checks that the backfill handles more
// rows than one batch, skips instances without an owner and ends.
func TestBackfillDeployLogUsers_Batches(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())

	require.NoError(t, db.DB.Create(&models.StackInstance{ID: "i1", Name: "app", Namespace: "stack-app-a", OwnerID: "owner-1", StackDefinitionID: "d1", Status: models.StackStatusRunning}).Error)
	require.NoError(t, db.DB.Create(&models.StackInstance{ID: "i2", Name: "noowner", Namespace: "stack-noowner", OwnerID: "", StackDefinitionID: "d1", Status: models.StackStatusRunning}).Error)
	now := time.Now().UTC()
	for i := 0; i < 7; i++ {
		l := models.DeploymentLog{ID: fmt.Sprintf("l%d", i), StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: now}
		require.NoError(t, db.DB.Create(&l).Error)
	}
	orphan := models.DeploymentLog{ID: "no-owner", StackInstanceID: "i2", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: now}
	require.NoError(t, db.DB.Create(&orphan).Error)

	require.NoError(t, backfillDeployLogUsers(db.DB, 3))

	var filled int64
	require.NoError(t, db.DB.Model(&models.DeploymentLog{}).Where("user_id = ?", "owner-1").Count(&filled).Error)
	assert.Equal(t, int64(7), filled)
	var l models.DeploymentLog
	require.NoError(t, db.DB.First(&l, "id = ?", "no-owner").Error)
	assert.Empty(t, l.UserID, "an instance without an owner gives no user")

	require.NoError(t, backfillDeployLogUsers(db.DB, 3), "a second run changes nothing")
}

// TestGORMDeploymentLogRepository_SummarizeByUsers checks the per-user deploy
// summary: only deploy logs, grouped by the user who started them, also for
// deleted instances.
func TestGORMDeploymentLogRepository_SummarizeByUsers(t *testing.T) {
	t.Parallel()
	db := setupTestDBWithAllTables(t)
	repo := NewGORMDeploymentLogRepository(db)
	ctx := context.Background()

	early := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	late := early.Add(2 * time.Hour)
	for _, l := range []models.DeploymentLog{
		{ID: "a", StackInstanceID: "gone", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: early, UserID: "u1"},
		{ID: "b", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogError, StartedAt: late, UserID: "u1"},
		{ID: "c", StackInstanceID: "i1", Action: models.DeployActionStop, Status: models.DeployLogSuccess, StartedAt: late, UserID: "u1"},
		{ID: "d", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: early, UserID: "u2"},
		{ID: "e", StackInstanceID: "i1", Action: models.DeployActionDeploy, Status: models.DeployLogSuccess, StartedAt: late},
	} {
		l := l
		require.NoError(t, repo.Create(ctx, &l))
	}

	got, err := repo.SummarizeByUsers(ctx, []string{"u1", "u3"})
	require.NoError(t, err)
	require.Len(t, got, 1, "u2 was not asked for; u3 has no deploys")
	u1 := got["u1"]
	require.NotNil(t, u1)
	assert.Equal(t, "u1", u1.UserID)
	assert.Equal(t, 2, u1.DeployCount)
	assert.Equal(t, 1, u1.SuccessCount)
	assert.Equal(t, 1, u1.ErrorCount)
	require.NotNil(t, u1.LastDeployAt)
	assert.True(t, u1.LastDeployAt.Equal(late), "last deploy %v", u1.LastDeployAt)

	empty, err := repo.SummarizeByUsers(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}
