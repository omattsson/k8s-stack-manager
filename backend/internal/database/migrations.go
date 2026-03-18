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

	// Run migrations
	if err := migrator.MigrateUp(); err != nil {
		return err
	}

	slog.Info("Database migrations completed successfully")
	return nil
}
