package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"backend/internal/database/schema"
	"backend/internal/models"

	"github.com/google/uuid"
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
			// Add composite index on items (idempotent via HasIndex check)
			if !tx.Migrator().HasIndex(&models.Item{}, "idx_items_name_price") {
				if err := tx.Exec("CREATE INDEX idx_items_name_price ON items(name, price)").Error; err != nil {
					return err
				}
			}

			// Add unique index on username (may already exist from uniqueIndex tag)
			if !tx.Migrator().HasIndex(&models.User{}, "idx_users_username") {
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
			// Skip if clusters table doesn't exist yet (created in migration 000009).
			if tx.Migrator().HasTable(&models.Cluster{}) && !tx.Migrator().HasColumn(&models.Cluster{}, "MaxInstancesPerUser") {
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
			if !tx.Migrator().HasIndex(&models.User{}, "idx_users_auth_provider_external_id") {
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

	// Create clusters table
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000009",
		Name:        "create_clusters_table",
		Description: "Create clusters table for multi-cluster support",
		Up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.Cluster{})
		},
		Down: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&models.Cluster{})
		},
	})

	// Create stack_definitions and stack_templates tables
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000010",
		Name:        "create_stack_definitions_and_templates",
		Description: "Create stack_definitions and stack_templates tables",
		Up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&models.StackDefinition{}); err != nil {
				return err
			}
			return tx.AutoMigrate(&models.StackTemplate{})
		},
		Down: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable(&models.StackTemplate{}); err != nil {
				return err
			}
			return tx.Migrator().DropTable(&models.StackDefinition{})
		},
	})

	// Create stack_instances table
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000011",
		Name:        "create_stack_instances",
		Description: "Create stack_instances table",
		Up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.StackInstance{})
		},
		Down: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&models.StackInstance{})
		},
	})

	// Create chart_configs and template_chart_configs tables
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000012",
		Name:        "create_chart_configs",
		Description: "Create chart_configs and template_chart_configs tables",
		Up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&models.ChartConfig{}); err != nil {
				return err
			}
			return tx.AutoMigrate(&models.TemplateChartConfig{})
		},
		Down: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable(&models.TemplateChartConfig{}); err != nil {
				return err
			}
			return tx.Migrator().DropTable(&models.ChartConfig{})
		},
	})

	// Create value_overrides and chart_branch_overrides tables
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000013",
		Name:        "create_value_and_branch_overrides",
		Description: "Create value_overrides and chart_branch_overrides tables",
		Up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&models.ValueOverride{}); err != nil {
				return err
			}
			return tx.AutoMigrate(&models.ChartBranchOverride{})
		},
		Down: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable(&models.ChartBranchOverride{}); err != nil {
				return err
			}
			return tx.Migrator().DropTable(&models.ValueOverride{})
		},
	})

	// Create deployment_logs table
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000014",
		Name:        "create_deployment_logs",
		Description: "Create deployment_logs table for recording deploy/stop/clean operations",
		Up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.DeploymentLog{})
		},
		Down: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&models.DeploymentLog{})
		},
	})

	// Create audit_logs table
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000015",
		Name:        "create_audit_logs",
		Description: "Create audit_logs table for user action auditing",
		Up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.AuditLog{})
		},
		Down: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&models.AuditLog{})
		},
	})

	// Create api_keys table
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000016",
		Name:        "create_api_keys",
		Description: "Create api_keys table for programmatic access",
		Up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.APIKey{})
		},
		Down: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&models.APIKey{})
		},
	})

	// Create shared_values table
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000017",
		Name:        "create_shared_values",
		Description: "Create shared_values table for per-cluster shared Helm values",
		Up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.SharedValues{})
		},
		Down: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&models.SharedValues{})
		},
	})

	// Create cleanup_policies table
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000018",
		Name:        "create_cleanup_policies",
		Description: "Create cleanup_policies table for automated maintenance",
		Up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.CleanupPolicy{})
		},
		Down: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&models.CleanupPolicy{})
		},
	})

	// Create user_favorites table
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000019",
		Name:        "create_user_favorites",
		Description: "Create user_favorites table for user bookmarks",
		Up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.UserFavorite{})
		},
		Down: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&models.UserFavorite{})
		},
	})

	// Add indexes for common query patterns on domain tables
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000020",
		Name:        "add_domain_indexes",
		Description: "Add indexes for common query patterns on domain tables",
		Up: func(tx *gorm.DB) error {
			type idxDef struct {
				table string
				name  string
				sql   string
			}
			indexes := []idxDef{
				{"stack_definitions", "idx_stack_definitions_owner_id", "CREATE INDEX idx_stack_definitions_owner_id ON stack_definitions(owner_id)"},
				{"stack_templates", "idx_stack_templates_owner_id", "CREATE INDEX idx_stack_templates_owner_id ON stack_templates(owner_id)"},
				{"stack_templates", "idx_stack_templates_is_published", "CREATE INDEX idx_stack_templates_is_published ON stack_templates(is_published)"},
				{"stack_instances", "idx_stack_instances_owner_id", "CREATE INDEX idx_stack_instances_owner_id ON stack_instances(owner_id)"},
				{"stack_instances", "idx_stack_instances_status", "CREATE INDEX idx_stack_instances_status ON stack_instances(status)"},
				{"stack_instances", "idx_stack_instances_cluster_id", "CREATE INDEX idx_stack_instances_cluster_id ON stack_instances(cluster_id)"},
				{"stack_instances", "idx_stack_instances_definition_id", "CREATE INDEX idx_stack_instances_definition_id ON stack_instances(stack_definition_id)"},
				{"chart_configs", "idx_chart_configs_definition_id", "CREATE INDEX idx_chart_configs_definition_id ON chart_configs(stack_definition_id)"},
				{"template_chart_configs", "idx_template_chart_configs_template_id", "CREATE INDEX idx_template_chart_configs_template_id ON template_chart_configs(stack_template_id)"},
				{"value_overrides", "idx_value_overrides_instance_id", "CREATE INDEX idx_value_overrides_instance_id ON value_overrides(stack_instance_id)"},
				{"chart_branch_overrides", "idx_chart_branch_overrides_instance_id", "CREATE INDEX idx_chart_branch_overrides_instance_id ON chart_branch_overrides(stack_instance_id)"},
				{"deployment_logs", "idx_deployment_logs_instance_id", "CREATE INDEX idx_deployment_logs_instance_id ON deployment_logs(stack_instance_id)"},
				{"audit_logs", "idx_audit_logs_user_id", "CREATE INDEX idx_audit_logs_user_id ON audit_logs(user_id)"},
				{"audit_logs", "idx_audit_logs_entity", "CREATE INDEX idx_audit_logs_entity ON audit_logs(entity_type, entity_id)"},
				{"audit_logs", "idx_audit_logs_timestamp", "CREATE INDEX idx_audit_logs_timestamp ON audit_logs(timestamp)"},
				{"api_keys", "idx_api_keys_user_id", "CREATE INDEX idx_api_keys_user_id ON api_keys(user_id)"},
				{"api_keys", "idx_api_keys_prefix", "CREATE INDEX idx_api_keys_prefix ON api_keys(prefix)"},
				{"shared_values", "idx_shared_values_cluster_id", "CREATE INDEX idx_shared_values_cluster_id ON shared_values(cluster_id)"},
				{"user_favorites", "idx_user_favorites_user_id", "CREATE INDEX idx_user_favorites_user_id ON user_favorites(user_id)"},
				{"user_favorites", "idx_user_favorites_entity", "CREATE UNIQUE INDEX idx_user_favorites_entity ON user_favorites(user_id, entity_type, entity_id)"},
			}
			for _, idx := range indexes {
				var count int64
				tx.Raw("SELECT COUNT(1) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?", idx.table, idx.name).Scan(&count)
				if count == 0 {
					if err := tx.Exec(idx.sql).Error; err != nil { // #nosec G202 -- SQL from hardcoded struct constants
						return err
					}
				}
			}
			return nil
		},
		Down: func(tx *gorm.DB) error {
			indexes := []struct{ table, name string }{
				{"user_favorites", "idx_user_favorites_entity"},
				{"user_favorites", "idx_user_favorites_user_id"},
				{"shared_values", "idx_shared_values_cluster_id"},
				{"api_keys", "idx_api_keys_prefix"},
				{"api_keys", "idx_api_keys_user_id"},
				{"audit_logs", "idx_audit_logs_timestamp"},
				{"audit_logs", "idx_audit_logs_entity"},
				{"audit_logs", "idx_audit_logs_user_id"},
				{"deployment_logs", "idx_deployment_logs_instance_id"},
				{"chart_branch_overrides", "idx_chart_branch_overrides_instance_id"},
				{"value_overrides", "idx_value_overrides_instance_id"},
				{"template_chart_configs", "idx_template_chart_configs_template_id"},
				{"chart_configs", "idx_chart_configs_definition_id"},
				{"stack_instances", "idx_stack_instances_definition_id"},
				{"stack_instances", "idx_stack_instances_cluster_id"},
				{"stack_instances", "idx_stack_instances_status"},
				{"stack_instances", "idx_stack_instances_owner_id"},
				{"stack_templates", "idx_stack_templates_is_published"},
				{"stack_templates", "idx_stack_templates_owner_id"},
				{"stack_definitions", "idx_stack_definitions_owner_id"},
			}
			for _, idx := range indexes {
				_ = tx.Exec(fmt.Sprintf("DROP INDEX %s ON %s", idx.name, idx.table)).Error // #nosec G202 -- table/index names are hardcoded constants
			}
			return nil
		},
	})

	// Migration 21: Add composite/covering indexes to eliminate full table scans
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000021",
		Name:        "add_query_covering_indexes",
		Description: "Add composite and covering indexes for common query patterns to eliminate full table scans",
		Up: func(tx *gorm.DB) error {
			type idxDef struct {
				table string
				name  string
				sql   string
			}
			indexes := []idxDef{
				// stack_instances: ListPaged() ORDER BY created_at DESC
				{"stack_instances", "idx_stack_instances_created_at", "CREATE INDEX idx_stack_instances_created_at ON stack_instances(created_at DESC)"},
				// stack_instances: status filter + ordering for dashboard queries
				{"stack_instances", "idx_stack_instances_status_created", "CREATE INDEX idx_stack_instances_status_created ON stack_instances(status, created_at DESC)"},
				// stack_definitions: List() ORDER BY created_at
				{"stack_definitions", "idx_stack_definitions_created_at", "CREATE INDEX idx_stack_definitions_created_at ON stack_definitions(created_at DESC)"},
				// deployment_logs: covering index for SummarizeByInstance WHERE instance+action+started_at
				{"deployment_logs", "idx_deployment_logs_instance_action_started", "CREATE INDEX idx_deployment_logs_instance_action_started ON deployment_logs(stack_instance_id, action, started_at, status)"},
				// deployment_logs: covering index for MAX(completed_at) query
				{"deployment_logs", "idx_deployment_logs_instance_started_completed", "CREATE INDEX idx_deployment_logs_instance_started_completed ON deployment_logs(stack_instance_id, started_at, completed_at)"},
				// audit_logs: filtered list with action + timestamp ordering
				{"audit_logs", "idx_audit_logs_action_timestamp", "CREATE INDEX idx_audit_logs_action_timestamp ON audit_logs(action, timestamp DESC)"},
				// stack_templates: List() ORDER BY created_at
				{"stack_templates", "idx_stack_templates_created_at", "CREATE INDEX idx_stack_templates_created_at ON stack_templates(created_at DESC)"},
				// notifications: user inbox query (user + read status + ordering)
				{"notifications", "idx_notifications_user_read_created", "CREATE INDEX idx_notifications_user_read_created ON notifications(user_id, is_read, created_at DESC)"},
			}
			for _, idx := range indexes {
				var count int64
				tx.Raw("SELECT COUNT(1) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?", idx.table, idx.name).Scan(&count)
				if count == 0 {
					if err := tx.Exec(idx.sql).Error; err != nil { // #nosec G202 -- SQL from hardcoded struct constants
						return err
					}
				}
			}
			return nil
		},
		Down: func(tx *gorm.DB) error {
			indexes := []struct{ table, name string }{
				{"notifications", "idx_notifications_user_read_created"},
				{"stack_templates", "idx_stack_templates_created_at"},
				{"audit_logs", "idx_audit_logs_action_timestamp"},
				{"deployment_logs", "idx_deployment_logs_instance_started_completed"},
				{"deployment_logs", "idx_deployment_logs_instance_action_started"},
				{"stack_definitions", "idx_stack_definitions_created_at"},
				{"stack_instances", "idx_stack_instances_status_created"},
				{"stack_instances", "idx_stack_instances_created_at"},
			}
			for _, idx := range indexes {
				_ = tx.Exec(fmt.Sprintf("DROP INDEX %s ON %s", idx.name, idx.table)).Error // #nosec G202 -- table/index names are hardcoded constants
			}
			return nil
		},
	})

	// Migration 22: Add unique constraint on chart_branch_overrides (instance + chart)
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000022",
		Name:        "add_chart_branch_overrides_unique_index",
		Description: "Add unique index on chart_branch_overrides(stack_instance_id, chart_config_id) to support atomic upsert",
		Up: func(tx *gorm.DB) error {
			// Check if index already exists (e.g. created by GORM AutoMigrate from model tag).
			if tx.Migrator().HasIndex(&models.ChartBranchOverride{}, "idx_instance_chart") {
				return nil
			}
			return tx.Exec("CREATE UNIQUE INDEX idx_instance_chart ON chart_branch_overrides(stack_instance_id, chart_config_id)").Error
		},
		Down: func(tx *gorm.DB) error {
			_ = tx.Exec("DROP INDEX idx_instance_chart ON chart_branch_overrides").Error
			return nil
		},
	})

	// Migration 23: Add missing composite indexes and drop redundant single-column indexes
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000023",
		Name:        "optimize_stack_instance_indexes",
		Description: "Add composite indexes for TTL reaper, namespace lookup, quota enforcement, and definition status; drop redundant single-column indexes",
		Up: func(tx *gorm.DB) error {
			type idxDef struct {
				table string
				name  string
				sql   string
			}

			// Fix #3: Composite index for TTL reaper ListExpired query
			// Fix #4: Index on namespace for FindByNamespace (admin orphan detection)
			// Fix #5: Composite indexes for quota enforcement and definition status checks
			newIndexes := []idxDef{
				{"stack_instances", "idx_stack_instances_status_expires", "CREATE INDEX idx_stack_instances_status_expires ON stack_instances(status, expires_at)"},
				{"stack_instances", "idx_stack_instances_namespace", "CREATE INDEX idx_stack_instances_namespace ON stack_instances(namespace)"},
				{"stack_instances", "idx_stack_instances_cluster_owner", "CREATE INDEX idx_stack_instances_cluster_owner ON stack_instances(cluster_id, owner_id)"},
				{"stack_instances", "idx_stack_instances_def_status", "CREATE INDEX idx_stack_instances_def_status ON stack_instances(stack_definition_id, status)"},
			}
			for _, idx := range newIndexes {
				var count int64
				tx.Raw("SELECT COUNT(1) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?", idx.table, idx.name).Scan(&count)
				if count == 0 {
					if err := tx.Exec(idx.sql).Error; err != nil { // #nosec G202 -- SQL from hardcoded struct constants
						return err
					}
				}
			}

			// Fix #11: Drop redundant single-column indexes now covered by composites
			// idx_stack_instances_status is covered by idx_stack_instances_status_created (migration 21)
			//   and idx_stack_instances_status_expires (above)
			// idx_deployment_logs_instance_id is covered by idx_deployment_logs_instance_action_started
			//   and idx_deployment_logs_instance_started_completed (migration 21)
			redundantIndexes := []struct{ table, name string }{
				{"stack_instances", "idx_stack_instances_status"},
				{"deployment_logs", "idx_deployment_logs_instance_id"},
			}
			for _, idx := range redundantIndexes {
				var count int64
				tx.Raw("SELECT COUNT(1) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?", idx.table, idx.name).Scan(&count)
				if count > 0 {
					if err := tx.Exec(fmt.Sprintf("DROP INDEX %s ON %s", idx.name, idx.table)).Error; err != nil { // #nosec G202 -- table/index names are hardcoded constants
						return err
					}
				}
			}

			return nil
		},
		Down: func(tx *gorm.DB) error {
			// Re-create the single-column indexes that were dropped
			restoreIndexes := []struct {
				table, name, sql string
			}{
				{"stack_instances", "idx_stack_instances_status", "CREATE INDEX idx_stack_instances_status ON stack_instances(status)"},
				{"deployment_logs", "idx_deployment_logs_instance_id", "CREATE INDEX idx_deployment_logs_instance_id ON deployment_logs(stack_instance_id)"},
			}
			for _, idx := range restoreIndexes {
				var count int64
				tx.Raw("SELECT COUNT(1) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?", idx.table, idx.name).Scan(&count)
				if count == 0 {
					if err := tx.Exec(idx.sql).Error; err != nil { // #nosec G202 -- SQL from hardcoded struct constants
						return err
					}
				}
			}

			// Drop the new composite indexes
			dropIndexes := []struct{ table, name string }{
				{"stack_instances", "idx_stack_instances_def_status"},
				{"stack_instances", "idx_stack_instances_cluster_owner"},
				{"stack_instances", "idx_stack_instances_namespace"},
				{"stack_instances", "idx_stack_instances_status_expires"},
			}
			for _, idx := range dropIndexes {
				_ = tx.Exec(fmt.Sprintf("DROP INDEX %s ON %s", idx.name, idx.table)).Error // #nosec G202 -- table/index names are hardcoded constants
			}
			return nil
		},
	})

	// Migration 24: Add unique constraint on value_overrides (instance + chart)
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000024",
		Name:        "add_value_overrides_unique_index",
		Description: "Add unique index on value_overrides(stack_instance_id, chart_config_id) to enforce one override per chart per instance",
		Up: func(tx *gorm.DB) error {
			if tx.Migrator().HasIndex(&models.ValueOverride{}, "idx_override_instance_chart") {
				return nil
			}
			return tx.Exec("CREATE UNIQUE INDEX idx_override_instance_chart ON value_overrides(stack_instance_id, chart_config_id)").Error
		},
		Down: func(tx *gorm.DB) error {
			_ = tx.Exec("DROP INDEX idx_override_instance_chart ON value_overrides").Error
			return nil
		},
	})

	// Migration 25: Add unique constraint on user_favorites (user + entity type + entity)
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000025",
		Name:        "add_user_favorites_unique_index",
		Description: "Add unique constraint on user_id, entity_type, entity_id for user_favorites",
		Up: func(tx *gorm.DB) error {
			if tx.Migrator().HasIndex(&models.UserFavorite{}, "idx_user_favorites_unique") {
				return nil
			}
			return tx.Exec(`CREATE UNIQUE INDEX idx_user_favorites_unique ON user_favorites (user_id, entity_type, entity_id)`).Error
		},
		Down: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX idx_user_favorites_unique ON user_favorites`).Error
		},
	})

	// Migration 26: Add (action, started_at) index for CountByAction analytics query
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000026",
		Name:        "add_deployment_logs_action_started_index",
		Description: "Add index on (action, started_at) for CountByAction analytics query",
		Up: func(tx *gorm.DB) error {
			if tx.Migrator().HasIndex(&models.DeploymentLog{}, "idx_deployment_logs_action_started") {
				return nil
			}
			return tx.Exec("CREATE INDEX idx_deployment_logs_action_started ON deployment_logs (action, started_at)").Error
		},
		Down: func(tx *gorm.DB) error {
			return tx.Exec("DROP INDEX idx_deployment_logs_action_started ON deployment_logs").Error
		},
	})

	// Migration 27: Add last_deployed_values column to stack_instances
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000027",
		Name:        "add_last_deployed_values_to_stack_instances",
		Description: "Add last_deployed_values LONGTEXT column to stack_instances for deployment diff preview",
		Up: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&models.StackInstance{}, "LastDeployedValues") {
				return nil
			}
			return tx.Migrator().AddColumn(&models.StackInstance{}, "LastDeployedValues")
		},
		Down: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&models.StackInstance{}, "LastDeployedValues") {
				return tx.Migrator().DropColumn(&models.StackInstance{}, "LastDeployedValues")
			}
			return nil
		},
	})

	// Migration 28: Alter last_deployed_values from TEXT to LONGTEXT (conditional)
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000028",
		Name:        "alter_last_deployed_values_to_longtext",
		Description: "Change last_deployed_values column from TEXT to LONGTEXT for large merged values",
		Up: func(tx *gorm.DB) error {
			dialector := tx.Dialector.Name()
			if dialector != "mysql" {
				return nil
			}
			// MySQL/MariaDB: check current column type and alter if needed.
			var columnType string
			row := tx.Raw("SELECT COLUMN_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'stack_instances' AND COLUMN_NAME = 'last_deployed_values'").Row()
			if err := row.Scan(&columnType); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil // column doesn't exist yet, migration 27 will handle it
				}
				return fmt.Errorf("failed to check column type: %w", err)
			}
			if columnType == "longtext" {
				return nil // already longtext
			}
			return tx.Exec("ALTER TABLE stack_instances MODIFY last_deployed_values LONGTEXT").Error // #nosec G202
		},
		Down: func(tx *gorm.DB) error {
			if tx.Dialector.Name() != "mysql" {
				return nil
			}
			if tx.Migrator().HasColumn(&models.StackInstance{}, "LastDeployedValues") {
				return tx.Exec("ALTER TABLE stack_instances MODIFY last_deployed_values TEXT").Error // #nosec G202
			}
			return nil
		},
	})

	// Migration 29: Add use_in_cluster column to clusters table
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000029",
		Name:        "add_use_in_cluster_to_clusters",
		Description: "Add use_in_cluster boolean column to clusters table for in-cluster (service account) auth",
		Up: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&models.Cluster{}, "UseInCluster") {
				return nil
			}
			return tx.Migrator().AddColumn(&models.Cluster{}, "UseInCluster")
		},
		Down: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&models.Cluster{}, "UseInCluster") {
				return tx.Migrator().DropColumn(&models.Cluster{}, "UseInCluster")
			}
			return nil
		},
	})

	// Migration 30: Create refresh_tokens table for JWT refresh token support
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000030",
		Name:        "create_refresh_tokens_table",
		Description: "Create refresh_tokens table for server-side refresh token storage",
		Up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.RefreshToken{})
		},
		Down: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&models.RefreshToken{})
		},
	})

	migrator.AddMigration(schema.Migration{
		Version:     "20231201000031",
		Name:        "add_build_pipeline_id_to_chart_configs",
		Description: "Add build_pipeline_id column to chart_configs and template_chart_configs for CI pipeline trigger",
		Up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&models.ChartConfig{}); err != nil {
				return err
			}
			return tx.AutoMigrate(&models.TemplateChartConfig{})
		},
		Down: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&models.ChartConfig{}, "build_pipeline_id") {
				if err := tx.Migrator().DropColumn(&models.ChartConfig{}, "build_pipeline_id"); err != nil {
					return err
				}
			}
			if tx.Migrator().HasColumn(&models.TemplateChartConfig{}, "build_pipeline_id") {
				return tx.Migrator().DropColumn(&models.TemplateChartConfig{}, "build_pipeline_id")
			}
			return nil
		},
	})

	migrator.AddMigration(schema.Migration{
		Version:     "20231201000032",
		Name:        "add_deployment_log_rollback_columns",
		Description: "Add values_snapshot and target_log_id columns to deployment_logs for rollback support",
		Up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.DeploymentLog{})
		},
		Down: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&models.DeploymentLog{}, "values_snapshot") {
				if err := tx.Migrator().DropColumn(&models.DeploymentLog{}, "values_snapshot"); err != nil {
					return err
				}
			}
			if tx.Migrator().HasColumn(&models.DeploymentLog{}, "target_log_id") {
				return tx.Migrator().DropColumn(&models.DeploymentLog{}, "target_log_id")
			}
			return nil
		},
	})

	// Migration 33: Add index on stack_instances.name for name-based lookup
	migrator.AddMigration(schema.Migration{
		Version:     "20231201000033",
		Name:        "add_stack_instances_name_index",
		Description: "Add index on stack_instances(name) for FindByName queries used by CLI name resolution",
		Up: func(tx *gorm.DB) error {
			var count int64
			tx.Raw("SELECT COUNT(1) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?",
				"stack_instances", "idx_stack_instances_name").Scan(&count)
			if count > 0 {
				return nil
			}
			return tx.Exec("CREATE INDEX idx_stack_instances_name ON stack_instances(name)").Error
		},
		Down: func(tx *gorm.DB) error {
			_ = tx.Exec("DROP INDEX idx_stack_instances_name ON stack_instances").Error
			return nil
		},
	})

	// Migration 34: Add container registry fields to clusters for auto pull secret provisioning
	migrator.AddMigration(schema.Migration{
		Version:     "20260422000034",
		Name:        "add_registry_fields_to_clusters",
		Description: "Add registry_url, registry_username, registry_password, and image_pull_secret_name columns to clusters table for automatic image pull secret provisioning",
		Up: func(tx *gorm.DB) error {
			cols := []struct {
				field string
				sql   string
			}{
				{"RegistryURL", "ALTER TABLE clusters ADD COLUMN registry_url VARCHAR(500) NOT NULL DEFAULT ''"},
				{"RegistryUsername", "ALTER TABLE clusters ADD COLUMN registry_username VARCHAR(255) NOT NULL DEFAULT ''"},
				{"RegistryPassword", "ALTER TABLE clusters ADD COLUMN registry_password TEXT"},
				{"ImagePullSecretName", "ALTER TABLE clusters ADD COLUMN image_pull_secret_name VARCHAR(255) NOT NULL DEFAULT ''"},
			}
			for _, col := range cols {
				if tx.Migrator().HasColumn(&models.Cluster{}, col.field) {
					continue
				}
				if tx.Dialector.Name() == "mysql" {
					if err := tx.Exec(col.sql).Error; err != nil { // #nosec G202 -- SQL from hardcoded struct constants
						return err
					}
				} else {
					if err := tx.Migrator().AddColumn(&models.Cluster{}, col.field); err != nil {
						return err
					}
				}
			}
			return nil
		},
		Down: func(tx *gorm.DB) error {
			for _, col := range []string{"ImagePullSecretName", "RegistryPassword", "RegistryUsername", "RegistryURL"} {
				if tx.Migrator().HasColumn(&models.Cluster{}, col) {
					if err := tx.Migrator().DropColumn(&models.Cluster{}, col); err != nil {
						return err
					}
				}
			}
			return nil
		},
	})

	migrator.AddMigration(schema.Migration{
		Version:     "20260423000035",
		Name:        "add_channel_to_notification_preferences",
		Description: "Add channel column to notification_preferences for routing notifications to different delivery channels",
		Up: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&models.NotificationPreference{}, "Channel") {
				return nil
			}
			if tx.Dialector.Name() == "mysql" {
				return tx.Exec("ALTER TABLE notification_preferences ADD COLUMN channel VARCHAR(20) NOT NULL DEFAULT 'in_app'").Error // #nosec G202
			}
			return tx.Migrator().AddColumn(&models.NotificationPreference{}, "Channel")
		},
		Down: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&models.NotificationPreference{}, "Channel") {
				return tx.Migrator().DropColumn(&models.NotificationPreference{}, "Channel")
			}
			return nil
		},
	})

	// Migration 36: Add index on deployment_logs(started_at DESC) for dashboard global query
	migrator.AddMigration(schema.Migration{
		Version:     "20260507000036",
		Name:        "add_deployment_logs_started_at_index",
		Description: "Add index on deployment_logs(started_at DESC) for ListRecentGlobal dashboard query",
		Up: func(tx *gorm.DB) error {
			var count int64
			tx.Raw("SELECT COUNT(1) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?",
				"deployment_logs", "idx_deployment_logs_started_at").Scan(&count)
			if count > 0 {
				return nil
			}
			return tx.Exec("CREATE INDEX idx_deployment_logs_started_at ON deployment_logs(started_at DESC)").Error
		},
		Down: func(tx *gorm.DB) error {
			_ = tx.Exec("DROP INDEX idx_deployment_logs_started_at ON deployment_logs").Error
			return nil
		},
	})

	migrator.AddMigration(schema.Migration{
		Version:     "20260507000037",
		Name:        "create_session_entries",
		Description: "Create session_entries table for token blocklist and OIDC state persistence",
		Up: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE TABLE IF NOT EXISTS session_entries (
				entry_key  VARCHAR(255) NOT NULL,
				kind       VARCHAR(20)  NOT NULL,
				data       TEXT,
				expires_at BIGINT       NOT NULL,
				PRIMARY KEY (entry_key, kind)
			)`).Error; err != nil {
				return err
			}
			var count int64
			tx.Raw("SELECT COUNT(1) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?",
				"session_entries", "idx_session_entries_expires_at").Scan(&count)
			if count == 0 {
				return tx.Exec("CREATE INDEX idx_session_entries_expires_at ON session_entries (expires_at)").Error
			}
			return nil
		},
		Down: func(tx *gorm.DB) error {
			return tx.Exec("DROP TABLE IF EXISTS session_entries").Error
		},
	})

	migrator.AddMigration(schema.Migration{
		Version:     "20260507000038",
		Name:        "add_users_disabled",
		Description: "Add disabled column to users table for account deactivation",
		Up: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&models.User{}, "disabled") {
				return nil
			}
			return tx.Exec("ALTER TABLE users ADD COLUMN disabled TINYINT(1) NOT NULL DEFAULT 0").Error
		},
		Down: func(tx *gorm.DB) error {
			if !tx.Migrator().HasColumn(&models.User{}, "disabled") {
				return nil
			}
			return tx.Exec("ALTER TABLE users DROP COLUMN disabled").Error
		},
	})

	migrator.AddMigration(schema.Migration{
		Version:     "20260508000039",
		Name:        "add_users_service_account",
		Description: "Add service_account column to users table for break-glass local login when OIDC is enabled",
		Up: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn(&models.User{}, "service_account") {
				return nil
			}
			if err := tx.Exec("ALTER TABLE users ADD COLUMN service_account TINYINT(1) NOT NULL DEFAULT 0").Error; err != nil {
				return err
			}
			return tx.Exec("UPDATE users SET service_account = 1 WHERE auth_provider = 'local' AND role = 'admin'").Error
		},
		Down: func(tx *gorm.DB) error {
			if !tx.Migrator().HasColumn(&models.User{}, "service_account") {
				return nil
			}
			return tx.Exec("ALTER TABLE users DROP COLUMN service_account").Error
		},
	})

	// Migration 40: Create notification channel tables for webhook-based notifications
	migrator.AddMigration(schema.Migration{
		Version:     "20260510000040",
		Name:        "create_notification_channel_tables",
		Description: "Create notification_channels, notification_channel_subscriptions, and notification_delivery_logs tables",
		Up: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable("notification_channels") {
				if err := tx.Exec(`CREATE TABLE notification_channels (
					id          VARCHAR(36)   NOT NULL PRIMARY KEY,
					name        VARCHAR(255)  NOT NULL UNIQUE,
					webhook_url VARCHAR(2048) NOT NULL,
					secret      TEXT,
					enabled     TINYINT(1)    NOT NULL DEFAULT 1,
					created_at  DATETIME      NULL,
					updated_at  DATETIME      NULL
				)`).Error; err != nil {
					return err
				}
			}
			if !tx.Migrator().HasTable("notification_channel_subscriptions") {
				if err := tx.Exec(`CREATE TABLE notification_channel_subscriptions (
					id         VARCHAR(36) NOT NULL PRIMARY KEY,
					channel_id VARCHAR(36) NOT NULL,
					event_type VARCHAR(50) NOT NULL,
					UNIQUE (channel_id, event_type)
				)`).Error; err != nil {
					return err
				}
				if err := tx.Exec("CREATE INDEX idx_ncs_event_type ON notification_channel_subscriptions (event_type)").Error; err != nil {
					return err
				}
			}
			if !tx.Migrator().HasTable("notification_delivery_logs") {
				if err := tx.Exec(`CREATE TABLE notification_delivery_logs (
					id            VARCHAR(36)  NOT NULL PRIMARY KEY,
					channel_id    VARCHAR(36)  NOT NULL,
					channel_name  VARCHAR(255) NOT NULL DEFAULT '',
					event_type    VARCHAR(50)  NOT NULL DEFAULT '',
					status        VARCHAR(20)  NOT NULL,
					status_code   INT          NOT NULL DEFAULT 0,
					error_message TEXT,
					created_at    DATETIME     NULL
				)`).Error; err != nil {
					return err
				}
				if err := tx.Exec("CREATE INDEX idx_ndl_channel_id ON notification_delivery_logs (channel_id)").Error; err != nil {
					return err
				}
				if err := tx.Exec("CREATE INDEX idx_ndl_created_at ON notification_delivery_logs (created_at)").Error; err != nil {
					return err
				}
			}
			return nil
		},
		Down: func(tx *gorm.DB) error {
			_ = tx.Exec("DROP TABLE IF EXISTS notification_delivery_logs").Error
			_ = tx.Exec("DROP TABLE IF EXISTS notification_channel_subscriptions").Error
			_ = tx.Exec("DROP TABLE IF EXISTS notification_channels").Error
			return nil
		},
	})

	// Migration 41: Session family columns on refresh_tokens (absolute session
	// lifetime, reuse grace window, idle timeout from the last request).
	migrator.AddMigration(refreshTokenSessionFamilyMigration())

	// Migration 42: Remove empty value override rows. A PUT with empty values
	// now deletes the override; older versions stored an empty row instead.
	migrator.AddMigration(removeEmptyValueOverridesMigration())

	// Migration 43: stack lifecycle columns. stopped_at on stack_instances
	// (cleanup condition stopped_days), owner_instance_id on
	// stack_definitions (quick deploy definitions) and chart_versions on
	// deployment_logs (rollback to a deploy) and branch on deployment_logs.
	migrator.AddMigration(stackLifecycleColumnsMigration())

	// Migration 44: release the working copy of published (or referenced)
	// templates whose latest snapshot is missing, legacy or stale. Use
	// Template, Quick Deploy, upgrades and locked values now read the latest
	// snapshot, never the working copy.
	migrator.AddMigration(backfillPublishedTemplateSnapshotsMigration())

	// Migration 45: stack_instances.expiry_warned_at. The TTL expiry warner
	// keeps its "already warned" state in the database, so a new leader
	// replica does not warn again.
	migrator.AddMigration(expiryWarnedAtMigration())

	// Migration 46: ws_events. Each replica writes its WebSocket messages
	// there; the other replicas poll the table and deliver them to their
	// clients (WS_FANOUT_ENABLED).
	migrator.AddMigration(wsEventsMigration())

	// Migration 47: audit_logs. Rename the plural, route-derived entity
	// types of old CRUD entries to the singular names of the audit route
	// table, and widen entity_id to 63 (namespace names are longer than a
	// UUID).
	migrator.AddMigration(auditEntityTypesMigration())

	// Migration 48: stack_definitions (owner_id, name) index for the owner
	// and name list filters and the per-owner name check.
	migrator.AddMigration(definitionOwnerNameIndexMigration())

	// Migration 49: deployment_logs.user_id (who started a deploy) for the
	// per-user analytics; older deploy logs get the instance owner.
	migrator.AddMigration(deployLogUserIDMigration())

	// Migration 50: users.role index for the guarded admin changes, which
	// lock the admin rows (role = 'admin').
	migrator.AddMigration(userRoleIndexMigration())

	// Migration 51: stack_instances.post_deploy_hook_until (blocking
	// post-deploy hooks run; the k8s status watcher skips the instance).
	migrator.AddMigration(postDeployHookUntilMigration())

	// Run migrations
	if err := migrator.MigrateUp(); err != nil {
		return err
	}

	slog.Info("Database migrations completed successfully")
	return nil
}

