// Package config provides configuration loading and validation for the application.
// It handles environment variables, default values, and config validation for all components.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	defaultMaxOpenConns    int32 = 25
	defaultMaxIdleConns    int32 = 5
	defaultConnMaxLifetime       = 5 * time.Minute
	defaultReadTimeout           = 10 * time.Second
	// defaultWriteTimeout is 0 (disabled at the server level) because per-write
	// deadlines are enforced in the WebSocket write pump (websocket/client.go).
	// A non-zero server-level write timeout would prematurely terminate idle
	// WebSocket connections.
	defaultWriteTimeout    = time.Duration(0)
	defaultIdleTimeout     = 30 * time.Second
	defaultShutdownTimeout = 30 * time.Second
)

// CORSConfig holds CORS configuration
type CORSConfig struct {
	AllowedOrigins string
}

// AuthConfig holds authentication and authorization configuration.
type AuthConfig struct {
	JWTSecret        string
	JWTExpiration    time.Duration
	LoginCacheTTL    time.Duration
	AdminUsername    string
	AdminPassword    string
	DefaultBranch    string
	SelfRegistration bool
}

// GitProviderConfig holds Git provider configuration.
type GitProviderConfig struct {
	AzureDevOpsPAT        string
	AzureDevOpsDefaultOrg string
	GitLabToken           string
	GitLabBaseURL         string
}

// DeploymentConfig holds deployment-related configuration for Helm operations.
type DeploymentConfig struct {
	DeploymentTimeout         time.Duration
	ClusterHealthPollInterval time.Duration
	HelmBinary                string
	KubeconfigPath            string
	KubeconfigEncryptionKey   string
	MaxConcurrentDeploys      int32
}

// OIDCConfig holds OpenID Connect configuration for external SSO authentication.
type OIDCConfig struct {
	StateTTL      time.Duration
	ProviderURL   string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	RoleClaim     string
	Scopes        []string
	AdminRoles    []string
	DevOpsRoles   []string
	Enabled       bool
	AutoProvision bool
	LocalAuth     bool
}

// Validate checks OIDCConfig when OIDC is enabled.
func (c *OIDCConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.ProviderURL == "" {
		return errors.New("OIDC_PROVIDER_URL is required when OIDC is enabled")
	}
	if c.ClientID == "" {
		return errors.New("OIDC_CLIENT_ID is required when OIDC is enabled")
	}
	if c.RedirectURL == "" {
		return errors.New("OIDC_REDIRECT_URL is required when OIDC is enabled")
	}
	return nil
}

// OtelConfig holds OpenTelemetry configuration for distributed tracing, metrics, and logging.
type OtelConfig struct {
	// 8-byte aligned fields first
	SampleRate float64
	// String fields
	Endpoint    string
	ServiceName string
	// Bool fields
	Enabled bool
}

// Config holds all configuration for the application
//
//nolint:govet // Struct field alignment has been optimized for better memory usage
type Config struct {
	// Group larger structs with time.Duration fields first
	Database DatabaseConfig
	Server   ServerConfig
	Auth     AuthConfig
	// Then string and simple field structs
	OIDC OIDCConfig
	// Then string and simple field structs
	App         AppConfig
	AzureTable  AzureTableConfig
	CORS        CORSConfig
	Logging     LogConfig
	GitProvider GitProviderConfig
	Deployment  DeploymentConfig
	Otel        OtelConfig
}

// AppConfig holds application-wide configuration
type AppConfig struct {
	Name                      string
	Environment               string
	DefaultInstanceTTLMinutes int
	Debug                     bool
	EnableSwagger             bool
}

// DatabaseConfig holds database-specific configuration
//
//nolint:govet // Struct field alignment has been optimized for time.Duration and string fields
type DatabaseConfig struct {
	// 8-byte aligned fields first
	ConnMaxLifetime time.Duration
	// String fields (8-byte on 64-bit systems)
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
	// 4-byte aligned fields
	MaxOpenConns int32
	MaxIdleConns int32
	// Add padding field to maintain 8-byte alignment
	_ [4]byte
}

// AzureTableConfig holds Azure Table Storage configuration
type AzureTableConfig struct {
	AccountName   string
	AccountKey    string
	Endpoint      string
	TableName     string
	UseAzureTable bool // true to use Azure Table as backend
	UseAzurite    bool // true to use local Azurite emulator
}

