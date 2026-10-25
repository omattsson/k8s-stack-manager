package database

import (
	"os"

	"backend/internal/config"
)

// Config holds database connection configuration
type Config struct {
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
}

// NewConfig creates a new database configuration from environment variables
func NewConfig() *Config {
	return &Config{
		Host:     getEnv("DB_HOST", "localhost"),
		Port:     getEnv("DB_PORT", "3306"),
		User:     getEnv("DB_USER", "appuser"),
		Password: getEnv("DB_PASSWORD", "apppass"),
		DBName:   getEnv("DB_NAME", "app"),
	}
}

// DSN returns the database connection string
func (c *Config) DSN() string {
	return c.User + ":" + c.Password + "@tcp(" + c.Host + ":" + c.Port + ")/" + c.DBName + "?charset=utf8mb4&parseTime=True&loc=UTC&interpolateParams=true" + config.MySQLSessionParams
}

// Helper function to get environment variables with default values
func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
