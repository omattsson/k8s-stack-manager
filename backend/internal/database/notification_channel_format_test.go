package database

import (
	"context"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNotificationChannelFormatMigration checks migration 60: Up adds the
// column, existing rows get "generic", and Up and Down are idempotent.
func TestNotificationChannelFormatMigration(t *testing.T) {
	t.Parallel()
	mig := notificationChannelFormatMigration()
	assert.Equal(t, "20261014000060", mig.Version)

	t.Run("full migration chain", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, db.AutoMigrate())
		m := db.DB.Migrator()
		assert.True(t, m.HasColumn(&models.NotificationChannel{}, "Format"))
		require.NoError(t, mig.Up(db.DB), "Up on an existing column")
	})

	t.Run("down and up on a GORM table backfill generic", func(t *testing.T) {
		t.Parallel()
		// The SQLite migrator of GORM cannot drop a column of the
		// hand-written CREATE TABLE of migration 40. Check Down and Up on a
		// table that GORM created (MySQL drops the column with ALTER TABLE).
		db := setupMigrationTestDB(t)
		require.NoError(t, db.DB.AutoMigrate(&models.NotificationChannel{}))
		m := db.DB.Migrator()

		require.NoError(t, mig.Down(db.DB))
		assert.False(t, m.HasColumn(&models.NotificationChannel{}, "Format"))
		require.NoError(t, mig.Down(db.DB), "Down must be idempotent")

		// A row from before the migration.
		require.NoError(t, db.DB.Table("notification_channels").Create(map[string]interface{}{
			"id": "old", "name": "old", "webhook_url": "https://example.com/old", "enabled": true,
		}).Error)

		require.NoError(t, mig.Up(db.DB))
		require.NoError(t, mig.Up(db.DB), "Up must be idempotent")
		assert.True(t, m.HasColumn(&models.NotificationChannel{}, "Format"))

		var format string
		require.NoError(t, db.DB.Table("notification_channels").Select("format").Where("id = ?", "old").Scan(&format).Error)
		assert.Equal(t, models.NotificationChannelFormatGeneric, format)
	})

	t.Run("no table", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, mig.Up(db.DB))
		require.NoError(t, mig.Down(db.DB))
	})
}

// TestGORMNotificationChannelRepository_Format checks that create, update,
// get and the dispatcher lookup store and read the channel format.
func TestGORMNotificationChannelRepository_Format(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := setupMigrationTestDB(t)
	require.NoError(t, db.AutoMigrate())
	repo := NewGORMNotificationChannelRepository(db.DB, "")

	teams := &models.NotificationChannel{ID: "t", Name: "teams", WebhookURL: "https://example.com/t", Enabled: true, Format: models.NotificationChannelFormatTeams}
	plain := &models.NotificationChannel{ID: "p", Name: "plain", WebhookURL: "https://example.com/p", Enabled: true}
	require.NoError(t, repo.CreateChannel(ctx, teams))
	require.NoError(t, repo.CreateChannel(ctx, plain))
	assert.Equal(t, models.NotificationChannelFormatGeneric, plain.Format, "create sets generic for an empty format")
	require.NoError(t, repo.SetSubscriptions(ctx, "t", []string{"deployment.success"}))

	got, err := repo.GetChannel(ctx, "t")
	require.NoError(t, err)
	assert.Equal(t, models.NotificationChannelFormatTeams, got.Format)
	got, err = repo.GetChannel(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, models.NotificationChannelFormatGeneric, got.Format)

	byEvent, err := repo.FindChannelsByEvent(ctx, "deployment.success")
	require.NoError(t, err)
	require.Len(t, byEvent, 1)
	assert.Equal(t, models.NotificationChannelFormatTeams, byEvent[0].Format, "the dispatcher reads the format")

	teams.Format = models.NotificationChannelFormatGeneric
	require.NoError(t, repo.UpdateChannel(ctx, teams, false))
	got, err = repo.GetChannel(ctx, "t")
	require.NoError(t, err)
	assert.Equal(t, models.NotificationChannelFormatGeneric, got.Format)

	plain.Format = models.NotificationChannelFormatTeams
	require.NoError(t, repo.UpdateChannel(ctx, plain, false))
	plain.Format = ""
	require.NoError(t, repo.UpdateChannel(ctx, plain, false))
	got, err = repo.GetChannel(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, models.NotificationChannelFormatGeneric, got.Format, "update stores generic for an empty format")
}