// ServerConfig holds HTTP server configuration
//
//nolint:govet // Struct field alignment has been optimized for time.Duration fields
type ServerConfig struct {
	// 8-byte aligned fields first
	ReadTimeout time.Duration
	// WriteTimeout is 0 (disabled) at the server level. Per-write deadlines are
	// enforced inside the WebSocket write pump (websocket/client.go), so a
	// server-level write timeout must be disabled to prevent premature
	// termination of idle WebSocket connections. Standard REST handlers complete
	// quickly in practice, but setting WriteTimeout to 0 does mean slow or
	// stalled clients can hold a connection indefinitely. Operators who want
	// protection against slow clients should set SERVER_WRITE_TIMEOUT to a
	// positive value (e.g. 30s); per-handler context deadlines can also be applied.
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	// String fields (8-byte on 64-bit systems)
	Host      string
	Port      string
	PprofAddr string
	// 4-byte fields
	RateLimit      int32
	LoginRateLimit int32
	// 1-byte fields
	HealthVerbose bool
	PprofEnabled  bool
}

// LogConfig holds logging configuration
type LogConfig struct {
	Level string
	File  string
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if err := c.App.Validate(); err != nil {
		return fmt.Errorf("app config: %w", err)
	}

	if c.AzureTable.UseAzureTable {
		if err := c.AzureTable.Validate(); err != nil {
			return fmt.Errorf("azure table config: %w", err)
		}
	} else {
		if err := c.Database.Validate(); err != nil {
			return fmt.Errorf("database config: %w", err)
		}
	}

	if err := c.Server.Validate(); err != nil {
		return fmt.Errorf("server config: %w", err)
	}

	if c.Auth.JWTSecret != "" || c.Auth.AdminPassword != "" {
		if err := c.Auth.Validate(); err != nil {
			return fmt.Errorf("auth config: %w", err)
		}
	}

	if c.Deployment.KubeconfigEncryptionKey == "" || len(c.Deployment.KubeconfigEncryptionKey) < 16 {
		slog.Warn("KUBECONFIG_ENCRYPTION_KEY is not set or too short, kubeconfig data will not be encrypted at rest")
	}

	if c.OIDC.Enabled {
		if err := c.OIDC.Validate(); err != nil {
			return fmt.Errorf("OIDC config: %w", err)
		}
	}

	return nil
}

func (c *AppConfig) Validate() error {
	if c.Name == "" {
		return errors.New("name is required")
	}

	if c.Environment == "" {
		return errors.New("environment is required")
	}

	return nil
}

func (c *DatabaseConfig) Validate() error {
	if c.Host == "" {
		return errors.New("host is required")
	}

	if c.Port == "" {
		return errors.New("port is required")
	}

	if c.User == "" {
		return errors.New("user is required")
	}

	if c.DBName == "" {
		return errors.New("database name is required")
	}

	if c.MaxOpenConns <= 0 {
		return errors.New("max open connections must be positive")
	}

	if c.MaxIdleConns <= 0 {
		return errors.New("max idle connections must be positive")
	}

	if c.ConnMaxLifetime <= 0 {
		return errors.New("connection max lifetime must be positive")
	}

	return nil
}

func (c *AzureTableConfig) Validate() error {
	if c.AccountName == "" {
		return errors.New("account name is required")
	}

	if c.AccountKey == "" {
		return errors.New("account key is required")
	}

	if c.Endpoint == "" {
		return errors.New("endpoint is required")
	}

	if c.TableName == "" {
		return errors.New("table name is required")
	}

	return nil
}

func (c *AuthConfig) Validate() error {
	if c.JWTSecret == "" {
		return errors.New("jwt_secret is required")
	}

	if len(c.JWTSecret) < 16 {
		return errors.New("jwt_secret must be at least 16 characters")
	}

	if c.JWTExpiration <= 0 {
		return errors.New("jwt_expiration must be positive")
	}

	return nil
}

func (c *ServerConfig) Validate() error {
	if c.Port == "" {
		return errors.New("port is required")
	}

	if c.ReadTimeout <= 0 {
		return errors.New("read timeout must be positive")
	}

	// WriteTimeout of 0 is valid — it disables the server-level write timeout
	// (per-write deadlines are handled in the WebSocket write pump instead).
	if c.WriteTimeout < 0 {
		return errors.New("write timeout must be non-negative (0 to disable)")
	}

	if c.IdleTimeout <= 0 {
		return errors.New("idle timeout must be positive")
	}

	return nil
}

