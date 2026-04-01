package config_test

import (
	"os"
	"testing"
	"time"

	"backend/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	// Not parallel: subtests use t.Setenv which modifies process env

	// Test with environment variables set
	t.Run("With environment variables", func(t *testing.T) {
		// Set test environment variables
		envVars := map[string]string{
			"APP_NAME":                 "testapp",
			"GO_ENV":                   "testing",
			"APP_DEBUG":                "true",
			"DB_HOST":                  "testhost",
			"DB_PORT":                  "3306",
			"DB_USER":                  "testuser",
			"DB_PASSWORD":              "testpass",
			"DB_NAME":                  "testdb",
			"SERVER_HOST":              "localhost",
			"SERVER_PORT":              "3000",
			"LOG_LEVEL":                "debug",
			"LOG_FILE":                 "test.log",
			"USE_AZURE_TABLE":          "true",
			"USE_AZURITE":              "true",
			"AZURE_TABLE_ACCOUNT_NAME": "devstoreaccount1",
			"AZURE_TABLE_ACCOUNT_KEY":  "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==",
			"AZURE_TABLE_ENDPOINT":     "127.0.0.1:10002",
			"AZURE_TABLE_NAME":         "testitems",
		}

		// Set environment variables
		for k, v := range envVars {
			t.Setenv(k, v)
		}
		defer func() {
			// Clean up environment variables
			for k := range envVars {
				os.Unsetenv(k)
			}
		}()

		config, err := config.LoadConfig()
		require.NoError(t, err)
		require.NotNil(t, config)

		// Check app config
		assert.Equal(t, "testapp", config.App.Name)
		assert.Equal(t, "testing", config.App.Environment)
		assert.True(t, config.App.Debug)

		// Check database config
		assert.Equal(t, "testhost", config.Database.Host)
		assert.Equal(t, "3306", config.Database.Port)
		assert.Equal(t, "testuser", config.Database.User)
		assert.Equal(t, "testpass", config.Database.Password)
		assert.Equal(t, "testdb", config.Database.DBName)

		// Check server config
		assert.Equal(t, "localhost", config.Server.Host)
		assert.Equal(t, "3000", config.Server.Port)

		// Check logging config
		assert.Equal(t, "debug", config.Logging.Level)
		assert.Equal(t, "test.log", config.Logging.File)

		// Check Azure Table config
		assert.True(t, config.AzureTable.UseAzureTable)
		assert.True(t, config.AzureTable.UseAzurite)
		assert.Equal(t, "devstoreaccount1", config.AzureTable.AccountName)
		assert.Equal(t, "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==", config.AzureTable.AccountKey)
		assert.Equal(t, "127.0.0.1:10002", config.AzureTable.Endpoint)
		assert.Equal(t, "testitems", config.AzureTable.TableName)
	})

	// Test with default values
	t.Run("With default values", func(t *testing.T) {
		// Clear all relevant environment variables
		vars := []string{
			"APP_NAME", "GO_ENV", "APP_DEBUG",
			"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME",
			"DB_MAX_OPEN_CONNS", "DB_MAX_IDLE_CONNS", "DB_CONN_MAX_LIFETIME",
			"SERVER_HOST", "SERVER_PORT", "SERVER_READ_TIMEOUT", "SERVER_WRITE_TIMEOUT", "SERVER_SHUTDOWN_TIMEOUT",
			"LOG_LEVEL", "LOG_FILE",
			"USE_AZURE_TABLE", "USE_AZURITE",
			"AZURE_TABLE_ACCOUNT_NAME", "AZURE_TABLE_ACCOUNT_KEY",
			"AZURE_TABLE_ENDPOINT", "AZURE_TABLE_NAME",
		}
		for _, v := range vars {
			os.Unsetenv(v)
		}

		config, err := config.LoadConfig()
		require.NoError(t, err)
		require.NotNil(t, config)

		// Check default app config
		assert.Equal(t, "backend-api", config.App.Name)
		assert.Equal(t, "development", config.App.Environment)
		assert.True(t, config.App.Debug)

		// Check default database config
		assert.Equal(t, "localhost", config.Database.Host)
		assert.Equal(t, "3306", config.Database.Port)
		assert.Equal(t, "root", config.Database.User)
		assert.Empty(t, config.Database.Password)
		assert.Equal(t, "app", config.Database.DBName)
		assert.Equal(t, int32(25), config.Database.MaxOpenConns)
		assert.Equal(t, int32(5), config.Database.MaxIdleConns)
		assert.Equal(t, 5*time.Minute, config.Database.ConnMaxLifetime)

		// Check default server config
		assert.Empty(t, config.Server.Host)
		assert.Equal(t, "8081", config.Server.Port)
		assert.Equal(t, 10*time.Second, config.Server.ReadTimeout)
		assert.Equal(t, time.Duration(0), config.Server.WriteTimeout) // disabled; per-write deadlines enforced in WebSocket write pump				assert.Equal(t, 30*time.Second, config.Server.IdleTimeout)
		assert.Equal(t, 30*time.Second, config.Server.ShutdownTimeout)
		// Check default logging config
		assert.Equal(t, "info", config.Logging.Level)
		assert.Empty(t, config.Logging.File)

		// Check default Azure Table config
		assert.False(t, config.AzureTable.UseAzureTable)
		assert.False(t, config.AzureTable.UseAzurite)
		assert.Empty(t, config.AzureTable.AccountName)
		assert.Empty(t, config.AzureTable.AccountKey)
		assert.Empty(t, config.AzureTable.Endpoint)
		assert.Equal(t, "items", config.AzureTable.TableName)
	})
}

