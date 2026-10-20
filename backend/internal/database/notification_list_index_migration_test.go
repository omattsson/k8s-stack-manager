package database

import (
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigration_NotificationListIndex checks migration 63 (#654): the index
// exists after the migrations, and Up and Down can run again.
func TestMigration_NotificationListIndex(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())

	m := db.DB.Migrator()
	assert.True(t, m.HasIndex(&models.Notification{}, notificationListIndexName))

	mig := notificationListIndexMigration()
	assert.Equal(t, "20261021000063", mig.Version)
	require.NoError(t, mig.Up(db.DB), "Up on an existing index")
	require.NoError(t, mig.Down(db.DB))
	assert.False(t, m.HasIndex(&models.Notification{}, notificationListIndexName))
	require.NoError(t, mig.Down(db.DB), "Down without the index")
	require.NoError(t, mig.Up(db.DB))
	assert.True(t, m.HasIndex(&models.Notification{}, notificationListIndexName))
	assert.True(t, m.HasIndex(&models.Notification{}, "idx_notifications_user_id"), "the user_id index stays")
}

func TestMigration_NotificationListIndexWithoutTable(t *testing.T) {
	t.Parallel()
	db := setupMigrationTestDB(t)
	mig := notificationListIndexMigration()
	require.NoError(t, mig.Up(db.DB), "Up without the table")
	require.NoError(t, mig.Down(db.DB), "Down without the table")
	assert.False(t, db.DB.Migrator().HasTable(&models.Notification{}))
}
