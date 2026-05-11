package database

import (
	"testing"

	"backend/internal/database/schema"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupMigrationTestDB creates a fresh SQLite in-memory database for migration
// tests. It does NOT run AutoMigrate so tests can verify migration behaviour
// from a blank slate. However, it pre-creates the clusters table because
// migration 000005 adds a column to it, and there is no migration that creates
// the clusters table itself (it was part of a different init path).
func setupMigrationTestDB(t *testing.T) *Database {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// Pre-create the clusters table (migration 000005 alters it).
	require.NoError(t, db.AutoMigrate(&models.Cluster{}))
	return &Database{DB: db}
}

// setupTestDBWithAllTables creates a fresh SQLite in-memory database with all
// model tables created via GORM's AutoMigrate (bypassing the versioned migration
// system). Use this for repository tests that just need a working schema.
func setupTestDBWithAllTables(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// Limit to 1 connection so all queries hit the same in-memory database.
	// SQLite `:memory:` creates a separate DB per connection; without this,
	// parallel subtests may get a fresh, empty database from the pool.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(1)

	require.NoError(t, db.AutoMigrate(
		&models.User{},
		&models.Item{},
		&models.Cluster{},
		&models.Notification{},
		&models.NotificationPreference{},
		&models.ResourceQuotaConfig{},
		&models.TemplateVersion{},
		&models.InstanceQuotaOverride{},
		&models.AuditLog{},
		&models.APIKey{},
		&models.StackDefinition{},
		&models.StackTemplate{},
		&models.StackInstance{},
		&models.ChartConfig{},
		&models.TemplateChartConfig{},
		&models.ValueOverride{},
		&models.ChartBranchOverride{},
		&models.DeploymentLog{},
		&models.SharedValues{},
		&models.CleanupPolicy{},
		&models.UserFavorite{},
	))
	return db
}

func TestAutoMigrate(t *testing.T) {
	t.Parallel()

	t.Run("creates all expected tables", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)

		err := db.AutoMigrate()
		require.NoError(t, err)

		tables, err := db.DB.Migrator().GetTables()
		require.NoError(t, err)

		expectedTables := []string{
			"users",
			"items",
			"notifications",
			"notification_preferences",
			"resource_quota_configs",
			"template_versions",
			"instance_quota_overrides",
			"notification_channels",
			"notification_channel_subscriptions",
			"notification_delivery_logs",
			"schema_versions",
		}
		for _, table := range expectedTables {
			assert.Contains(t, tables, table, "expected table %q to exist", table)
		}
	})

	t.Run("idempotent - running twice succeeds", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)

		require.NoError(t, db.AutoMigrate())
		// Running a second time should be a no-op (all migrations already applied).
		err := db.AutoMigrate()
		assert.NoError(t, err, "running AutoMigrate twice should not fail")
	})

	t.Run("records schema versions", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, db.AutoMigrate())

		var versions []schema.SchemaVersion
		err := db.DB.Order("version ASC").Find(&versions).Error
		require.NoError(t, err)

		// We expect at least 7 migrations from the current codebase.
		// (Migration 000008 uses information_schema which is MySQL-specific,
		// so it may or may not succeed on SQLite; we check the ones that do.)
		assert.GreaterOrEqual(t, len(versions), 7, "expected at least 7 migration records")

		// Verify the first and later known migration versions exist.
		versionStrings := make([]string, len(versions))
		for i, v := range versions {
			versionStrings[i] = v.Version
		}
		assert.Contains(t, versionStrings, "20231201000001", "base tables migration")
		assert.Contains(t, versionStrings, "20231201000004", "notification tables migration")
		assert.Contains(t, versionStrings, "20231201000006", "template versions migration")
		assert.Contains(t, versionStrings, "20231201000007", "instance quota overrides migration")
	})

	t.Run("migration versions are unique", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, db.AutoMigrate())

		var versions []schema.SchemaVersion
		require.NoError(t, db.DB.Find(&versions).Error)

		seen := make(map[string]bool)
		for _, v := range versions {
			assert.False(t, seen[v.Version], "duplicate migration version: %s", v.Version)
			seen[v.Version] = true
		}
	})

	t.Run("base tables migration creates users and items", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, db.AutoMigrate())

		// Verify User table can hold data.
		user := models.User{ID: "u1", Username: "tester", Role: "user"}
		require.NoError(t, db.DB.Create(&user).Error)

		var found models.User
		require.NoError(t, db.DB.First(&found, "id = ?", "u1").Error)
		assert.Equal(t, "tester", found.Username)

		// Verify Item table can hold data.
		item := models.Item{Name: "widget", Price: 9.99}
		require.NoError(t, db.DB.Create(&item).Error)
		assert.NotZero(t, item.ID)
	})

	t.Run("item version default is 1 after migration 000003", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, db.AutoMigrate())

		item := models.Item{Name: "versioned", Price: 1.0}
		require.NoError(t, db.DB.Create(&item).Error)

		var found models.Item
		require.NoError(t, db.DB.First(&found, item.ID).Error)
		// After migration 000003, new items should default to version 1.
		assert.Equal(t, uint(1), found.Version)
	})

	t.Run("notification tables migration creates tables", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, db.AutoMigrate())

		assert.True(t, db.DB.Migrator().HasTable(&models.Notification{}))
		assert.True(t, db.DB.Migrator().HasTable(&models.NotificationPreference{}))

		// Verify we can insert a notification.
		n := models.Notification{
			ID:     "n1",
			UserID: "u1",
			Type:   "deploy",
			Title:  "Deployed",
		}
		require.NoError(t, db.DB.Create(&n).Error)
	})

	t.Run("resource quota config migration creates table", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, db.AutoMigrate())

		assert.True(t, db.DB.Migrator().HasTable(&models.ResourceQuotaConfig{}))

		rq := models.ResourceQuotaConfig{
			ID:        "rq1",
			ClusterID: "c1",
			PodLimit:  10,
		}
		require.NoError(t, db.DB.Create(&rq).Error)
	})

	t.Run("template versions migration creates table", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, db.AutoMigrate())

		assert.True(t, db.DB.Migrator().HasTable(&models.TemplateVersion{}))

		tv := models.TemplateVersion{
			ID:         "tv1",
			TemplateID: "t1",
			Version:    "1.0.0",
			Snapshot:   `{"template":{},"charts":[]}`,
		}
		require.NoError(t, db.DB.Create(&tv).Error)
	})

	t.Run("instance quota overrides migration creates table", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, db.AutoMigrate())

		assert.True(t, db.DB.Migrator().HasTable(&models.InstanceQuotaOverride{}))

		podLimit := 5
		iqo := models.InstanceQuotaOverride{
			ID:              "iqo1",
			StackInstanceID: "si1",
			PodLimit:        &podLimit,
		}
		require.NoError(t, db.DB.Create(&iqo).Error)
	})

	t.Run("migrator tracks applied versions correctly", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, db.AutoMigrate())

		var versions []schema.SchemaVersion
		require.NoError(t, db.DB.Find(&versions).Error)

		for _, v := range versions {
			assert.NotEmpty(t, v.Version, "version string should not be empty")
			assert.NotEmpty(t, v.Name, "migration name should not be empty")
			assert.False(t, v.AppliedAt.IsZero(), "applied_at should be set")
		}
	})

	t.Run("cluster max_instances_per_user column exists after migration 000005", func(t *testing.T) {
		t.Parallel()
		db := setupMigrationTestDB(t)
		require.NoError(t, db.AutoMigrate())

		assert.True(t, db.DB.Migrator().HasColumn(&models.Cluster{}, "MaxInstancesPerUser"),
			"clusters table should have max_instances_per_user column")
	})
}

