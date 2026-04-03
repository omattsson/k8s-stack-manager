package database

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"backend/internal/config"

	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	maxRetries = 5
	retryDelay = 2 * time.Second
)

// NewFromAppConfig creates a new database instance from application config
func NewFromAppConfig(cfg *config.Config) (*Database, error) {
	// Set up logging based on environment
	logLevel := logger.Info
	if !cfg.App.Debug {
		logLevel = logger.Error
	}

	// Initialize database with retries
	var db *gorm.DB
	var err error
	var retryCount int

	for retryCount < maxRetries {
		db, err = gorm.Open(mysql.Open(cfg.Database.DSN()), &gorm.Config{
			Logger:                 logger.Default.LogMode(logLevel),
			SkipDefaultTransaction: true,
		})

		if err == nil {
			break
		}

		retryCount++
		if retryCount < maxRetries {
			slog.Warn("Failed to connect to database, retrying...",
				"attempt", retryCount,
				"maxRetries", maxRetries,
				"error", err,
				"retryDelay", retryDelay,
			)
			time.Sleep(retryDelay)
		}
	}

	if err != nil {
		return nil, NewDatabaseError("connect", fmt.Errorf("failed after %d attempts: %w", retryCount, err))
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, NewDatabaseError("configure", err)
	}

	// Set connection pool settings from config
	sqlDB.SetMaxOpenConns(int(cfg.Database.MaxOpenConns))
	sqlDB.SetMaxIdleConns(int(cfg.Database.MaxIdleConns))
	sqlDB.SetConnMaxLifetime(cfg.Database.ConnMaxLifetime)

	// Test the connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, NewDatabaseError("ping", err)
	}

	database := &Database{DB: db}

	// Enable foreign key checks and set other important MySQL settings
	if err := database.configure(); err != nil {
		return nil, err
	}

	return database, nil
}

// NewFromDBConfig creates a new database instance from database configuration
func NewFromDBConfig(cfg *Config) (*Database, error) {
	// For testing, use SQLite in-memory database when cfg is nil
	if cfg == nil {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
			Logger:                 logger.Default.LogMode(logger.Info),
			SkipDefaultTransaction: true,
		})
		if err != nil {
			return nil, NewDatabaseError("connect", err)
		}

		sqlDB, err := db.DB()
		if err != nil {
			return nil, NewDatabaseError("configure", err)
		}

		// Set reasonable defaults for testing
		sqlDB.SetMaxOpenConns(5)
		sqlDB.SetMaxIdleConns(2)
		sqlDB.SetConnMaxLifetime(time.Hour)

		return &Database{DB: db}, nil
	}

	// Initialize database with retries
	var db *gorm.DB
	var err error
	var retryCount int

	for retryCount < maxRetries {
		db, err = gorm.Open(mysql.Open(cfg.DSN()), &gorm.Config{
			Logger:                 logger.Default.LogMode(logger.Info),
			SkipDefaultTransaction: true,
		})

		if err == nil {
			break
		}

		retryCount++
		if retryCount < maxRetries {
			slog.Warn("Failed to connect to database, retrying...",
				"attempt", retryCount,
				"maxRetries", maxRetries,
				"error", err,
				"retryDelay", retryDelay,
			)
			time.Sleep(retryDelay)
		}
	}

	if err != nil {
		return nil, NewDatabaseError("connect", fmt.Errorf("failed to connect to database after %d attempts: %v", retryCount, err))
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, NewDatabaseError("configure", err)
	}

	// Set reasonable defaults for connection pool
	sqlDB.SetMaxOpenConns(5)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// Test the connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, NewDatabaseError("ping", err)
	}

	database := &Database{DB: db}

	// Enable foreign key checks and set other important MySQL settings
	if err := database.configure(); err != nil {
		return nil, err
	}

	return database, nil
}

// configure sets up important database settings
func (d *Database) configure() error {
	// Get the dialect
	dialect := d.Dialector.Name()

	if dialect == "mysql" {
		// Enable foreign key checks
		if err := d.Exec("SET FOREIGN_KEY_CHECKS = 1").Error; err != nil {
			return NewDatabaseError("configure", err)
		}

		// Set explicit default timezone to UTC
		if err := d.Exec("SET time_zone = '+00:00'").Error; err != nil {
			return NewDatabaseError("configure", err)
		}

		// Set SQL mode to strict
		if err := d.Exec("SET SESSION sql_mode = 'STRICT_TRANS_TABLES,NO_AUTO_VALUE_ON_ZERO,NO_ENGINE_SUBSTITUTION'").Error; err != nil {
			return NewDatabaseError("configure", err)
		}
	} else if dialect == "sqlite" {
		// Enable foreign key checks for SQLite
		if err := d.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
			return NewDatabaseError("configure", err)
		}
	}

	return nil
}