func TestDatabaseDSN(t *testing.T) {
	t.Parallel()
	dbConfig := config.DatabaseConfig{
		Host:     "testhost",
		Port:     "3306",
		User:     "testuser",
		Password: "testpass",
		DBName:   "testdb",
	}

	expected := "testuser:testpass@tcp(testhost:3306)/testdb?charset=utf8mb4&parseTime=True&loc=UTC"
	assert.Equal(t, expected, dbConfig.DSN())
}
func TestConfigValidate(t *testing.T) {
	t.Parallel()

	t.Run("valid config returns nil", func(t *testing.T) {
		t.Parallel()
		validConfig := &config.Config{
			App: config.AppConfig{
				Name:        "myapp",
				Environment: "production",
				Debug:       false,
			},
			Database: config.DatabaseConfig{
				Host:            "localhost",
				Port:            "3306",
				User:            "user",
				Password:        "pass",
				DBName:          "dbname",
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: 1 * time.Minute,
			},
			Server: config.ServerConfig{
				Host:            "127.0.0.1",
				Port:            "8080",
				ReadTimeout:     5 * time.Second,
				WriteTimeout:    5 * time.Second,
				IdleTimeout:     30 * time.Second,
				ShutdownTimeout: 10 * time.Second,
			},
		}

		assert.NoError(t, validConfig.Validate())
	})

	t.Run("invalid app config", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			App: config.AppConfig{
				Name:        "",
				Environment: "production",
				Debug:       false,
			},
			Database: config.DatabaseConfig{
				Host:            "localhost",
				Port:            "3306",
				User:            "user",
				Password:        "pass",
				DBName:          "dbname",
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: 1 * time.Minute,
			},
			Server: config.ServerConfig{
				Host:            "127.0.0.1",
				Port:            "8080",
				ReadTimeout:     5 * time.Second,
				WriteTimeout:    5 * time.Second,
				IdleTimeout:     30 * time.Second,
				ShutdownTimeout: 10 * time.Second,
			},
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "app config")
	})

	t.Run("invalid database config", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			App: config.AppConfig{
				Name:        "myapp",
				Environment: "production",
				Debug:       false,
			},
			Database: config.DatabaseConfig{
				Host:            "",
				Port:            "3306",
				User:            "user",
				Password:        "pass",
				DBName:          "dbname",
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: 1 * time.Minute,
			},
			Server: config.ServerConfig{
				Host:            "127.0.0.1",
				Port:            "8080",
				ReadTimeout:     5 * time.Second,
				WriteTimeout:    5 * time.Second,
				IdleTimeout:     30 * time.Second,
				ShutdownTimeout: 10 * time.Second,
			},
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "database config")
	})

	t.Run("invalid server config", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			App: config.AppConfig{
				Name:        "myapp",
				Environment: "production",
				Debug:       false,
			},
			Database: config.DatabaseConfig{
				Host:            "localhost",
				Port:            "3306",
				User:            "user",
				Password:        "pass",
				DBName:          "dbname",
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: 1 * time.Minute,
			},
			Server: config.ServerConfig{
				Host:            "127.0.0.1",
				Port:            "",
				ReadTimeout:     5 * time.Second,
				WriteTimeout:    5 * time.Second,
				IdleTimeout:     30 * time.Second,
				ShutdownTimeout: 10 * time.Second,
			},
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "server config")
	})

	t.Run("zero WriteTimeout passes validation", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			App: config.AppConfig{
				Name:        "myapp",
				Environment: "production",
			},
			Database: config.DatabaseConfig{
				Host:            "localhost",
				Port:            "3306",
				User:            "user",
				DBName:          "dbname",
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: 1 * time.Minute,
			},
			Server: config.ServerConfig{
				Port:            "8080",
				ReadTimeout:     5 * time.Second,
				WriteTimeout:    0, // disabled at server level; per-write deadlines in WebSocket pump
				IdleTimeout:     30 * time.Second,
				ShutdownTimeout: 10 * time.Second,
			},
		}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("negative WriteTimeout fails validation", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			App: config.AppConfig{
				Name:        "myapp",
				Environment: "production",
			},
			Database: config.DatabaseConfig{
				Host:            "localhost",
				Port:            "3306",
				User:            "user",
				DBName:          "dbname",
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: 1 * time.Minute,
			},
			Server: config.ServerConfig{
				Port:            "8080",
				ReadTimeout:     5 * time.Second,
				WriteTimeout:    -1 * time.Second,
				IdleTimeout:     30 * time.Second,
				ShutdownTimeout: 10 * time.Second,
			},
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write timeout")
	})
}
func TestAuthConfigValidate(t *testing.T) {
	t.Parallel()

	t.Run("valid auth config passes", func(t *testing.T) {
		t.Parallel()
		cfg := config.AuthConfig{
			JWTSecret:     "this-is-a-long-enough-secret",
			JWTExpiration: 24 * time.Hour,
		}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("empty JWT secret fails", func(t *testing.T) {
		t.Parallel()
		cfg := config.AuthConfig{
			JWTSecret:     "",
			JWTExpiration: 24 * time.Hour,
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "jwt_secret is required")
	})

	t.Run("short JWT secret fails", func(t *testing.T) {
		t.Parallel()
		cfg := config.AuthConfig{
			JWTSecret:     "tooshort",
			JWTExpiration: 24 * time.Hour,
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "jwt_secret must be at least 16 characters")
	})

	t.Run("negative expiration fails", func(t *testing.T) {
		t.Parallel()
		cfg := config.AuthConfig{
			JWTSecret:     "this-is-a-long-enough-secret",
			JWTExpiration: -1 * time.Hour,
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "jwt_expiration must be positive")
	})

	t.Run("Config.Validate with auth config set validates it", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			App: config.AppConfig{
				Name:        "myapp",
				Environment: "production",
			},
			Database: config.DatabaseConfig{
				Host:            "localhost",
				Port:            "3306",
				User:            "user",
				DBName:          "dbname",
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: 1 * time.Minute,
			},
			Server: config.ServerConfig{
				Port:            "8080",
				ReadTimeout:     5 * time.Second,
				WriteTimeout:    0,
				IdleTimeout:     30 * time.Second,
				ShutdownTimeout: 10 * time.Second,
			},
			Auth: config.AuthConfig{
				JWTSecret:     "short",
				JWTExpiration: 24 * time.Hour,
				AdminPassword: "admin123",
			},
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "auth config")
	})
}

