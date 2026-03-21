// Package main provides the entry point for the backend API service.
// It handles configuration loading, database setup, and HTTP server initialization.
package main

import (
	_ "backend/docs"
	"backend/internal/api/handlers"
	"backend/internal/api/routes"
	"backend/internal/cluster"
	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/database/azure"
	"backend/internal/deployer"
	"backend/internal/gitprovider"
	"backend/internal/health"
	"backend/internal/helm"
	"backend/internal/k8s"
	"backend/internal/models"
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
	"github.com/google/uuid"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"k8s.io/client-go/tools/clientcmd"
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

// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description JWT Bearer token (format: "Bearer <token>")

// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name X-API-Key
// @description API key (format: "sk_<key>")
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

	apiKeyRepo, err := azure.NewAPIKeyRepository(azCfg.AccountName, azCfg.AccountKey, azCfg.Endpoint, azCfg.UseAzurite)
	if err != nil {
		slog.Error("Failed to create API key repository", "error", err)
		os.Exit(1)
	}

	deployLogRepo, err := azure.NewDeploymentLogRepository(azCfg.AccountName, azCfg.AccountKey, azCfg.Endpoint, azCfg.UseAzurite)
	if err != nil {
		slog.Error("Failed to create deployment log repository", "error", err)
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
	// Phase 3: Create deployment services
	// ------------------------------------------------------------------

	// Create cluster repository
	clusterRepo, err := azure.NewClusterRepository(azCfg.AccountName, azCfg.AccountKey, azCfg.Endpoint, azCfg.UseAzurite, cfg.Deployment.KubeconfigEncryptionKey)
	if err != nil {
		slog.Error("Failed to create cluster repository", "error", err)
		os.Exit(1)
	}

	// Auto-create default cluster from KUBECONFIG_PATH for single-cluster migration
	ensureDefaultCluster(clusterRepo, cfg)

	// Create cluster registry for multi-cluster client management
	clusterRegistry := cluster.NewRegistry(cluster.RegistryConfig{
		ClusterRepo: clusterRepo,
		HelmBinary:  cfg.Deployment.HelmBinary,
		HelmTimeout: cfg.Deployment.DeploymentTimeout,
	})
	defer clusterRegistry.Close()

	// Start cluster health poller
	healthPoller := cluster.NewHealthPoller(cluster.HealthPollerConfig{
		ClusterRepo: clusterRepo,
		Registry:    clusterRegistry,
		Interval:    cfg.Deployment.ClusterHealthPollInterval,
		Hub:         hub,
	})
	healthPoller.Start()

	// K8s watcher — uses registry for multi-cluster monitoring
	k8sWatcher := k8s.NewWatcher(clusterRegistry, instanceRepo, hub, 30*time.Second)
	watcherCtx, watcherCancel := context.WithCancel(context.Background())
	defer watcherCancel()
	k8sWatcher.Start(watcherCtx)

	// Deployment manager — uses registry for multi-cluster deploys
	deployManager := deployer.NewManager(deployer.ManagerConfig{
		Registry:      clusterRegistry,
		InstanceRepo:  instanceRepo,
		DeployLogRepo: deployLogRepo,
		Hub:           hub,
		MaxConcurrent: int(cfg.Deployment.MaxConcurrentDeploys),
	})

	// ------------------------------------------------------------------
	// Phase 1: Create handlers
	// ------------------------------------------------------------------
	authHandler := handlers.NewAuthHandler(userRepo, &cfg.Auth)
	templateHandler := handlers.NewTemplateHandler(templateRepo, templateChartRepo, definitionRepo, chartConfigRepo)
	definitionHandler := handlers.NewDefinitionHandler(definitionRepo, chartConfigRepo, instanceRepo, templateRepo, templateChartRepo)
	instanceHandler := handlers.NewInstanceHandlerWithDeployer(
		instanceRepo, overrideRepo, definitionRepo, chartConfigRepo,
		templateRepo, templateChartRepo, valuesGen, userRepo,
		deployManager, k8sWatcher, clusterRegistry, deployLogRepo,
	)
	gitHandler := handlers.NewGitHandler(gitRegistry)
	auditLogHandler := handlers.NewAuditLogHandler(auditRepo)
	userHandler := handlers.NewUserHandler(userRepo)
	apiKeyHandler := handlers.NewAPIKeyHandler(apiKeyRepo, userRepo)

	adminHandler := handlers.NewAdminHandler(clusterRegistry, instanceRepo)
	clusterHandler := handlers.NewClusterHandler(clusterRepo, clusterRegistry, instanceRepo)

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
		UserHandler:       userHandler,
		APIKeyHandler:     apiKeyHandler,
		AdminHandler:      adminHandler,
		ClusterHandler:    clusterHandler,
		UserRepo:          userRepo,
		APIKeyRepo:        apiKeyRepo,
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

	// Cancel in-flight deploy/stop goroutines
	deployManager.Shutdown()

	// Stop cluster health poller
	healthPoller.Stop()

	// Stop K8s status watcher
	if k8sWatcher != nil {
		k8sWatcher.Stop()
	}

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

// ensureDefaultCluster auto-creates a default cluster from KUBECONFIG_PATH
// when no clusters exist yet. This provides a migration path for existing
// single-cluster setups.
func ensureDefaultCluster(clusterRepo models.ClusterRepository, cfg *config.Config) {
	clusters, err := clusterRepo.List()
	if err != nil {
		slog.Error("Failed to list clusters for default cluster check", "error", err)
		return
	}
	if len(clusters) > 0 {
		return
	}
	if cfg.Deployment.KubeconfigPath == "" {
		return
	}

	// Extract the API server URL from the kubeconfig so the auto-created
	// default cluster satisfies the Validate() requirement.
	apiServerURL := extractAPIServerURL(cfg.Deployment.KubeconfigPath)
	if apiServerURL == "" {
		slog.Warn("Cannot auto-create default cluster: unable to determine API server URL from kubeconfig",
			"kubeconfig_path", cfg.Deployment.KubeconfigPath)
		return
	}

	defaultCluster := &models.Cluster{
		ID:             uuid.New().String(),
		Name:           "default",
		Description:    "Auto-created from KUBECONFIG_PATH",
		KubeconfigPath: cfg.Deployment.KubeconfigPath,
		APIServerURL:   apiServerURL,
		IsDefault:      true,
		HealthStatus:   models.ClusterUnreachable,
	}
	if createErr := clusterRepo.Create(defaultCluster); createErr != nil {
		slog.Error("Failed to auto-create default cluster", "error", createErr)
		return
	}
	slog.Info("auto-created default cluster from KUBECONFIG_PATH",
		"cluster_id", defaultCluster.ID,
		"kubeconfig_path", cfg.Deployment.KubeconfigPath,
		"api_server_url", apiServerURL,
	)
}

// extractAPIServerURL reads the kubeconfig file and returns the API server URL
// from the current context. Returns empty string if the URL cannot be determined.
func extractAPIServerURL(kubeconfigPath string) string {
	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		slog.Warn("Failed to parse kubeconfig for API server URL", "path", kubeconfigPath, "error", err)
		return ""
	}
	return restCfg.Host
}