// refreshTokenSessionFamilyMigration is migration 41. It is a function so
// tests can run its Down step.
func refreshTokenSessionFamilyMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261008000041",
		Name:        "add_refresh_token_session_family",
		Description: "Add family_id, session_started_at and rotated_at to refresh_tokens and backfill legacy rows",
		Up: func(tx *gorm.DB) error {
			m := tx.Migrator()
			for _, field := range []string{"FamilyID", "SessionStartedAt", "RotatedAt"} {
				if !m.HasColumn(&models.RefreshToken{}, field) {
					if err := m.AddColumn(&models.RefreshToken{}, field); err != nil {
						return err
					}
				}
			}
			if !m.HasIndex(&models.RefreshToken{}, "FamilyID") {
				if err := m.CreateIndex(&models.RefreshToken{}, "FamilyID"); err != nil {
					return err
				}
			}
			// A legacy token is its own family; its session started when it was created.
			if err := tx.Exec("UPDATE refresh_tokens SET family_id = id WHERE family_id IS NULL OR family_id = ''").Error; err != nil {
				return err
			}
			return tx.Exec("UPDATE refresh_tokens SET session_started_at = created_at WHERE session_started_at IS NULL").Error
		},
		Down: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if m.HasIndex(&models.RefreshToken{}, "FamilyID") {
				if err := m.DropIndex(&models.RefreshToken{}, "FamilyID"); err != nil {
					return err
				}
			}
			for _, field := range []string{"RotatedAt", "SessionStartedAt", "FamilyID"} {
				if m.HasColumn(&models.RefreshToken{}, field) {
					if err := m.DropColumn(&models.RefreshToken{}, field); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}

// removeEmptyValueOverridesMigration is migration 42. It deletes value
// override rows whose values are empty or whitespace only. Such rows have no
// effect on the rendered values but show up in GET /overrides. Down is a
// no-op: the deleted rows carried no data.
func removeEmptyValueOverridesMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261008000042",
		Name:        "remove_empty_value_overrides",
		Description: "Delete value_overrides rows with empty or whitespace-only values",
		Up: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable(&models.ValueOverride{}) {
				return nil
			}
			return tx.Exec(
				"DELETE FROM value_overrides WHERE `values` IS NULL OR TRIM(REPLACE(REPLACE(REPLACE(`values`, ?, ''), ?, ''), ?, '')) = ''",
				"\n", "\r", "\t",
			).Error
		},
		Down: func(_ *gorm.DB) error {
			return nil
		},
	}
}

