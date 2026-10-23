package database

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewConfig(t *testing.T) {
	t.Run("With environment variables", func(t *testing.T) {
		// Set test environment variables
		os.Setenv("DB_HOST", "testhost")
		os.Setenv("DB_PORT", "3307")
		os.Setenv("DB_USER", "testuser")
		os.Setenv("DB_PASSWORD", "testpass")
		os.Setenv("DB_NAME", "testdb")
		defer func() {
			os.Unsetenv("DB_HOST")
			os.Unsetenv("DB_PORT")
			os.Unsetenv("DB_USER")
			os.Unsetenv("DB_PASSWORD")
			os.Unsetenv("DB_NAME")
		}()

		config := NewConfig()

		assert.Equal(t, "testhost", config.Host)
		assert.Equal(t, "3307", config.Port)
		assert.Equal(t, "testuser", config.User)
		assert.Equal(t, "testpass", config.Password)
		assert.Equal(t, "testdb", config.DBName)
	})

	t.Run("With default values", func(t *testing.T) {
		// Clear environment variables
		os.Unsetenv("DB_HOST")
		os.Unsetenv("DB_PORT")
		os.Unsetenv("DB_USER")
		os.Unsetenv("DB_PASSWORD")
		os.Unsetenv("DB_NAME")

		config := NewConfig()

		assert.Equal(t, "localhost", config.Host)
		assert.Equal(t, "3306", config.Port)
		assert.Equal(t, "appuser", config.User)
		assert.Equal(t, "apppass", config.Password)
		assert.Equal(t, "app", config.DBName)
	})
}

func TestConfigDSN(t *testing.T) {
	config := &Config{
		Host:     "testhost",
		Port:     "3306",
		User:     "testuser",
		Password: "testpass",
		DBName:   "testdb",
	}

	expected := "testuser:testpass@tcp(testhost:3306)/testdb?charset=utf8mb4&parseTime=True&loc=UTC&interpolateParams=true"
	assert.Equal(t, expected, config.DSN())
}
