//go:build integration

package database

import (
	"context"
	"os"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"backend/internal/models"
)

// TestDatabaseMySQL_NotificationTrigger checks migration 64 (#652) on MySQL:
// the column types after the full chain, Down, Down, Up and Up with an old
// row, and that Create stores NULL for an empty trigger.
func TestDatabaseMySQL_NotificationTrigger(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)
	admin, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "Failed to connect to MySQL — is the container running?")

	const dbName = "migration_test_notification_trigger"
	require.NoError(t, admin.Exec("DROP DATABASE IF EXISTS "+dbName).Error)
	require.NoError(t, admin.Exec("CREATE DATABASE "+dbName).Error)
	t.Cleanup(func() { _ = admin.Exec("DROP DATABASE IF EXISTS " + dbName).Error })

	cfg.DBName = dbName
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Cluster{}), "migration 5 alters clusters")
	require.NoError(t, (&Database{DB: db}).AutoMigrate())

	ctx := context.Background()
	m := db.Migrator()
	mig := notificationTriggerMigration()

	type column struct {
		ColumnName string  `gorm:"column:COLUMN_NAME"`
		ColumnType string  `gorm:"column:COLUMN_TYPE"`
		IsNullable string  `gorm:"column:IS_NULLABLE"`
		Default    *string `gorm:"column:COLUMN_DEFAULT"`
	}
	triggerColumns := func(t *testing.T) map[string]column {
		t.Helper()
		var cols []column
		require.NoError(t, db.Raw(`SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'notifications' AND COLUMN_NAME LIKE 'trigger\_%'`, dbName).Scan(&cols).Error)
		out := map[string]column{}
		for _, c := range cols {
			out[c.ColumnName] = c
		}
		return out
	}
	assertColumns := func(t *testing.T) {
		t.Helper()
		cols := triggerColumns(t)
		require.Len(t, cols, 3)
		for name, wantType := range map[string]string{
			"trigger_type": "varchar(20)", "trigger_id": "varchar(36)", "trigger_name": "varchar(255)",
		} {
			assert.Equal(t, wantType, cols[name].ColumnType, name)
			assert.Equal(t, "YES", cols[name].IsNullable, name)
			assert.Nil(t, cols[name].Default, "%s has no default value", name)
		}
	}

	t.Run("full chain creates the columns", func(t *testing.T) {
		assertColumns(t)
		require.NoError(t, mig.Up(db), "Up on existing columns")
		assertColumns(t)
	})

	t.Run("down and up twice keep an old row", func(t *testing.T) {
		require.NoError(t, mig.Down(db))
		assert.Empty(t, triggerColumns(t))
		require.NoError(t, mig.Down(db), "Down without the columns")

		// A row from before the migration.
		require.NoError(t, db.Table("notifications").Create(map[string]interface{}{
			"id": "old", "user_id": "u-old", "type": "deployment.stopped", "title": "Stack stopped",
			"message": "Stack web has been stopped", "created_at": time.Now().UTC(), "is_read": false,
		}).Error)

		require.NoError(t, mig.Up(db))
		require.NoError(t, mig.Up(db), "Up must be idempotent")
		assertColumns(t)
		for _, col := range notificationTriggerColumns {
			assert.True(t, m.HasColumn(&models.Notification{}, col), col)
		}

		var nulls int64
		require.NoError(t, db.Table("notifications").
			Where("id = ? AND trigger_type IS NULL AND trigger_id IS NULL AND trigger_name IS NULL", "old").
			Count(&nulls).Error)
		assert.Equal(t, int64(1), nulls)

		list, _, _, err := NewGORMNotificationRepository(db).ListByUser(ctx, "u-old", false, 10, 0)
		require.NoError(t, err)
		require.Len(t, list, 1)
		assert.Equal(t, models.Trigger{}, list[0].Trigger, "an old row has no trigger")
	})

	t.Run("create stores the trigger and NULL for an empty one", func(t *testing.T) {
		repo := NewGORMNotificationRepository(db)
		now := time.Now().UTC()
		rows := []models.Notification{
			{ID: "user", Trigger: models.Trigger{TriggerType: "user", TriggerID: "u9", TriggerName: "alice"}},
			{ID: "policy", Trigger: models.Trigger{TriggerType: "cleanup-policy", TriggerID: "p-1", TriggerName: "nightly"}},
			{ID: "ttl", Trigger: models.Trigger{TriggerType: "ttl"}},
			{ID: "none"},
		}
		for i := range rows {
			rows[i].UserID = "u-new"
			rows[i].Type = "deployment.stopped"
			rows[i].Title = "Stack stopped"
			rows[i].CreatedAt = now.Add(time.Duration(i) * time.Second)
			require.NoError(t, repo.Create(ctx, &rows[i]))
		}

		list, total, _, err := repo.ListByUser(ctx, "u-new", false, 10, 0)
		require.NoError(t, err)
		require.Equal(t, int64(4), total)
		got := map[string]models.Trigger{}
		for _, n := range list {
			got[n.ID] = n.Trigger
		}
		for _, r := range rows {
			assert.Equal(t, r.Trigger, got[r.ID], r.ID)
		}

		var nullIDs []string
		require.NoError(t, db.Table("notifications").Where("user_id = ? AND trigger_type IS NULL", "u-new").
			Order("id").Pluck("id", &nullIDs).Error)
		assert.Equal(t, []string{"none"}, nullIDs, "only the row without a trigger has NULL")
		var nullNames []string
		require.NoError(t, db.Table("notifications").Where("user_id = ? AND trigger_name IS NULL", "u-new").
			Order("id").Pluck("id", &nullNames).Error)
		assert.Equal(t, []string{"none", "ttl"}, nullNames, "an empty name is NULL, not ''")
	})
}