// stackLifecycleColumnsMigration is migration 43. It adds
// stack_instances.stopped_at, stack_definitions.owner_instance_id (indexed)
// and deployment_logs.chart_versions and branch. Existing stopped instances get
// stopped_at from their last successful stop log, or from updated_at when no
// such log exists. It is a function so tests can run its Down step.
func stackLifecycleColumnsMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261009000043",
		Name:        "add_stack_lifecycle_columns",
		Description: "Add stopped_at to stack_instances, owner_instance_id to stack_definitions, chart_versions and branch to deployment_logs; backfill stopped_at",
		Up: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if m.HasTable(&models.StackInstance{}) && !m.HasColumn(&models.StackInstance{}, "StoppedAt") {
				if err := m.AddColumn(&models.StackInstance{}, "StoppedAt"); err != nil {
					return err
				}
			}
			if m.HasTable(&models.StackDefinition{}) {
				if !m.HasColumn(&models.StackDefinition{}, "OwnerInstanceID") {
					if err := m.AddColumn(&models.StackDefinition{}, "OwnerInstanceID"); err != nil {
						return err
					}
				}
				if !m.HasIndex(&models.StackDefinition{}, "OwnerInstanceID") {
					if err := m.CreateIndex(&models.StackDefinition{}, "OwnerInstanceID"); err != nil {
						return err
					}
				}
			}
			for _, field := range []string{"ChartVersions", "Branch"} {
				if m.HasTable(&models.DeploymentLog{}) && !m.HasColumn(&models.DeploymentLog{}, field) {
					if err := m.AddColumn(&models.DeploymentLog{}, field); err != nil {
						return err
					}
				}
			}
			if !m.HasTable(&models.StackInstance{}) {
				return nil
			}
			if m.HasTable(&models.DeploymentLog{}) {
				if err := tx.Exec(
					`UPDATE stack_instances SET stopped_at = (
						SELECT MAX(dl.completed_at) FROM deployment_logs dl
						WHERE dl.stack_instance_id = stack_instances.id AND dl.action = ? AND dl.status = ?
					) WHERE status = ? AND stopped_at IS NULL`,
					models.DeployActionStop, models.DeployLogSuccess, models.StackStatusStopped,
				).Error; err != nil {
					return err
				}
			}
			return tx.Exec(
				"UPDATE stack_instances SET stopped_at = updated_at WHERE status = ? AND stopped_at IS NULL",
				models.StackStatusStopped,
			).Error
		},
		Down: func(tx *gorm.DB) error {
			m := tx.Migrator()
			for _, field := range []string{"Branch", "ChartVersions"} {
				if m.HasColumn(&models.DeploymentLog{}, field) {
					if err := m.DropColumn(&models.DeploymentLog{}, field); err != nil {
						return err
					}
				}
			}
			if m.HasIndex(&models.StackDefinition{}, "OwnerInstanceID") {
				if err := m.DropIndex(&models.StackDefinition{}, "OwnerInstanceID"); err != nil {
					return err
				}
			}
			if m.HasColumn(&models.StackDefinition{}, "OwnerInstanceID") {
				if err := m.DropColumn(&models.StackDefinition{}, "OwnerInstanceID"); err != nil {
					return err
				}
			}
			if m.HasColumn(&models.StackInstance{}, "StoppedAt") {
				return m.DropColumn(&models.StackInstance{}, "StoppedAt")
			}
			return nil
		},
	}
}

