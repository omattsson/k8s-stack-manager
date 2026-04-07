package database

import (
	"database/sql"
	"errors"
	"fmt"
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

	// Run migrations
	if err := migrator.MigrateUp(); err != nil {
		return err
	}

	slog.Info("Database migrations completed successfully")
	return nil
}
