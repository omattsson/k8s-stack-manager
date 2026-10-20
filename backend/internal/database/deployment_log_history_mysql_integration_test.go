//go:build integration

package database

import (
	"os"
	"testing"

	"backend/internal/models"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestDatabaseDeploymentLogHistory runs the deploy history list (filters on
// the count and on both paging paths, summary columns, offset next cursor)
// and the history highlights on MySQL, after the full migration chain.
func TestDatabaseDeploymentLogHistory(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)
	admin, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "Failed to connect to MySQL — is the container running?")

	const dbName = "migration_test_deploy_log_history"
	require.NoError(t, admin.Exec("DROP DATABASE IF EXISTS "+dbName).Error)
	require.NoError(t, admin.Exec("CREATE DATABASE "+dbName).Error)
	t.Cleanup(func() { _ = admin.Exec("DROP DATABASE IF EXISTS " + dbName).Error })

	cfg.DBName = dbName
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	// No migration creates the clusters table (see setupMySQLMigrationDB).
	require.NoError(t, db.AutoMigrate(&models.Cluster{}))
	require.NoError(t, (&Database{DB: db}).AutoMigrate())

	// The highlight queries use this index.
	require.True(t, db.Migrator().HasIndex(&models.DeploymentLog{}, "idx_deployment_logs_instance_action_started"))

	repo := NewGORMDeploymentLogRepository(db)
	t.Run("list", func(t *testing.T) {
		runDeployLogHistoryListChecks(t, repo)
	})
	t.Run("highlights", func(t *testing.T) {
		runDeployLogHighlightChecks(t, repo)
	})
}
