package database

import (
	"fmt"
	"log/slog"

	"backend/internal/config"
	"backend/internal/models"

	"gorm.io/gorm"
)

// NewRepository creates a new MySQL-backed repository.
func NewRepository(cfg *config.Config) (models.Repository, error) {
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

// NewRepositoryWithGormDB creates a MySQL-backed repository and returns the
// underlying *gorm.DB so callers can construct domain-specific repositories
// without opening a second connection pool.
func NewRepositoryWithGormDB(cfg *config.Config) (models.Repository, *gorm.DB, error) {
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
