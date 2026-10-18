//go:build integration

package database

import (
	"context"
	"os"
	"testing"
	"time"

	"backend/internal/database/schema"
	"backend/internal/models"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupMySQLMigrationDB creates an empty MySQL database for versioned
// migration tests, so the full migration chain runs from a blank slate.
func setupMySQLMigrationDB(t *testing.T) *Database {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = "root:rootpassword@tcp(localhost:3306)/app?charset=utf8mb4&parseTime=True&loc=Local"
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	require.NoError(t, err)

	admin, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err, "Failed to connect to MySQL — is the container running?")

	const dbName = "migration_test_refresh_tokens"
	require.NoError(t, admin.Exec("DROP DATABASE IF EXISTS "+dbName).Error)
	require.NoError(t, admin.Exec("CREATE DATABASE "+dbName).Error)
	t.Cleanup(func() { _ = admin.Exec("DROP DATABASE IF EXISTS " + dbName).Error })

	cfg.DBName = dbName
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	// Same as the SQLite helper: no migration creates the clusters table.
	require.NoError(t, db.AutoMigrate(&models.Cluster{}))
	return &Database{DB: db}
}

// TestDatabaseRefreshTokenSessionFamily runs migration 41 on MySQL
// against a pre-41 refresh_tokens table with a legacy row, exercises the
// session-family repository methods, and checks the Down step.
func TestDatabaseRefreshTokenSessionFamily(t *testing.T) {
	db := setupMySQLMigrationDB(t)
	require.NoError(t, db.AutoMigrate())

	// Back to the pre-41 schema.
	m := db.DB.Migrator()
	require.NoError(t, m.DropIndex(&models.RefreshToken{}, "FamilyID"))
	for _, field := range []string{"RotatedAt", "SessionStartedAt", "FamilyID"} {
		require.NoError(t, m.DropColumn(&models.RefreshToken{}, field))
	}
	require.NoError(t, db.DB.Unscoped().Where("version = ?", "20261008000041").Delete(&schema.SchemaVersion{}).Error)

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, db.DB.Exec(
		"INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, last_activity, created_at, revoked) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"legacy-1", "u1", "hash-legacy", time.Now().UTC().Add(time.Hour), created, created, false,
	).Error)

	require.NoError(t, db.AutoMigrate())

	var legacy models.RefreshToken
	require.NoError(t, db.DB.Where("id = ?", "legacy-1").First(&legacy).Error)
	assert.Equal(t, "legacy-1", legacy.FamilyID)
	assert.True(t, legacy.SessionStartedAt.Equal(created), "got %s", legacy.SessionStartedAt)
	assert.Nil(t, legacy.RotatedAt)

	repo := NewGORMRefreshTokenRepository(db.DB)
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.Create(&models.RefreshToken{
		ID: "succ-1", UserID: "u1", FamilyID: "legacy-1", TokenHash: "hash-succ",
		ExpiresAt: now.Add(time.Hour), LastActivity: now.Add(-time.Hour), CreatedAt: now, SessionStartedAt: created,
	}))

	affected, err := repo.MarkRotatedIfActive("legacy-1", now)
	require.NoError(t, err)
	assert.Equal(t, int64(1), affected)
	require.NoError(t, db.DB.Where("id = ?", "legacy-1").First(&legacy).Error)
	require.NotNil(t, legacy.RotatedAt)

	require.NoError(t, repo.TouchFamily(context.Background(), "legacy-1", now))
	var succ models.RefreshToken
	require.NoError(t, db.DB.Where("id = ?", "succ-1").First(&succ).Error)
	assert.WithinDuration(t, now, succ.LastActivity, time.Second)

	count, err := repo.CountActiveInFamily("legacy-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	require.NoError(t, repo.RevokeFamily("legacy-1"))
	count, err = repo.CountActiveInFamily("legacy-1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	// Down removes the columns and the index again; Up after Down restores them.
	mig := refreshTokenSessionFamilyMigration()
	require.NoError(t, mig.Down(db.DB))
	for _, field := range []string{"FamilyID", "SessionStartedAt", "RotatedAt"} {
		assert.False(t, m.HasColumn(&models.RefreshToken{}, field), "column %s should be dropped", field)
	}
	assert.False(t, m.HasIndex(&models.RefreshToken{}, "FamilyID"))
	require.NoError(t, mig.Down(db.DB), "Down must be idempotent")
	require.NoError(t, mig.Up(db.DB))
	assert.True(t, m.HasColumn(&models.RefreshToken{}, "FamilyID"))
	assert.True(t, m.HasIndex(&models.RefreshToken{}, "FamilyID"))
}
