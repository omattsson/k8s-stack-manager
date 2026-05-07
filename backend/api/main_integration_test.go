//go:build integration

package main

import (
	"backend/internal/config"
	"backend/internal/database"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDatabaseInitialization(t *testing.T) {
	t.Parallel()
	// Use mock config instead of loading from environment
	cfg := &config.Config{
		App: config.AppConfig{
			Name:        "testapp",
			Environment: "testing",
			Debug:       true,
		},
		Database: config.DatabaseConfig{
			Host:            "localhost",
			Port:            "3306",
			User:            "root",
			Password:        "rootpassword",
			DBName:          "app",
			MaxOpenConns:    5,
			MaxIdleConns:    2,
			ConnMaxLifetime: 5 * time.Minute,
		},
		Server: config.ServerConfig{
			Host:            "localhost",
			Port:            "8082",
			ReadTimeout:     10 * time.Second,
			WriteTimeout:    time.Duration(0),
			ShutdownTimeout: 30 * time.Second,
		},
		Logging: config.LogConfig{
			Level: "debug",
			File:  "",
		},
	}

	// Create a mock database instance
	db, err := database.NewFromAppConfig(cfg)
	require.NoError(t, err)
	require.NotNil(t, db)
}
