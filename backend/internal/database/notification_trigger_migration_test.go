package database

import (
	"context"
	"testing"
	"time"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNotificationTriggerMigration checks migration 64 (#652): Up adds the
// columns, old rows keep NULL, and Up and Down are idempotent.
func TestNotificationTriggerMigration(t *testing.T) {
	t.Parallel()
	mig := notificationTriggerMigration()
	assert.Equal(t, "20261021000064", mig.Version)

	t.Run("full migration chain", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, db.AutoMigrate())
		m := db.DB.Migrator()
		for _, col := range notificationTriggerColumns {
			assert.True(t, m.HasColumn(&models.Notification{}, col), col)
		}
		require.NoError(t, mig.Up(db.DB), "Up on existing columns")
	})

	t.Run("down, up and up keep an old row", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, db.DB.AutoMigrate(&models.Notification{}))
		m := db.DB.Migrator()

		require.NoError(t, mig.Down(db.DB))
		for _, col := range notificationTriggerColumns {
			assert.False(t, m.HasColumn(&models.Notification{}, col), col)
		}
		require.NoError(t, mig.Down(db.DB), "Down must be idempotent")

		// A row from before the migration.
		require.NoError(t, db.DB.Table("notifications").Create(map[string]interface{}{
			"id": "old", "user_id": "u1", "type": "deployment.stopped", "title": "Stack stopped",
			"created_at": time.Now().UTC(), "is_read": false,
		}).Error)

		require.NoError(t, mig.Up(db.DB))
		require.NoError(t, mig.Up(db.DB), "Up must be idempotent")
		for _, col := range notificationTriggerColumns {
			assert.True(t, m.HasColumn(&models.Notification{}, col), col)
		}

		var nulls int64
		require.NoError(t, db.DB.Table("notifications").
			Where("id = ? AND trigger_type IS NULL AND trigger_id IS NULL AND trigger_name IS NULL", "old").
			Count(&nulls).Error)
		assert.Equal(t, int64(1), nulls, "the old row has NULL trigger columns")

		list, total, err := NewGORMNotificationRepository(db.DB).ListByUser(context.Background(), "u1", false, 10, 0)
		require.NoError(t, err)
		require.Equal(t, int64(1), total)
		assert.Equal(t, models.Trigger{}, list[0].Trigger, "NULL reads as no trigger")
	})

	t.Run("no table", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, mig.Up(db.DB))
		require.NoError(t, mig.Down(db.DB))
	})
}

// TestGORMNotificationRepository_Trigger checks that Create stores the
// trigger, NULL for an empty one, and that ListByUser reads it back.
func TestGORMNotificationRepository_Trigger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		trigger  models.Trigger
		wantNull bool
	}{
		{name: "user", trigger: models.Trigger{TriggerType: "user", TriggerID: "u9", TriggerName: "alice"}},
		{name: "cleanup policy", trigger: models.Trigger{TriggerType: "cleanup-policy", TriggerID: "p-1", TriggerName: "nightly"}},
		{name: "ttl", trigger: models.Trigger{TriggerType: "ttl"}},
		{name: "empty", wantNull: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			db := setupMigrationTestDB(t)
			require.NoError(t, db.AutoMigrate())
			repo := NewGORMNotificationRepository(db.DB)

			require.NoError(t, repo.Create(ctx, &models.Notification{
				ID: "n1", UserID: "u1", Type: "deployment.stopped", Title: "Stack stopped",
				Trigger: tt.trigger, CreatedAt: time.Now().UTC(),
			}))

			list, _, err := repo.ListByUser(ctx, "u1", false, 10, 0)
			require.NoError(t, err)
			require.Len(t, list, 1)
			assert.Equal(t, tt.trigger, list[0].Trigger)

			var nullType int64
			require.NoError(t, db.DB.Table("notifications").Where("id = ? AND trigger_type IS NULL", "n1").Count(&nullType).Error)
			var nullName int64
			require.NoError(t, db.DB.Table("notifications").Where("id = ? AND trigger_name IS NULL", "n1").Count(&nullName).Error)
			assert.Equal(t, tt.wantNull, nullType == 1, "trigger_type NULL")
			assert.Equal(t, tt.trigger.TriggerName == "", nullName == 1, "an empty name is NULL")
		})
	}
}