func TestAzureTableConfigValidate(t *testing.T) {
	t.Parallel()

	t.Run("valid config returns nil", func(t *testing.T) {
		t.Parallel()
		validConfig := config.AzureTableConfig{
			AccountName: "account",
			AccountKey:  "key",
			Endpoint:    "endpoint",
			TableName:   "table",
		}

		assert.NoError(t, validConfig.Validate())
	})

	t.Run("missing account name", func(t *testing.T) {
		t.Parallel()
		cfg := config.AzureTableConfig{
			AccountName: "",
			AccountKey:  "key",
			Endpoint:    "endpoint",
			TableName:   "table",
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "account name is required")
	})

	t.Run("missing account key", func(t *testing.T) {
		t.Parallel()
		cfg := config.AzureTableConfig{
			AccountName: "account",
			AccountKey:  "",
			Endpoint:    "endpoint",
			TableName:   "table",
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "account key is required")
	})

	t.Run("missing endpoint", func(t *testing.T) {
		t.Parallel()
		cfg := config.AzureTableConfig{
			AccountName: "account",
			AccountKey:  "key",
			Endpoint:    "",
			TableName:   "table",
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "endpoint is required")
	})

	t.Run("missing table name", func(t *testing.T) {
		t.Parallel()
		cfg := config.AzureTableConfig{
			AccountName: "account",
			AccountKey:  "key",
			Endpoint:    "endpoint",
			TableName:   "",
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "table name is required")
	})
}

