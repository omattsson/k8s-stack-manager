package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGetEnvInt32(t *testing.T) {
	// Not parallel: subtests use t.Setenv which is incompatible with t.Parallel().

	tests := []struct {
		name     string
		envValue string // "" means unset
		setEnv   bool
		fallback int32
		want     int32
	}{
		{
			name:     "valid positive int",
			envValue: "42",
			setEnv:   true,
			fallback: 10,
			want:     42,
		},
		{
			name:     "valid zero",
			envValue: "0",
			setEnv:   true,
			fallback: 10,
			want:     0,
		},
		{
			name:     "valid negative",
			envValue: "-5",
			setEnv:   true,
			fallback: 10,
			want:     -5,
		},
		{
			name:     "unset returns fallback",
			setEnv:   false,
			fallback: 25,
			want:     25,
		},
		{
			name:     "non-numeric returns fallback",
			envValue: "abc",
			setEnv:   true,
			fallback: 25,
			want:     25,
		},
		{
			name:     "float returns fallback",
			envValue: "3.14",
			setEnv:   true,
			fallback: 7,
			want:     7,
		},
		{
			name:     "overflow returns fallback",
			envValue: "99999999999999999999",
			setEnv:   true,
			fallback: 5,
			want:     5,
		},
		{
			name:     "empty space returns fallback",
			envValue: "   ",
			setEnv:   true,
			fallback: 5,
			want:     5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const envKey = "TEST_GET_ENV_INT32"
			if tt.setEnv {
				t.Setenv(envKey, tt.envValue)
			}

			got := getEnvInt32(envKey, tt.fallback)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetEnvFloat64(t *testing.T) {
	// Not parallel: subtests use t.Setenv which is incompatible with t.Parallel().

	tests := []struct {
		name     string
		envValue string
		setEnv   bool
		fallback float64
		want     float64
	}{
		{
			name:     "valid float",
			envValue: "0.5",
			setEnv:   true,
			fallback: 1.0,
			want:     0.5,
		},
		{
			name:     "valid integer-like float",
			envValue: "1",
			setEnv:   true,
			fallback: 0.5,
			want:     1.0,
		},
		{
			name:     "valid zero",
			envValue: "0",
			setEnv:   true,
			fallback: 1.0,
			want:     0.0,
		},
		{
			name:     "unset returns fallback",
			setEnv:   false,
			fallback: 1.0,
			want:     1.0,
		},
		{
			name:     "non-numeric returns fallback",
			envValue: "abc",
			setEnv:   true,
			fallback: 1.0,
			want:     1.0,
		},
		{
			name:     "empty space returns fallback",
			envValue: "   ",
			setEnv:   true,
			fallback: 0.75,
			want:     0.75,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const envKey = "TEST_GET_ENV_FLOAT64"
			if tt.setEnv {
				t.Setenv(envKey, tt.envValue)
			}

			got := getEnvFloat64(envKey, tt.fallback)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetEnvDuration(t *testing.T) {
	// Not parallel: subtests use t.Setenv which is incompatible with t.Parallel().

	tests := []struct {
		name     string
		envValue string // "" means unset
		setEnv   bool
		fallback time.Duration
		want     time.Duration
	}{
		{
			name:     "valid duration seconds",
			envValue: "30s",
			setEnv:   true,
			fallback: 10 * time.Second,
			want:     30 * time.Second,
		},
		{
			name:     "valid duration minutes",
			envValue: "5m",
			setEnv:   true,
			fallback: 1 * time.Minute,
			want:     5 * time.Minute,
		},
		{
			name:     "valid duration hours",
			envValue: "2h",
			setEnv:   true,
			fallback: 1 * time.Hour,
			want:     2 * time.Hour,
		},
		{
			name:     "valid zero duration",
			envValue: "0s",
			setEnv:   true,
			fallback: 10 * time.Second,
			want:     0,
		},
		{
			name:     "unset returns fallback",
			setEnv:   false,
			fallback: 5 * time.Minute,
			want:     5 * time.Minute,
		},
		{
			name:     "invalid string returns fallback",
			envValue: "not-a-duration",
			setEnv:   true,
			fallback: 30 * time.Second,
			want:     30 * time.Second,
		},
		{
			name:     "plain number without unit returns fallback",
			envValue: "42",
			setEnv:   true,
			fallback: 10 * time.Second,
			want:     10 * time.Second,
		},
		{
			name:     "empty spaces returns fallback",
			envValue: "   ",
			setEnv:   true,
			fallback: 10 * time.Second,
			want:     10 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const envKey = "TEST_GET_ENV_DURATION"
			if tt.setEnv {
				t.Setenv(envKey, tt.envValue)
			}

			got := getEnvDuration(envKey, tt.fallback)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetEnvBool(t *testing.T) {
	// Not parallel: subtests use t.Setenv which is incompatible with t.Parallel().

	tests := []struct {
		name     string
		envValue string
		setEnv   bool
		fallback bool
		want     bool
	}{
		{
			name:     "true string",
			envValue: "true",
			setEnv:   true,
			fallback: false,
			want:     true,
		},
		{
			name:     "false string",
			envValue: "false",
			setEnv:   true,
			fallback: true,
			want:     false,
		},
		{
			name:     "1 is true",
			envValue: "1",
			setEnv:   true,
			fallback: false,
			want:     true,
		},
		{
			name:     "0 is false",
			envValue: "0",
			setEnv:   true,
			fallback: true,
			want:     false,
		},
		{
			name:     "unset returns fallback true",
			setEnv:   false,
			fallback: true,
			want:     true,
		},
		{
			name:     "unset returns fallback false",
			setEnv:   false,
			fallback: false,
			want:     false,
		},
		{
			name:     "invalid returns fallback",
			envValue: "notabool",
			setEnv:   true,
			fallback: true,
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const envKey = "TEST_GET_ENV_BOOL"
			if tt.setEnv {
				t.Setenv(envKey, tt.envValue)
			}

			got := getEnvBool(envKey, tt.fallback)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetEnv(t *testing.T) {
	// Not parallel: subtests use t.Setenv which is incompatible with t.Parallel().

	t.Run("returns env value when set", func(t *testing.T) {
		t.Setenv("TEST_GET_ENV", "myvalue")

		got := getEnv("TEST_GET_ENV", "fallback")
		assert.Equal(t, "myvalue", got)
	})

	t.Run("returns fallback when unset", func(t *testing.T) {
		got := getEnv("TEST_GET_ENV_UNSET", "fallback")
		assert.Equal(t, "fallback", got)
	})
}

func TestDatabaseDSN_NoPassword(t *testing.T) {
	t.Parallel()
	dbConfig := DatabaseConfig{
		Host:         "localhost",
		Port:         "3306",
		User:         "root",
		Password:     "",
		DBName:       "testdb",
		MaxOpenConns: 10,
	}
	expected := "root@tcp(localhost:3306)/testdb?charset=utf8mb4&parseTime=True&loc=UTC&maxAllowedPacket=0"
	assert.Equal(t, expected, dbConfig.DSN())
}

func TestDatabaseConfigValidate_AllFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    DatabaseConfig
		wantErr   string
	}{
		{
			name: "missing port",
			config: DatabaseConfig{
				Host:            "localhost",
				Port:            "",
				User:            "user",
				DBName:          "db",
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: time.Minute,
			},
			wantErr: "port is required",
		},
		{
			name: "missing user",
			config: DatabaseConfig{
				Host:            "localhost",
				Port:            "3306",
				User:            "",
				DBName:          "db",
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: time.Minute,
			},
			wantErr: "user is required",
		},
		{
			name: "missing dbname",
			config: DatabaseConfig{
				Host:            "localhost",
				Port:            "3306",
				User:            "user",
				DBName:          "",
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: time.Minute,
			},
			wantErr: "database name is required",
		},
		{
			name: "zero max open conns",
			config: DatabaseConfig{
				Host:            "localhost",
				Port:            "3306",
				User:            "user",
				DBName:          "db",
				MaxOpenConns:    0,
				MaxIdleConns:    5,
				ConnMaxLifetime: time.Minute,
			},
			wantErr: "max open connections must be positive",
		},
		{
			name: "zero max idle conns",
			config: DatabaseConfig{
				Host:            "localhost",
				Port:            "3306",
				User:            "user",
				DBName:          "db",
				MaxOpenConns:    10,
				MaxIdleConns:    0,
				ConnMaxLifetime: time.Minute,
			},
			wantErr: "max idle connections must be positive",
		},
		{
			name: "zero conn max lifetime",
			config: DatabaseConfig{
				Host:            "localhost",
				Port:            "3306",
				User:            "user",
				DBName:          "db",
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: 0,
			},
			wantErr: "connection max lifetime must be positive",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.config.Validate()
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestServerConfigValidate_AllPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  ServerConfig
		wantErr string
	}{
		{
			name: "missing port",
			config: ServerConfig{
				Port:         "",
				ReadTimeout:  5 * time.Second,
				WriteTimeout: 5 * time.Second,
				IdleTimeout:  30 * time.Second,
			},
			wantErr: "port is required",
		},
		{
			name: "negative read timeout",
			config: ServerConfig{
				Port:         "8080",
				ReadTimeout:  -1 * time.Second,
				WriteTimeout: 5 * time.Second,
				IdleTimeout:  30 * time.Second,
			},
			wantErr: "read timeout must be positive",
		},
		{
			name: "negative idle timeout",
			config: ServerConfig{
				Port:         "8080",
				ReadTimeout:  5 * time.Second,
				WriteTimeout: 0,
				IdleTimeout:  -1 * time.Second,
			},
			wantErr: "idle timeout must be positive",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.config.Validate()
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestAppConfigValidate(t *testing.T) {
	t.Parallel()

	t.Run("missing environment", func(t *testing.T) {
		t.Parallel()
		cfg := AppConfig{Name: "app", Environment: ""}
		err := cfg.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "environment is required")
	})
}

func TestConfigValidate_AzureTable(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		App: AppConfig{Name: "app", Environment: "prod"},
		AzureTable: AzureTableConfig{
			UseAzureTable: true,
			AccountName:   "",
			AccountKey:    "key",
			Endpoint:      "endpoint",
			TableName:     "table",
		},
		Server: ServerConfig{
			Port:        "8080",
			ReadTimeout: 5 * time.Second,
			IdleTimeout: 30 * time.Second,
		},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "azure table config")
}