// backfillSnapshotChangeSummary marks the snapshots that migration 44 creates.
const backfillSnapshotChangeSummary = "Backfill: release of the working copy at upgrade"

// backfillDefaultVersion is the version of a migration 44 snapshot when the
// template has no version string; the working copy takes it too.
const backfillDefaultVersion = "1.0.0"

// backfillPublishedTemplateSnapshotsMigration is migration 44. Before the
// "draft and release" model, Use Template and Quick Deploy read the working
// copy of a template. Now they (and the deploy-time locked values) read the
// latest snapshot. So that nobody gets stale content after the upgrade, this
// migration stores the working copy (template fields + template charts) as a
// new snapshot, with the template version string and the owner as author,
// for every template where:
//   - the template is published and has no snapshot, or
//   - the template is published or referenced by a stack definition
//     (source_template_id), has a snapshot, and the latest snapshot uses the
//     legacy format (schema_version < 1) or differs from the working copy.
//
// A template without a version string gets backfillDefaultVersion (snapshot
// and working copy). Duplicate version strings are accepted here. The migration is idempotent:
// after it, the latest snapshot equals the working copy. Down is a no-op: the
// snapshots stay valid history.
func backfillPublishedTemplateSnapshotsMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261009000044",
		Name:        "release_template_working_copies",
		Description: "Snapshot the working copy of published or referenced templates whose latest snapshot is missing, legacy or stale",
		Up:          releaseTemplateWorkingCopies,
		Down: func(_ *gorm.DB) error {
			return nil
		},
	}
}

