// Package main provides the entry point for the backend API service.
// It handles configuration loading, database setup, and HTTP server initialization.
package main

import (
	_ "backend/docs"
	"backend/internal/api/handlers"
	"backend/internal/api/routes"
	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/database/azure"
	"backend/internal/gitprovider"
	"backend/internal/health"
	"backend/internal/helm"
	"backend/internal/websocket"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

const (
	defaultShutdownTimeout = 5 * time.Second
)

// @title           Backend API
// @version         1.0
// @description     This is the API documentation for the backend service
// @host            localhost:8081
// @BasePath        /
// @schemes         http https
// @produce         json
// @consumes        json
func main() {
	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		slog.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	// Initialize repository using the factory (selects MySQL or Azure Table based on config)
	repo, err := database.NewRepository(cfg)
	if err != nil {
		slog.Error("Failed to initialize repository", "error", err)
		os.Exit(1)
	}

	// Initialize health checker with actual database dependency
	healthChecker := health.New()
	healthChecker.AddCheck("database", func(ctx context.Context) error {
		return repo.Ping(ctx)
	})
	healthChecker.SetReady(true)

	// Create and start WebSocket hub
	hub := websocket.NewHub()
	go hub.Run()

	// ------------------------------------------------------------------
	// Phase 1: Create domain-specific Azure Table repositories
	// ------------------------------------------------------------------
	azCfg := cfg.AzureTable

	userRepo, err := azure.NewUserRepository(azCfg.AccountName, azCfg.AccountKey, azCfg.Endpoint, azCfg.UseAzurite)
	if err != nil {
		slog.Error("Failed to create user repository", "error", err)
		os.Exit(1)
	}

	templateRepo, err := azure.NewStackTemplateRepository(azCfg.AccountName, azCfg.AccountKey, azCfg.Endpoint, azCfg.UseAzurite)
	if err != nil {
		slog.Error("Failed to create stack template repository", "error", err)
		os.Exit(1)
	}

	templateChartRepo, err := azure.NewTemplateChartConfigRepository(azCfg.AccountName, azCfg.AccountKey, azCfg.Endpoint, azCfg.UseAzurite)
	if err != nil {
		slog.Error("Failed to create template chart config repository", "error", err)
		os.Exit(1)
	}

	definitionRepo, err := azure.NewStackDefinitionRepository(azCfg.AccountName, azCfg.AccountKey, azCfg.Endpoint, azCfg.UseAzurite)
	if err != nil {
		slog.Error("Failed to create stack definition repository", "error", err)
		os.Exit(1)
	}

	chartConfigRepo, err := azure.NewChartConfigRepository(azCfg.AccountName, azCfg.AccountKey, azCfg.Endpoint, azCfg.UseAzurite)
	if err != nil {
		slog.Error("Failed to create chart config repository", "error", err)
		os.Exit(1)
	}

	instanceRepo, err := azure.NewStackInstanceRepository(azCfg.AccountName, azCfg.AccountKey, azCfg.Endpoint, azCfg.UseAzurite)
	if err != nil {
		slog.Error("Failed to create stack instance repository", "error", err)
		os.Exit(1)
	}

	overrideRepo, err := azure.NewValueOverrideRepository(azCfg.AccountName, azCfg.AccountKey, azCfg.Endpoint, azCfg.UseAzurite)
	if err != nil {
		slog.Error("Failed to create value override repository", "error", err)
		os.Exit(1)
	}

	auditRepo, err := azure.NewAuditLogRepository(azCfg.AccountName, azCfg.AccountKey, azCfg.Endpoint, azCfg.UseAzurite)
	if err != nil {
		slog.Error("Failed to create audit log repository", "error", err)
		os.Exit(1)
	}

	// ------------------------------------------------------------------
	// Phase 1: Create domain services
	// ------------------------------------------------------------------
	gitRegistry := gitprovider.NewRegistry(gitprovider.Config{
		AzureDevOps: gitprovider.AzureDevOpsConfig{
			PAT:        cfg.GitProvider.AzureDevOpsPAT,
			DefaultOrg: cfg.GitProvider.AzureDevOpsDefaultOrg,
		},
		GitLab: gitprovider.GitLabConfig{
			Token:   cfg.GitProvider.GitLabToken,
			BaseURL: cfg.GitProvider.GitLabBaseURL,
		},
	})

	valuesGen := helm.NewValuesGenerator()

	// ------------------------------------------------------------------
	// Phase 1: Create handlers
	// ------------------------------------------------------------------
	authHandler := handlers.NewAuthHandler(userRepo, &cfg.Auth)
	templateHandler := handlers.NewTemplateHandler(templateRepo, templateChartRepo, definitionRepo, chartConfigRepo)
	definitionHandler := handlers.NewDefinitionHandler(definitionRepo, chartConfigRepo, instanceRepo)
	instanceHandler := handlers.NewInstanceHandler(
		instanceRepo, overrideRepo, definitionRepo, chartConfigRepo,
		templateRepo, templateChartRepo, valuesGen, userRepo,
	)
	gitHandler := handlers.NewGitHandler(gitRegistry)
	auditLogHandler := handlers.NewAuditLogHandler(auditRepo)

	// Auto-create admin user on startup if ADMIN_PASSWORD is set.
	authHandler.EnsureAdminUser()

	// Setup router — use gin.New() since SetupRoutes registers its own Logger and Recovery middleware.
	router := gin.New()
	rateLimiter := routes.SetupRoutes(router, routes.Deps{
		Repository:        repo,
		HealthChecker:     healthChecker,
		Config:            cfg,
		Hub:               hub,
		AuthHandler:       authHandler,
		TemplateHandler:   templateHandler,
		DefinitionHandler: definitionHandler,
		InstanceHandler:   instanceHandler,
		GitHandler:        gitHandler,
		AuditLogHandler:   auditLogHandler,
		AuditLogger:       auditRepo,
	})
	defer rateLimiter.Stop()
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// Create server with timeouts
	srv := &http.Server{
		Addr:         fmt.Sprintf("%s:%s", cfg.Server.Host, cfg.Server.Port),
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	// Start server in a goroutine
	go func() {
		slog.Info("Server starting", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Failed to start server", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("Shutting down server...")

	// Shut down WebSocket hub (closes all client connections)
	hub.Shutdown()

	// Give outstanding requests time to complete
	shutdownTimeout := cfg.Server.ShutdownTimeout
	if shutdownTimeout == 0 {
		shutdownTimeout = defaultShutdownTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	err = srv.Shutdown(ctx)

	// Close repository connections (database pool, etc.)
	if closeErr := repo.Close(); closeErr != nil {
		slog.Error("Failed to close repository", "error", closeErr)
	}

	if err != nil {
		slog.Error("Server forced to shutdown", "error", err)
		return
	}

	slog.Info("Server exited gracefully")
}
