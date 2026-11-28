//go:build integration

package database

import (
	"context"
	"os"
	"testing"

	"backend/internal/models"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestDatabaseDeploymentLogImageDigests runs migration 69 (Down and Up
// twice) and LatestImageDigests on MySQL, after the full migration chain
// (#811 R1).
func TestDatabaseDeploymentLogImageDigests(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)
	admin, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "Failed to connect to MySQL — is the container running?")

	const dbName = "migration_test_deploy_log_image_digests"
	require.NoError(t, admin.Exec("DROP DATABASE IF EXISTS "+dbName).Error)
	require.NoError(t, admin.Exec("CREATE DATABASE "+dbName).Error)
	t.Cleanup(func() { _ = admin.Exec("DROP DATABASE IF EXISTS " + dbName).Error })

	cfg.DBName = dbName
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	// No migration creates the clusters table (see setupMySQLMigrationDB).
	require.NoError(t, db.AutoMigrate(&models.Cluster{}))
	require.NoError(t, (&Database{DB: db}).AutoMigrate())

	m := db.Migrator()
	require.True(t, m.HasColumn(&models.DeploymentLog{}, "ImageDigests"))
	mig := deployLogImageDigestsMigration()
	require.NoError(t, mig.Down(db))
	require.NoError(t, mig.Down(db))
	assert.False(t, m.HasColumn(&models.DeploymentLog{}, "ImageDigests"))
	require.NoError(t, mig.Up(db))
	require.NoError(t, mig.Up(db))
	require.True(t, m.HasColumn(&models.DeploymentLog{}, "ImageDigests"))

	var nullable string
	require.NoError(t, db.Raw("SELECT IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'deployment_logs' AND COLUMN_NAME = 'image_digests'", dbName).Scan(&nullable).Error)
	assert.Equal(t, "YES", nullable)

	repo := NewGORMDeploymentLogRepository(db)
	want := seedImageDigestLogs(t, repo)
	got, err := repo.LatestImageDigests(context.Background(), "si-1")
	require.NoError(t, err)
	assert.Equal(t, want, got)

	// A log of an older release has NULL in the column: skipped.
	require.NoError(t, db.Exec("UPDATE deployment_logs SET image_digests = NULL WHERE id = 'l7'").Error)
	got, err = repo.LatestImageDigests(context.Background(), "si-1")
	require.NoError(t, err)
	assert.NotEqual(t, want["r/app:v1"], got["r/app:v1"])
	assert.NotEmpty(t, got["r/app:v1"], "the next newest log with digests wins")

	checkImageDigestScanBound(t, repo)

	checkImageDigestsOf(t, repo)
}