// releaseTemplateWorkingCopies is the Up step of migration 44.
func releaseTemplateWorkingCopies(tx *gorm.DB) error {
	m := tx.Migrator()
	if !m.HasTable(&models.StackTemplate{}) || !m.HasTable(&models.TemplateVersion{}) || !m.HasTable(&models.TemplateChartConfig{}) {
		return nil
	}

	referenced := make(map[string]bool)
	if m.HasTable(&models.StackDefinition{}) {
		var ids []string
		if err := tx.Model(&models.StackDefinition{}).
			Where("source_template_id IS NOT NULL AND source_template_id <> ''").
			Distinct("source_template_id").
			Pluck("source_template_id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			referenced[id] = true
		}
	}

	var templates []models.StackTemplate
	if err := tx.Order("id").Find(&templates).Error; err != nil {
		return err
	}
	for i := range templates {
		tmpl := &templates[i]
		if !tmpl.IsPublished && !referenced[tmpl.ID] {
			continue
		}

		var latest models.TemplateVersion
		hasSnapshot := true
		if err := tx.Where("template_id = ?", tmpl.ID).Order("created_at DESC, id DESC").First(&latest).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			hasSnapshot = false
		}
		if !hasSnapshot && !tmpl.IsPublished {
			continue // never released: readers fall back to the working copy
		}

		var charts []models.TemplateChartConfig
		if err := tx.Where("stack_template_id = ?", tmpl.ID).Order("deploy_order ASC").Find(&charts).Error; err != nil {
			return err
		}
		// A release needs a version string: an empty one becomes the default.
		emptyVersion := strings.TrimSpace(tmpl.Version) == ""
		if emptyVersion {
			tmpl.Version = backfillDefaultVersion
		}
		working := models.NewTemplateSnapshot(tmpl, charts)

		if hasSnapshot {
			var current models.TemplateSnapshot
			if err := json.Unmarshal([]byte(latest.Snapshot), &current); err == nil &&
				current.SchemaVersion >= models.TemplateSnapshotSchemaVersion &&
				models.SameTemplateContent(current, working) {
				continue // the latest snapshot is the working copy already
			}
		}

		snapshot, err := json.Marshal(working)
		if err != nil {
			return fmt.Errorf("marshal snapshot of template %s: %w", tmpl.ID, err)
		}
		createdAt := time.Now().UTC()
		if hasSnapshot && !createdAt.After(latest.CreatedAt) {
			createdAt = latest.CreatedAt.Add(time.Millisecond)
		}
		version := models.TemplateVersion{
			ID:            uuid.New().String(),
			TemplateID:    tmpl.ID,
			Version:       tmpl.Version,
			Snapshot:      string(snapshot),
			ChangeSummary: backfillSnapshotChangeSummary,
			CreatedBy:     tmpl.OwnerID,
			CreatedAt:     createdAt,
		}
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		if emptyVersion {
			if err := tx.Model(&models.StackTemplate{}).Where("id = ?", tmpl.ID).Update("version", tmpl.Version).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// expiryWarnedAtMigration is migration 45. It adds the nullable
// stack_instances.expiry_warned_at column. Before, the expiry warner kept
// this state in process memory, so every replica (and every restart) sent the
// warning again. No backfill: an instance in its warning window at upgrade
// time gets one more warning, as after a restart before. It is a function so
// tests can run its Down step.
func expiryWarnedAtMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261009000045",
		Name:        "add_stack_instance_expiry_warned_at",
		Description: "Add expiry_warned_at to stack_instances (TTL expiry warning sent for the current expires_at)",
		Up: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if m.HasTable(&models.StackInstance{}) && !m.HasColumn(&models.StackInstance{}, "ExpiryWarnedAt") {
				return m.AddColumn(&models.StackInstance{}, "ExpiryWarnedAt")
			}
			return nil
		},
		Down: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if m.HasColumn(&models.StackInstance{}, "ExpiryWarnedAt") {
				return m.DropColumn(&models.StackInstance{}, "ExpiryWarnedAt")
			}
			return nil
		},
	}
}

