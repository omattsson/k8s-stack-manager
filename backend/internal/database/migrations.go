package database

import (
	"log/slog"

	"backend/internal/database/schema"
	"backend/internal/models"

	"gorm.io/gorm"
)

// AutoMigrate runs database migrations for all models
func (d *Database) AutoMigrate() error {
	slog.Info("Running database migrations...")

	// Initialize migrator
	migrator := schema.NewMigrator(d.DB)

	// Add migrations
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000001",
		Name:        "create_base_tables",
		Description: "Create initial user and item tables",
		Up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.User{}, &models.Item{})
		},
		Down: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&models.Item{}, &models.User{})
		},
	})

	// Example of adding indexes and constraints in a separate migration
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000002",
		Name:        "add_indexes",
		Description: "Add indexes for performance optimization",
		Up: func(tx *gorm.DB) error {
			// Add composite index on items
			var count int64
			tx.Raw("SELECT COUNT(1) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'items' AND index_name = 'idx_items_name_price'").Scan(&count)
			if count == 0 {
				if err := tx.Exec("CREATE INDEX idx_items_name_price ON items(name, price)").Error; err != nil {
					return err
				}
			}

			// Add unique index on username
			tx.Raw("SELECT COUNT(1) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'users' AND index_name = 'idx_users_username'").Scan(&count)
			if count == 0 {
				if err := tx.Exec("CREATE UNIQUE INDEX idx_users_username ON users(username)").Error; err != nil {
					return err
				}
			}
			return nil
		},
		Down: func(tx *gorm.DB) error {
			if err := tx.Exec("DROP INDEX idx_items_name_price ON items").Error; err != nil {
				return err
			}
			return tx.Exec("DROP INDEX idx_users_username ON users").Error
		},
	})

	// Ensure version column exists and update defaults for optimistic locking
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000003",
		Name:        "update_items_version_default",
		Description: "Add version column if missing, set default to 1, and update existing rows",
		Up: func(tx *gorm.DB) error {
			// Ensure the version column exists (handles cases where migration 000001
			// was applied before the Version field was added to the Item model)
			if err := tx.AutoMigrate(&models.Item{}); err != nil {
				return err
			}
			// Update existing rows that still have the old default of 0 to the new default of 1
			if err := tx.Exec("UPDATE items SET version = 1 WHERE version = 0").Error; err != nil {
				return err
			}
			// Alter column default to 1 (MySQL syntax; SQLite defaults are set via AutoMigrate)
			if tx.Dialector.Name() == "mysql" {
				return tx.Exec("ALTER TABLE items ALTER COLUMN version SET DEFAULT 1").Error
			}
			return nil
		},
		Down: func(tx *gorm.DB) error {
			if tx.Dialector.Name() == "mysql" {
				return tx.Exec("ALTER TABLE items ALTER COLUMN version SET DEFAULT 0").Error
			}
			return nil
		},
	})

	// Create notifications and notification_preferences tables
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000004",
		Name:        "create_notification_tables",
		Description: "Create notifications and notification_preferences tables",
		Up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&models.Notification{}, &models.NotificationPreference{}); err != nil {
				return err
			}
			return nil
		},
		Down: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable(&models.NotificationPreference{}); err != nil {
				return err
			}
			return tx.Migrator().DropTable(&models.Notification{})
		},
	})

	// Create resource_quota_configs table and add max_instances_per_user to clusters
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000005",
		Name:        "create_resource_quota_configs",
		Description: "Create resource_quota_configs table and add max_instances_per_user to clusters",
		Up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&models.ResourceQuotaConfig{}); err != nil {
				return err
			}
			// Add max_instances_per_user column to clusters using GORM's dialect-agnostic migrator.
			if !tx.Migrator().HasColumn(&models.Cluster{}, "MaxInstancesPerUser") {
				if err := tx.Migrator().AddColumn(&models.Cluster{}, "MaxInstancesPerUser"); err != nil {
					return err
				}
			}
			return nil
		},
		Down: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable(&models.ResourceQuotaConfig{}); err != nil {
				return err
			}
			if tx.Migrator().HasColumn(&models.Cluster{}, "MaxInstancesPerUser") {
				if err := tx.Migrator().DropColumn(&models.Cluster{}, "MaxInstancesPerUser"); err != nil {
					return err
				}
			}
			return nil
		},
	})

	// Create template_versions table for template versioning & upgrades
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000006",
		Name:        "create_template_versions",
		Description: "Create template_versions table for tracking published template snapshots",
		Up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.TemplateVersion{})
		},
		Down: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&models.TemplateVersion{})
		},
	})

	// Create instance_quota_overrides table for per-instance resource quota overrides
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000007",
		Name:        "create_instance_quota_overrides",
		Description: "Create instance_quota_overrides table for per-instance resource quota overrides",
		Up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.InstanceQuotaOverride{})
		},
		Down: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&models.InstanceQuotaOverride{})
		},
	})

	// Add OIDC-related columns to users table for external authentication
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000008",
		Name:        "add_oidc_fields_to_users",
		Description: "Add auth_provider, external_id, and email columns to users table for OIDC support",
		Up: func(tx *gorm.DB) error {
			// Add columns idempotently — check existence before adding.
			if !tx.Migrator().HasColumn(&models.User{}, "auth_provider") {
				if err := tx.Exec("ALTER TABLE users ADD COLUMN auth_provider VARCHAR(50) NOT NULL DEFAULT 'local'").Error; err != nil {
					return err
				}
			}
			if !tx.Migrator().HasColumn(&models.User{}, "external_id") {
				if err := tx.Exec("ALTER TABLE users ADD COLUMN external_id VARCHAR(255) NULL DEFAULT NULL").Error; err != nil {
					return err
				}
			}
			if !tx.Migrator().HasColumn(&models.User{}, "email") {
				if err := tx.Exec("ALTER TABLE users ADD COLUMN email VARCHAR(255) NOT NULL DEFAULT ''").Error; err != nil {
					return err
				}
			}
			// Set existing rows to 'local'.
			if err := tx.Exec("UPDATE users SET auth_provider = 'local' WHERE auth_provider = '' OR auth_provider IS NULL").Error; err != nil {
				return err
			}
			// Normalise legacy empty-string external_id to NULL so unique index works.
			if err := tx.Exec("UPDATE users SET external_id = NULL WHERE external_id = ''").Error; err != nil {
				return err
			}
			// Add UNIQUE index on (auth_provider, external_id) for FindByExternalID queries.
			// MySQL allows multiple NULLs in a unique index, so local users (NULL external_id) won't collide.
			var count int64
			tx.Raw("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'users' AND index_name = 'idx_users_auth_provider_external_id'").Scan(&count)
			if count == 0 {
				if err := tx.Exec("CREATE UNIQUE INDEX idx_users_auth_provider_external_id ON users (auth_provider, external_id)").Error; err != nil {
					return err
				}
			}
			return nil
		},
		Down: func(tx *gorm.DB) error {
			_ = tx.Migrator().DropIndex(&models.User{}, "idx_users_auth_provider_external_id")
			for _, col := range []string{"email", "external_id", "auth_provider"} {
				if tx.Migrator().HasColumn(&models.User{}, col) {
					if err := tx.Migrator().DropColumn(&models.User{}, col); err != nil {
						return err
					}
				}
			}
			return nil
		},
	})

	// Run migrations
	if err := migrator.MigrateUp(); err != nil {
		return err
	}

	slog.Info("Database migrations completed successfully")
	return nil
}
