//go:build integration

package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"backend/internal/models"
)

// TestDatabaseRetentionDeploymentLogs runs the deploy log retention on MySQL:
// the keep rules (a subquery on deployment_logs in the ID select; MySQL
// refuses it in a DELETE, error 1093), the database time and the batches.
func TestDatabaseRetentionDeploymentLogs(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)
	admin, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "Failed to connect to MySQL — is the container running?")

	const dbName = "migration_test_retention"
	require.NoError(t, admin.Exec("DROP DATABASE IF EXISTS "+dbName).Error)
	require.NoError(t, admin.Exec("CREATE DATABASE "+dbName).Error)
	t.Cleanup(func() { _ = admin.Exec("DROP DATABASE IF EXISTS " + dbName).Error })

	cfg.DBName = dbName
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Cluster{}))
	require.NoError(t, (&Database{DB: db}).AutoMigrate())

	fixtures := seedDeployLogFixtures(t, db)
	repo := NewGORMRetentionRepository(db).WithPause(0)
	deleted, err := repo.DeleteDeploymentLogsOlderThan(context.Background(), retentionAge, 3)
	require.NoError(t, err)
	assertDeployLogFixtures(t, db, fixtures, deleted)

	// The other tables: 5 old rows and 1 recent row each, batches of 2.
	now := time.Now().UTC()
	tables := []struct {
		table, column string
		create        func(id string) error
		del           func(context.Context) (int64, error)
	}{
		{"audit_logs", "timestamp",
			func(id string) error {
				return db.Create(&models.AuditLog{ID: id, UserID: "u1", Action: "create", EntityType: "stack_instance", EntityID: "e1", Timestamp: now}).Error
			},
			func(ctx context.Context) (int64, error) { return repo.DeleteAuditLogsOlderThan(ctx, retentionAge, 2) }},
		{"notifications", "created_at",
			func(id string) error {
				return db.Create(&models.Notification{ID: id, UserID: "u1", Type: "deployment.success", Title: "t", CreatedAt: now}).Error
			},
			func(ctx context.Context) (int64, error) {
				return repo.DeleteNotificationsOlderThan(ctx, retentionAge, 2)
			}},
		{"notification_delivery_logs", "created_at",
			func(id string) error {
				return db.Create(&models.NotificationDeliveryLog{ID: id, ChannelID: "ch1", EventType: "deployment.success", CreatedAt: now}).Error
			},
			func(ctx context.Context) (int64, error) {
				return repo.DeleteDeliveryLogsOlderThan(ctx, retentionAge, 2)
			}},
	}
	for _, tt := range tables {
		for i := 0; i < 6; i++ {
			id := fmt.Sprintf("%s-%d", tt.table, i)
			require.NoError(t, tt.create(id))
			age := 5 * 24 * time.Hour
			if i == 5 {
				age = time.Hour
			}
			setRowAge(t, db, tt.table, tt.column, id, age)
		}
		deleted, err := tt.del(context.Background())
		require.NoError(t, err, tt.table)
		require.EqualValues(t, 5, deleted, tt.table)
		var left int64
		require.NoError(t, db.Table(tt.table).Count(&left).Error)
		require.EqualValues(t, 1, left, tt.table)
	}
}