// wsEventsMigration is migration 46. It creates the ws_events table for the
// WebSocket fan-out between replicas: an auto-increment id (the read position
// of the pollers) and an index on created_at for the cleanup. It is a
// function so tests can run its Down step.
func wsEventsMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261009000046",
		Name:        "create_ws_events",
		Description: "Create ws_events (WebSocket fan-out between replicas)",
		Up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&models.WSEvent{})
		},
		Down: func(tx *gorm.DB) error {
			if tx.Migrator().HasTable(&models.WSEvent{}) {
				return tx.Migrator().DropTable(&models.WSEvent{})
			}
			return nil
		},
	}
}

// auditEntityTypeRenames maps the plural entity types that the audit
// middleware derived from the route before the audit route table to the
// singular names it writes now (see middleware.KnownAuditEntityTypes). Only
// plain CRUD entries used these names.
var auditEntityTypeRenames = map[string]string{
	"api_keys":              "api_key",
	"branches":              "branch_override",
	"cleanup_policies":      "cleanup_policy",
	"clusters":              "cluster",
	"favorites":             "favorite",
	"notification_channels": "notification_channel",
	"orphaned_namespaces":   "namespace",
	"preferences":           "notification_preference",
	"quota_overrides":       "quota_override",
	"quotas":                "quota",
	"subscriptions":         "notification_subscription",
}

