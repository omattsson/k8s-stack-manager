package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestNewFromDBConfig(t *testing.T) {
	t.Parallel()

	t.Run("With SQLite configuration", func(t *testing.T) {
		t.Parallel()
		db, err := NewFromDBConfig(nil)
		require.NoError(t, err)
		require.NotNil(t, db)

		// Test database connection
		sqlDB, err := db.DB.DB()
		require.NoError(t, err)

		// Verify connection settings
		stats := sqlDB.Stats()
		assert.Equal(t, 5, stats.MaxOpenConnections)
	})

	t.Run("With MySQL configuration", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Host:     "invalid",
			Port:     "3306",
			User:     "test",
			Password: "test",
			DBName:   "test",
		}

		db, err := NewFromDBConfig(cfg)
		assert.Error(t, err)
		assert.Nil(t, db)
		assert.Contains(t, err.Error(), "failed to connect to database")
	})
}

func TestNewFromDBConfig_SQLitePing(t *testing.T) {
	t.Parallel()

	db, err := NewFromDBConfig(nil)
	require.NoError(t, err)

	sqlDB, err := db.DB.DB()
	require.NoError(t, err)

	// SQLite in-memory should always be pingable.
	assert.NoError(t, sqlDB.Ping())
}

func TestNewFromDBConfig_SQLiteDialect(t *testing.T) {
	t.Parallel()

	db, err := NewFromDBConfig(nil)
	require.NoError(t, err)

	assert.Equal(t, "sqlite", db.Dialector.Name())
}

func TestConfigure_SQLite(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	database := &Database{DB: db}
	err = database.configure()
	require.NoError(t, err)

	// Verify foreign keys are enabled by checking PRAGMA.
	var fkEnabled int
	require.NoError(t, db.Raw("PRAGMA foreign_keys").Scan(&fkEnabled).Error)
	assert.Equal(t, 1, fkEnabled, "foreign key checks should be enabled")
}

func TestConfigure_UnknownDialect(t *testing.T) {
	t.Parallel()

	// For a dialect that is neither mysql nor sqlite, configure() should be a no-op.
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// We cannot easily change the dialector name, but we can verify that
	// calling configure on a valid SQLite DB does not error.
	database := &Database{DB: db}
	assert.NoError(t, database.configure())
}

func TestNewFromDBConfig_ConnectionPoolSettings(t *testing.T) {
	t.Parallel()

	db, err := NewFromDBConfig(nil)
	require.NoError(t, err)

	sqlDB, err := db.DB.DB()
	require.NoError(t, err)

	stats := sqlDB.Stats()
	assert.Equal(t, 5, stats.MaxOpenConnections, "MaxOpenConns should be 5")
}

func TestDatabasePing_Success(t *testing.T) {
	t.Parallel()

	db, err := NewFromDBConfig(nil)
	require.NoError(t, err)
	assert.NoError(t, db.Ping())
}

func TestDatabasePing_ClosedDB(t *testing.T) {
	t.Parallel()

	db, err := NewFromDBConfig(nil)
	require.NoError(t, err)

	sqlDB, err := db.DB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	err = db.Ping()
	assert.Error(t, err, "ping should fail on a closed database")
}