// DSN returns the database connection string
func (c *DatabaseConfig) DSN() string {
	// Use a builder for better performance and readability
	var b strings.Builder

	b.WriteString(c.User)

	if c.Password != "" {
		b.WriteByte(':')
		b.WriteString(c.Password)
	}

	b.WriteString("@tcp(")
	b.WriteString(c.Host)
	b.WriteByte(':')
	b.WriteString(c.Port)
	b.WriteByte(')')
	b.WriteByte('/')
	b.WriteString(c.DBName)
	b.WriteString("?charset=utf8mb4&parseTime=True&loc=UTC")

	if c.MaxOpenConns > 0 {
		b.WriteString("&maxAllowedPacket=0") // Let server control packet size
	}

	return b.String()
}

// LoadConfig loads configuration from environment variables
func LoadConfig() (*Config, error) {
	if err := loadDotEnv(); err != nil {
		return nil, err
	}

	cfg := &Config{
		App:        loadAppConfig(),
		Database:   loadDatabaseConfig(),
		AzureTable: loadAzureTableConfig(),
		Server:     loadServerConfig(),
		CORS: CORSConfig{
			AllowedOrigins: getEnv("CORS_ALLOWED_ORIGINS", "*"),
		},
		Logging: LogConfig{
			Level: getEnv("LOG_LEVEL", "info"),
			File:  getEnv("LOG_FILE", ""),
		},
		Auth:        loadAuthConfig(),
		GitProvider: loadGitProviderConfig(),
		Deployment:  loadDeploymentConfig(),
		OIDC:        loadOIDCConfig(),
		Otel:        loadOtelConfig(),
	}

	// Validate the configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// loadDotEnv loads the .env file if it exists.
func loadDotEnv() error {
	envFile := os.Getenv("ENV_FILE")
	if envFile == "" {
		envFile = ".env"
	}

	if _, err := os.Stat(envFile); err == nil {
		if err := godotenv.Load(envFile); err != nil {
			return fmt.Errorf("error loading %s: %w", envFile, err)
		}
	}
	return nil
}

func loadAppConfig() AppConfig {
	return AppConfig{
		Name:                      getEnv("APP_NAME", "backend-api"),
		Environment:               getEnv("GO_ENV", "development"),
		DefaultInstanceTTLMinutes: int(getEnvInt32("DEFAULT_INSTANCE_TTL_MINUTES", 0)),
		Debug:                     getEnvBool("APP_DEBUG", true),
		EnableSwagger:             getEnvBool("ENABLE_SWAGGER", false),
	}
}

func loadDatabaseConfig() DatabaseConfig {
	return DatabaseConfig{
		Host:            getEnv("DB_HOST", "localhost"),
		Port:            getEnv("DB_PORT", "3306"),
		User:            getEnv("DB_USER", "root"),
		Password:        getEnv("DB_PASSWORD", ""),
		DBName:          getEnv("DB_NAME", "app"),
		MaxOpenConns:    getEnvInt32("DB_MAX_OPEN_CONNS", defaultMaxOpenConns),
		MaxIdleConns:    getEnvInt32("DB_MAX_IDLE_CONNS", defaultMaxIdleConns),
		ConnMaxLifetime: getEnvDuration("DB_CONN_MAX_LIFETIME", defaultConnMaxLifetime),
	}
}

func loadAzureTableConfig() AzureTableConfig {
	return AzureTableConfig{
		AccountName:   getEnv("AZURE_TABLE_ACCOUNT_NAME", ""),
		AccountKey:    getEnv("AZURE_TABLE_ACCOUNT_KEY", ""),
		Endpoint:      getEnv("AZURE_TABLE_ENDPOINT", ""),
		TableName:     getEnv("AZURE_TABLE_NAME", "items"),
		UseAzureTable: getEnvBool("USE_AZURE_TABLE", false),
		UseAzurite:    getEnvBool("USE_AZURITE", false),
	}
}

func loadServerConfig() ServerConfig {
	return ServerConfig{
		Host:            getEnv("SERVER_HOST", ""),
		Port:            getEnv("SERVER_PORT", "8081"),
		ReadTimeout:     getEnvDuration("SERVER_READ_TIMEOUT", defaultReadTimeout),
		WriteTimeout:    getEnvDuration("SERVER_WRITE_TIMEOUT", defaultWriteTimeout),
		IdleTimeout:     getEnvDuration("SERVER_IDLE_TIMEOUT", defaultIdleTimeout),
		ShutdownTimeout: getEnvDuration("SERVER_SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
		RateLimit:       getEnvInt32("RATE_LIMIT", 100),
		LoginRateLimit:  getEnvInt32("LOGIN_RATE_LIMIT", 10),
		HealthVerbose:   getEnvBool("HEALTH_VERBOSE", false),
		PprofEnabled:    getEnvBool("PPROF_ENABLED", false),
		PprofAddr:       getEnv("PPROF_ADDR", "127.0.0.1:6060"),
	}
}

func loadAuthConfig() AuthConfig {
	return AuthConfig{
		JWTSecret:        getEnv("JWT_SECRET", ""),
		JWTExpiration:    getEnvDuration("JWT_EXPIRATION", 24*time.Hour),
		LoginCacheTTL:    getEnvDuration("LOGIN_CACHE_TTL", 30*time.Second),
		AdminUsername:    getEnv("ADMIN_USERNAME", "admin"),
		AdminPassword:    getEnv("ADMIN_PASSWORD", ""),
		SelfRegistration: getEnvBool("SELF_REGISTRATION", false),
		DefaultBranch:    getEnv("DEFAULT_BRANCH", "master"),
	}
}

func loadGitProviderConfig() GitProviderConfig {
	return GitProviderConfig{
		AzureDevOpsPAT:        getEnv("AZURE_DEVOPS_PAT", ""),
		AzureDevOpsDefaultOrg: getEnv("AZURE_DEVOPS_DEFAULT_ORG", ""),
		GitLabToken:           getEnv("GITLAB_TOKEN", ""),
		GitLabBaseURL:         getEnv("GITLAB_BASE_URL", ""),
	}
}

func loadDeploymentConfig() DeploymentConfig {
	return DeploymentConfig{
		HelmBinary:                getEnv("HELM_BINARY", "helm"),
		KubeconfigPath:            getEnv("KUBECONFIG_PATH", getEnv("KUBECONFIG", "")),
		KubeconfigEncryptionKey:   getEnv("KUBECONFIG_ENCRYPTION_KEY", ""),
		DeploymentTimeout:         getEnvDuration("DEPLOYMENT_TIMEOUT", 10*time.Minute),
		ClusterHealthPollInterval: getEnvDuration("CLUSTER_HEALTH_POLL_INTERVAL", 60*time.Second),
		MaxConcurrentDeploys:      getEnvInt32("MAX_CONCURRENT_DEPLOYS", 5),
	}
}

func loadOIDCConfig() OIDCConfig {
	return OIDCConfig{
		Enabled:       getEnvBool("OIDC_ENABLED", false),
		ProviderURL:   getEnv("OIDC_PROVIDER_URL", ""),
		ClientID:      getEnv("OIDC_CLIENT_ID", ""),
		ClientSecret:  getEnv("OIDC_CLIENT_SECRET", ""),
		RedirectURL:   getEnv("OIDC_REDIRECT_URL", ""),
		Scopes:        parseCSV(getEnv("OIDC_SCOPES", "openid,profile,email")),
		RoleClaim:     getEnv("OIDC_ROLE_CLAIM", "roles"),
		AdminRoles:    parseCSV(getEnv("OIDC_ADMIN_ROLES", "")),
		DevOpsRoles:   parseCSV(getEnv("OIDC_DEVOPS_ROLES", "")),
		AutoProvision: getEnvBool("OIDC_AUTO_PROVISION", true),
		LocalAuth:     getEnvBool("OIDC_LOCAL_AUTH", true),
		StateTTL:      getEnvDuration("OIDC_STATE_TTL", 5*time.Minute),
	}
}

func loadOtelConfig() OtelConfig {
	return OtelConfig{
		Enabled:     getEnvBool("OTEL_ENABLED", false),
		Endpoint:    getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		ServiceName: getEnv("OTEL_SERVICE_NAME", "k8s-stack-manager"),
		SampleRate:  getEnvFloat64("OTEL_TRACE_SAMPLE_RATE", 1.0),
	}
}

// Helper functions for environment variables
func getEnv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	return value
}

func getEnvBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	v, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}

	return v
}

//nolint:wsl // Function layout has been made consistent with other helpers
func getEnvInt32(key string, fallback int32) int32 {
	if value := os.Getenv(key); value != "" {
		v, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return fallback
		}
		return int32(v)
	}
	return fallback
}

func getEnvFloat64(key string, fallback float64) float64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	v, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}

	return v
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	v, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}

	return v
}

// parseCSV splits a comma-separated string into a trimmed slice, filtering out empty entries.
func parseCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