// auditEntityTypesMigration is migration 47. It renames the plural entity
// types of old audit entries (auditEntityTypeRenames) and widens
// audit_logs.entity_id from 36 to 63 characters (an in-place ALTER on
// MySQL; 63 is the RFC 1123 label maximum, for namespace names). Entries of non-CRUD operations
// written before the route table (for example "create | deploy") keep their
// values: the old entry does not tell the operation apart reliably. Down is a
// no-op: new entries use the singular names too, and a narrower entity_id
// could cut values.
func auditEntityTypesMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261009000047",
		Name:        "audit_logs_singular_entity_types",
		Description: "Rename plural audit entity types to singular names and widen audit_logs.entity_id to 63",
		Up: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable(&models.AuditLog{}) {
				return nil
			}
			if err := tx.Migrator().AlterColumn(&models.AuditLog{}, "EntityID"); err != nil {
				return fmt.Errorf("widening audit_logs.entity_id: %w", err)
			}
			for from, to := range auditEntityTypeRenames {
				if err := tx.Model(&models.AuditLog{}).
					Where("entity_type = ?", from).
					Update("entity_type", to).Error; err != nil {
					return fmt.Errorf("renaming audit entity type %q: %w", from, err)
				}
			}
			return nil
		},
		Down: func(_ *gorm.DB) error {
			return nil
		},
	}
}

