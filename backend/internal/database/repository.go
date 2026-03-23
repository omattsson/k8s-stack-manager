package database

import (
	"fmt"
	"log/slog"

	"backend/internal/config"
	"backend/internal/database/azure"
	"backend/internal/models"

	"gorm.io/gorm"
)

// NewRepository creates a new repository based on the configuration
func NewRepository(cfg *config.Config) (models.Repository, error) {
	if cfg.AzureTable.UseAzureTable {
		slog.Info("Using Azure Table Storage as repository")
		return azure.NewTableRepository(
			cfg.AzureTable.AccountName,
			cfg.AzureTable.AccountKey,
			cfg.AzureTable.Endpoint,
			cfg.AzureTable.TableName,
			cfg.AzureTable.UseAzurite,
		)
	}

	slog.Info("Using MySQL as repository")
	db, err := NewFromAppConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize MySQL database: %w", err)
	}

	// Run database migrations (migrator tracks applied versions; safe on every startup)
	if err := db.AutoMigrate(); err != nil {
		// Clean up the database connection to avoid resource leaks
		if sqlDB, dbErr := db.DB.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		return nil, fmt.Errorf("failed to run database migrations: %w", err)
	}

	return models.NewRepository(db.DB), nil
}

// NewRepositoryWithGormDB creates a repository and, for MySQL, also returns the
// underlying *gorm.DB so callers can construct domain-specific repositories
// without opening a second connection pool. Returns nil *gorm.DB for Azure Table.
func NewRepositoryWithGormDB(cfg *config.Config) (models.Repository, *gorm.DB, error) {
	if cfg.AzureTable.UseAzureTable {
		slog.Info("Using Azure Table Storage as repository")
		repo, err := azure.NewTableRepository(
			cfg.AzureTable.AccountName,
			cfg.AzureTable.AccountKey,
			cfg.AzureTable.Endpoint,
			cfg.AzureTable.TableName,
			cfg.AzureTable.UseAzurite,
		)
		return repo, nil, err
	}

	slog.Info("Using MySQL as repository")
	db, err := NewFromAppConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize MySQL database: %w", err)
	}

	if err := db.AutoMigrate(); err != nil {
		if sqlDB, dbErr := db.DB.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		return nil, nil, fmt.Errorf("failed to run database migrations: %w", err)
	}

	return models.NewRepository(db.DB), db.DB, nil
}