// TestMigrator_Simple verifies that the schema migrator correctly runs Up/Down
// migrations with a minimal set of migrations.
func TestMigrator_Simple(t *testing.T) {
	t.Parallel()

	t.Run("single migration up and down", func(t *testing.T) {
		t.Parallel()
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		require.NoError(t, err)

		migrator := schema.NewMigrator(db)

		type TestMigrationModel struct {
			ID   uint   `gorm:"primarykey"`
			Name string `gorm:"size:255"`
		}

		migrator.AddMigration(schema.Migration{
			Version:     "001",
			Name:        "create_test_table",
			Description: "Create a test table",
			Up: func(tx *gorm.DB) error {
				return tx.AutoMigrate(&TestMigrationModel{})
			},
			Down: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable(&TestMigrationModel{})
			},
		})

		// Run up.
		require.NoError(t, migrator.MigrateUp())
		assert.True(t, db.Migrator().HasTable(&TestMigrationModel{}))

		// Run up again (idempotent).
		require.NoError(t, migrator.MigrateUp())

		// Run down.
		require.NoError(t, migrator.MigrateDown())
		assert.False(t, db.Migrator().HasTable(&TestMigrationModel{}))
	})

	t.Run("migrate down with no migrations applied returns nil", func(t *testing.T) {
		t.Parallel()
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		require.NoError(t, err)

		migrator := schema.NewMigrator(db)
		// Ensure schema_versions table exists so the query does not fail.
		require.NoError(t, db.AutoMigrate(&schema.SchemaVersion{}))

		err = migrator.MigrateDown()
		assert.NoError(t, err, "MigrateDown with no applied migrations should return nil")
	})
}