// definitionOwnerNameIndexName is the index that migration 48 creates.
const definitionOwnerNameIndexName = "idx_stack_definitions_owner_name"

// definitionOwnerNameIndexMigration is migration 48. It adds an index on
// stack_definitions (owner_id, name) for the owner and name filters of
// GET /stack-definitions and for the per-owner name check. Up and Down are
// idempotent: they check whether the index exists first. It is a function so
// tests can run its Down step.
func definitionOwnerNameIndexMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261009000048",
		Name:        "add_stack_definitions_owner_name_index",
		Description: "Add index on stack_definitions (owner_id, name) for the owner and name list filters",
		Up: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if !m.HasTable(&models.StackDefinition{}) || m.HasIndex(&models.StackDefinition{}, definitionOwnerNameIndexName) {
				return nil
			}
			return tx.Exec("CREATE INDEX " + definitionOwnerNameIndexName + " ON stack_definitions(owner_id, name)").Error // #nosec G202 -- index name is a constant
		},
		Down: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if m.HasIndex(&models.StackDefinition{}, definitionOwnerNameIndexName) {
				return m.DropIndex(&models.StackDefinition{}, definitionOwnerNameIndexName)
			}
			return nil
		},
	}
}

// deployLogUserIndexName is the composite index that migration 49 creates.
// It covers SummarizeByUsers (filter user_id and action, read the times and
// the status) without a table read.
const deployLogUserIndexName = "idx_deployment_logs_user_action"

// deployLogUserBackfillBatch is the number of deploy logs that one backfill
// UPDATE of migration 49 changes.
const deployLogUserBackfillBatch = 1000

// deployLogUserIDMigration is migration 49. It adds deployment_logs.user_id
// and a composite index on (user_id, action, started_at, completed_at,
// status). Older deploy logs get the owner of their instance when the
// instance still exists (the best value available; a deploy by another user
// counts for the owner). Logs of deleted instances stay without a user. The
// backfill runs in batches, so no statement locks the whole table. Up and
// Down are idempotent. It is a function so tests can run its Down step.
func deployLogUserIDMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261009000049",
		Name:        "add_deployment_logs_user_id",
		Description: "Add deployment_logs.user_id with a composite index for per-user deploy analytics and fill it from the instance owner",
		Up: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if !m.HasTable(&models.DeploymentLog{}) {
				return nil
			}
			if !m.HasColumn(&models.DeploymentLog{}, "UserID") {
				if err := m.AddColumn(&models.DeploymentLog{}, "UserID"); err != nil {
					return err
				}
			}
			if !m.HasIndex(&models.DeploymentLog{}, deployLogUserIndexName) {
				if err := tx.Exec("CREATE INDEX " + deployLogUserIndexName + // #nosec G202 -- index name is a constant
					" ON deployment_logs(user_id, action, started_at, completed_at, status)").Error; err != nil {
					return err
				}
			}
			if !m.HasTable(&models.StackInstance{}) {
				return nil
			}
			return backfillDeployLogUsers(tx, deployLogUserBackfillBatch)
		},
		Down: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if m.HasIndex(&models.DeploymentLog{}, deployLogUserIndexName) {
				if err := m.DropIndex(&models.DeploymentLog{}, deployLogUserIndexName); err != nil {
					return err
				}
			}
			if m.HasColumn(&models.DeploymentLog{}, "UserID") {
				return m.DropColumn(&models.DeploymentLog{}, "UserID")
			}
			return nil
		},
	}
}

// backfillDeployLogUsers sets user_id of deploy logs without a user to the
// owner of their instance, batchSize rows per UPDATE, until no row is left.
// It selects the IDs first and updates by ID: MySQL does not allow LIMIT in
// an IN subquery on the updated table, and SQLite has no UPDATE ... LIMIT.
// Only logs of an existing instance with an owner are selected, so each
// batch changes every selected row and the loop ends.
func backfillDeployLogUsers(tx *gorm.DB, batchSize int) error {
	for {
		var ids []string
		if err := tx.Raw(
			`SELECT dl.id FROM deployment_logs dl
			JOIN stack_instances si ON si.id = dl.stack_instance_id
			WHERE (dl.user_id IS NULL OR dl.user_id = '') AND dl.action = ?
			AND si.owner_id IS NOT NULL AND si.owner_id <> ''
			LIMIT ?`,
			models.DeployActionDeploy, batchSize,
		).Scan(&ids).Error; err != nil {
			return fmt.Errorf("selecting deploy logs without a user: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Exec(
			`UPDATE deployment_logs SET user_id = (
				SELECT si.owner_id FROM stack_instances si WHERE si.id = deployment_logs.stack_instance_id
			) WHERE id IN ?`,
			ids,
		).Error; err != nil {
			return fmt.Errorf("setting deploy log users: %w", err)
		}
	}
}

// userRoleIndexName is the index on users.role.
const userRoleIndexName = "idx_users_role"

// userRoleIndexMigration is migration 50. It adds an index on users.role.
// The guarded admin changes (UpdateRole, SetDisabled, DeleteGuarded) lock
// the admin rows with SELECT ... WHERE role = 'admin' FOR UPDATE; with the
// index, InnoDB locks the admin rows only, not every scanned users row. Up
// and Down are idempotent. It is a function so tests can run its Down step.
func userRoleIndexMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261009000050",
		Name:        "add_users_role_index",
		Description: "Add an index on users.role for the admin-row locks of role, disable and delete changes",
		Up: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if !m.HasTable(&models.User{}) || m.HasIndex(&models.User{}, userRoleIndexName) {
				return nil
			}
			return tx.Exec("CREATE INDEX " + userRoleIndexName + " ON users (role)").Error // #nosec G202 -- index name is a constant
		},
		Down: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if m.HasIndex(&models.User{}, userRoleIndexName) {
				return m.DropIndex(&models.User{}, userRoleIndexName)
			}
			return nil
		},
	}
}

// postDeployHookUntilMigration is migration 51. It adds
// post_deploy_hook_until to stack_instances: set while blocking post-deploy
// hooks run, so the k8s status watcher on the leader does not set error for
// the instance during the wait. No backfill. It is a function so tests can
// run its Down step.
func postDeployHookUntilMigration() schema.Migration {
	return schema.Migration{
		Version:     "20261009000051",
		Name:        "add_stack_instance_post_deploy_hook_until",
		Description: "Add post_deploy_hook_until to stack_instances (blocking post-deploy hooks run until this time)",
		Up: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if m.HasTable(&models.StackInstance{}) && !m.HasColumn(&models.StackInstance{}, "PostDeployHookUntil") {
				return m.AddColumn(&models.StackInstance{}, "PostDeployHookUntil")
			}
			return nil
		},
		Down: func(tx *gorm.DB) error {
			m := tx.Migrator()
			if m.HasColumn(&models.StackInstance{}, "PostDeployHookUntil") {
				return m.DropColumn(&models.StackInstance{}, "PostDeployHookUntil")
			}
			return nil
		},
	}
}