// ---- OIDC config tests ----

func TestOIDCConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     config.OIDCConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "disabled config is always valid",
			cfg:     config.OIDCConfig{Enabled: false},
			wantErr: false,
		},
		{
			name: "enabled config with all required fields is valid",
			cfg: config.OIDCConfig{
				Enabled:     true,
				ProviderURL: "https://login.microsoftonline.com/tenant/v2.0",
				ClientID:    "my-client",
				RedirectURL: "http://localhost:3000/auth/callback",
			},
			wantErr: false,
		},
		{
			name: "enabled config without ProviderURL is invalid",
			cfg: config.OIDCConfig{
				Enabled:  true,
				ClientID: "my-client",
			},
			wantErr: true,
			errMsg:  "OIDC_PROVIDER_URL",
		},
		{
			name: "enabled config without ClientID is invalid",
			cfg: config.OIDCConfig{
				Enabled:     true,
				ProviderURL: "https://example.com",
				RedirectURL: "http://localhost:3000/auth/callback",
			},
			wantErr: true,
			errMsg:  "OIDC_CLIENT_ID",
		},
		{
			name: "enabled config without RedirectURL is invalid",
			cfg: config.OIDCConfig{
				Enabled:     true,
				ProviderURL: "https://example.com",
				ClientID:    "my-client",
			},
			wantErr: true,
			errMsg:  "OIDC_REDIRECT_URL",
		},
		{
			name: "enabled config missing both required fields is invalid",
			cfg: config.OIDCConfig{
				Enabled: true,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOIDCConfigLoad(t *testing.T) {
	// Not parallel: subtests use t.Setenv which modifies the process environment.

	t.Run("OIDC disabled by default when env var not set", func(t *testing.T) {
		t.Setenv("OIDC_ENABLED", "")
		cfg, err := config.LoadConfig()
		require.NoError(t, err)
		assert.False(t, cfg.OIDC.Enabled)
	})

	t.Run("scopes default to openid profile email when env var not set", func(t *testing.T) {
		t.Setenv("OIDC_ENABLED", "")
		t.Setenv("OIDC_SCOPES", "")
		cfg, err := config.LoadConfig()
		require.NoError(t, err)
		assert.Equal(t, []string{"openid", "profile", "email"}, cfg.OIDC.Scopes)
	})

	t.Run("OIDC enabled with required fields is valid", func(t *testing.T) {
		t.Setenv("OIDC_ENABLED", "true")
		t.Setenv("OIDC_PROVIDER_URL", "https://login.microsoftonline.com/tenant/v2.0")
		t.Setenv("OIDC_CLIENT_ID", "test-client-id")
		t.Setenv("OIDC_REDIRECT_URL", "http://localhost:3000/auth/callback")
		cfg, err := config.LoadConfig()
		require.NoError(t, err)
		assert.True(t, cfg.OIDC.Enabled)
		assert.Equal(t, "https://login.microsoftonline.com/tenant/v2.0", cfg.OIDC.ProviderURL)
		assert.Equal(t, "test-client-id", cfg.OIDC.ClientID)
	})

	t.Run("OIDC enabled without ProviderURL returns validation error", func(t *testing.T) {
		t.Setenv("OIDC_ENABLED", "true")
		t.Setenv("OIDC_PROVIDER_URL", "")
		t.Setenv("OIDC_CLIENT_ID", "test-client-id")
		_, err := config.LoadConfig()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OIDC_PROVIDER_URL")
	})

	t.Run("OIDC enabled without ClientID returns validation error", func(t *testing.T) {
		t.Setenv("OIDC_ENABLED", "true")
		t.Setenv("OIDC_PROVIDER_URL", "https://example.com")
		t.Setenv("OIDC_CLIENT_ID", "")
		_, err := config.LoadConfig()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OIDC_CLIENT_ID")
	})

	t.Run("admin and devops roles are parsed from comma-separated values", func(t *testing.T) {
		t.Setenv("OIDC_ENABLED", "")
		t.Setenv("OIDC_ADMIN_ROLES", "k8s-stack-admin,stack-admin")
		t.Setenv("OIDC_DEVOPS_ROLES", "k8s-stack-devops")
		cfg, err := config.LoadConfig()
		require.NoError(t, err)
		assert.Equal(t, []string{"k8s-stack-admin", "stack-admin"}, cfg.OIDC.AdminRoles)
		assert.Equal(t, []string{"k8s-stack-devops"}, cfg.OIDC.DevOpsRoles)
	})

	t.Run("empty admin and devops role env vars produce nil slices", func(t *testing.T) {
		t.Setenv("OIDC_ENABLED", "")
		t.Setenv("OIDC_ADMIN_ROLES", "")
		t.Setenv("OIDC_DEVOPS_ROLES", "")
		cfg, err := config.LoadConfig()
		require.NoError(t, err)
		assert.Nil(t, cfg.OIDC.AdminRoles)
		assert.Nil(t, cfg.OIDC.DevOpsRoles)
	})

	t.Run("custom scopes override the default", func(t *testing.T) {
		t.Setenv("OIDC_ENABLED", "")
		t.Setenv("OIDC_SCOPES", "openid,groups")
		cfg, err := config.LoadConfig()
		require.NoError(t, err)
		assert.Equal(t, []string{"openid", "groups"}, cfg.OIDC.Scopes)
	})

	t.Run("auto-provision defaults to true", func(t *testing.T) {
		t.Setenv("OIDC_ENABLED", "")
		t.Setenv("OIDC_AUTO_PROVISION", "")
		cfg, err := config.LoadConfig()
		require.NoError(t, err)
		assert.True(t, cfg.OIDC.AutoProvision)
	})

	t.Run("local auth defaults to true", func(t *testing.T) {
		t.Setenv("OIDC_ENABLED", "")
		t.Setenv("OIDC_LOCAL_AUTH", "")
		cfg, err := config.LoadConfig()
		require.NoError(t, err)
		assert.True(t, cfg.OIDC.LocalAuth)
	})
}

func TestOtelConfigLoad(t *testing.T) {
	// Not parallel: subtests use t.Setenv which modifies the process environment.

	t.Run("OTel disabled by default", func(t *testing.T) {
		t.Setenv("OTEL_ENABLED", "")
		cfg, err := config.LoadConfig()
		require.NoError(t, err)
		assert.False(t, cfg.Otel.Enabled)
	})

	t.Run("OTel default values", func(t *testing.T) {
		t.Setenv("OTEL_ENABLED", "")
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
		t.Setenv("OTEL_SERVICE_NAME", "")
		t.Setenv("OTEL_TRACE_SAMPLE_RATE", "")
		cfg, err := config.LoadConfig()
		require.NoError(t, err)
		assert.False(t, cfg.Otel.Enabled)
		assert.Equal(t, "localhost:4317", cfg.Otel.Endpoint)
		assert.Equal(t, "k8s-stack-manager", cfg.Otel.ServiceName)
		assert.Equal(t, 1.0, cfg.Otel.SampleRate)
	})

	t.Run("OTel enabled with custom values", func(t *testing.T) {
		t.Setenv("OTEL_ENABLED", "true")
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "collector:4317")
		t.Setenv("OTEL_SERVICE_NAME", "my-service")
		t.Setenv("OTEL_TRACE_SAMPLE_RATE", "0.25")
		cfg, err := config.LoadConfig()
		require.NoError(t, err)
		assert.True(t, cfg.Otel.Enabled)
		assert.Equal(t, "collector:4317", cfg.Otel.Endpoint)
		assert.Equal(t, "my-service", cfg.Otel.ServiceName)
		assert.Equal(t, 0.25, cfg.Otel.SampleRate)
	})
}
