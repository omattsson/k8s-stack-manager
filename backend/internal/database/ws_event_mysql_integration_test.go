//go:build integration

package database

import (
	"context"
	"os"
	"strings"
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

// setupMySQLWSEventDB creates an empty MySQL database and runs the full
// migration chain, so ws_events has the schema of migration 46.
func setupMySQLWSEventDB(t *testing.T) *GORMWSEventRepository {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)

	admin, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "Failed to connect to MySQL — is the container running?")

	const dbName = "migration_test_ws_events"
	require.NoError(t, admin.Exec("DROP DATABASE IF EXISTS "+dbName).Error)
	require.NoError(t, admin.Exec("CREATE DATABASE "+dbName).Error)
	t.Cleanup(func() { _ = admin.Exec("DROP DATABASE IF EXISTS " + dbName).Error })

	cfg.DBName = dbName
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	// No migration creates the clusters table (see setupMySQLMigrationDB).
	require.NoError(t, db.AutoMigrate(&models.Cluster{}))
	require.NoError(t, (&Database{DB: db}).AutoMigrate())
	return NewGORMWSEventRepository(db)
}

// TestDatabaseWSEventRepository runs the ws_events repository on MySQL:
// multi-row insert IDs, the CASE payload projection, ListByIDs, MAX(id),
// a MEDIUMTEXT payload above 64 KiB and the batched delete.
func TestDatabaseWSEventRepository(t *testing.T) {
	repo := setupMySQLWSEventDB(t)
	ctx := context.Background()

	var colType string
	require.NoError(t, repo.db.Raw(
		"SELECT DATA_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ws_events' AND COLUMN_NAME = 'payload'",
	).Scan(&colType).Error)
	assert.Equal(t, "mediumtext", colType)

	maxID, err := repo.MaxID(ctx)
	require.NoError(t, err)
	assert.Zero(t, maxID)

	now := time.Now().UTC()
	large := strings.Repeat("x", 300*1024) // above TEXT (64 KiB)
	events := []*models.WSEvent{
		{CreatedAt: now.Add(-10 * time.Minute), Target: models.WSEventTargetAll, Origin: "a", Payload: "old-a"},
		{CreatedAt: now, Target: "instance:i1", Origin: "b", Payload: large},
		{CreatedAt: now.Add(-10 * time.Minute), Target: "user:u1", Origin: "b", Payload: "old-b"},
		{CreatedAt: now, Target: "revoke:user:u1", Origin: "a", Payload: ""},
	}
	require.NoError(t, repo.Insert(ctx, events))
	for i := 1; i < len(events); i++ {
		assert.Equal(t, events[i-1].ID+1, events[i].ID, "multi-row insert gets consecutive IDs")
	}

	maxID, err = repo.MaxID(ctx)
	require.NoError(t, err)
	assert.Equal(t, events[3].ID, maxID)

	got, err := repo.ListAfter(ctx, 0, 10, "a")
	require.NoError(t, err)
	require.Len(t, got, 4)
	assert.Empty(t, got[0].Payload, "own payload is empty (CASE)")
	assert.Equal(t, large, got[1].Payload, "MEDIUMTEXT keeps the full payload")
	assert.Equal(t, "old-b", got[2].Payload)
	assert.Equal(t, "instance:i1", got[1].Target)
	assert.Equal(t, "b", got[1].Origin)
	assert.WithinDuration(t, now, got[1].CreatedAt, time.Second)

	got, err = repo.ListAfter(ctx, events[1].ID, 1, "b")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, events[2].ID, got[0].ID)
	assert.Empty(t, got[0].Payload)

	got, err = repo.ListByIDs(ctx, []int64{events[3].ID, events[0].ID, 99999}, "b")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, events[0].ID, got[0].ID)
	assert.Equal(t, "old-a", got[0].Payload)

	ids, err := repo.IDsAfter(ctx, events[0].ID, 2)
	require.NoError(t, err)
	assert.Equal(t, []int64{events[1].ID, events[2].ID}, ids)

	// Batch size 1: two batches for the two old rows; the fresh rows stay.
	deleted, err := repo.DeleteOlderThan(ctx, now.Add(-5*time.Minute), 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)
	got, err = repo.ListAfter(ctx, 0, 10, "")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, events[1].ID, got[0].ID)
	assert.Equal(t, events[3].ID, got[1].ID)

	// Down and Up of migration 46.
	mig := wsEventsMigration()
	require.NoError(t, mig.Down(repo.db))
	assert.False(t, repo.db.Migrator().HasTable("ws_events"))
	require.NoError(t, mig.Up(repo.db))
	assert.True(t, repo.db.Migrator().HasIndex(&models.WSEvent{}, "idx_ws_events_created_at"))
}
