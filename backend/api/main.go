// Package main provides the entry point for the backend API service.
// It handles configuration loading, database setup, and HTTP server initialization.
package main

import (
	"backend/internal/api/handlers"
	"backend/internal/api/routes"
	"backend/internal/auth"
	"backend/internal/cluster"
	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/deployer"
	"backend/internal/gitprovider"
	"backend/internal/health"
	"backend/internal/helm"
	"backend/internal/k8s"
	"backend/internal/models"
	"backend/internal/scheduler"
	"backend/internal/telemetry"
	"backend/internal/ttl"
	"backend/internal/websocket"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	defaultShutdownTimeout = 5 * time.Second
)

// must logs a fatal error and exits if err is non-nil.
// Used to reduce repetitive error-checking boilerplate during startup.
func must(name string, err error) {
	if err != nil {
		slog.Error("Failed to create "+name, "error", err)
		os.Exit(1)
	}
}

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

	// Initialize OpenTelemetry (no-op when OTEL_ENABLED=false).
	tel, err := telemetry.Init(cfg.Otel)
	if err != nil {
		slog.Error("Failed to initialize OpenTelemetry", "error", err)
		os.Exit(1)
	}

	// Initialize repository using the factory (selects MySQL or Azure Table based on config)
	repo, mysqlGormDB, err := database.NewRepositoryWithGormDB(cfg)
	if err != nil {
		slog.Error("Failed to initialize repository", "error", err)
		os.Exit(1)
	}

	// Register database/sql pool metrics when using MySQL with OTel.
	if cfg.Otel.Enabled && mysqlGormDB != nil {
		sqlDB, err := mysqlGormDB.DB()
		if err == nil {
			if err := telemetry.StartDBMetrics(sqlDB); err != nil {
				slog.Warn("Failed to register DB pool metrics", "error", err)
			}
		}
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
	// Create all domain-specific repositories via factory
	// ------------------------------------------------------------------
	repos, err := database.NewRepositorySet(cfg, mysqlGormDB)
	if err != nil {
		slog.Error("Failed to create domain repositories", "error", err)
		os.Exit(1)
	}

	userRepo := repos.User
	templateRepo := repos.StackTemplate
	templateChartRepo := repos.TemplateChartConfig
	definitionRepo := repos.StackDefinition
	chartConfigRepo := repos.ChartConfig
	instanceRepo := repos.StackInstance
	overrideRepo := repos.ValueOverride
	branchOverrideRepo := repos.ChartBranchOverride
	auditRepo := repos.AuditLog
	apiKeyRepo := repos.APIKey
	deployLogRepo := repos.DeploymentLog
	sharedValuesRepo := repos.SharedValues
	quotaRepo := repos.ResourceQuota
	quotaOverrideRepo := repos.InstanceQuotaOverride
	templateVersionRepo := repos.TemplateVersion
	notificationRepo := repos.Notification
	favoriteRepo := repos.UserFavorite
	cleanupPolicyRepo := repos.CleanupPolicy
	clusterRepo := repos.Cluster

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

	// Auto-create default cluster from KUBECONFIG_PATH for single-cluster migration
	ensureDefaultCluster(clusterRepo, instanceRepo, cfg)

	// Create cluster registry for multi-cluster client management
	clusterRegistry := cluster.NewRegistry(cluster.RegistryConfig{
		ClusterRepo: clusterRepo,
		HelmBinary:  cfg.Deployment.HelmBinary,
		HelmTimeout: cfg.Deployment.DeploymentTimeout,
	})

	// Register extended health checks for cluster, git, and helm.
	healthChecker.AddCheck("cluster_registry", func(ctx context.Context) error {
		return clusterRegistry.HealthCheck(ctx)
	})
	healthChecker.AddCheck("git_provider", func(ctx context.Context) error {
		return gitRegistry.HealthCheck(ctx)
	})
	healthChecker.AddCheck("helm", deployer.HelmHealthCheck(cfg.Deployment.HelmBinary))

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
	k8sWatcher.Start(watcherCtx)

	// Deployment manager — uses registry for multi-cluster deploys
	deployManager := deployer.NewManager(deployer.ManagerConfig{
		Registry:          clusterRegistry,
		InstanceRepo:      instanceRepo,
		DeployLogRepo:     deployLogRepo,
		Hub:               hub,
		TxRunner:          repos.TxRunner,
		MaxConcurrent:     int(cfg.Deployment.MaxConcurrentDeploys),
		QuotaRepo:         quotaRepo,
		QuotaOverrideRepo: quotaOverrideRepo,
	})

	// ------------------------------------------------------------------
	// Phase 1: Create handlers
	// ------------------------------------------------------------------
	authHandler := handlers.NewAuthHandler(userRepo, &cfg.Auth)

	// OIDC authentication — conditionally initialize when enabled.
	var oidcHandler *handlers.OIDCHandler
	var oidcStateStore *auth.StateStore
	if cfg.OIDC.Enabled {
		oidcProvider, oidcErr := auth.NewProvider(context.Background(), &cfg.OIDC)
		if oidcErr != nil {
			slog.Error("Failed to initialize OIDC provider", "error", oidcErr)
			os.Exit(1)
		}
		oidcStateStore = auth.NewStateStore(cfg.OIDC.StateTTL)
		oidcHandler = handlers.NewOIDCHandler(oidcProvider, oidcStateStore, userRepo, &cfg.OIDC, &cfg.Auth)
		slog.Info("OIDC authentication enabled", "provider_url", cfg.OIDC.ProviderURL)
	}

	templateHandler := handlers.NewTemplateHandlerWithVersions(templateRepo, templateChartRepo, definitionRepo, chartConfigRepo, templateVersionRepo)
	templateHandler.SetTxRunner(repos.TxRunner)
	definitionHandler := handlers.NewDefinitionHandlerWithVersions(definitionRepo, chartConfigRepo, instanceRepo, templateRepo, templateChartRepo, templateVersionRepo)
	definitionHandler.SetTxRunner(repos.TxRunner)
	templateVersionHandler := handlers.NewTemplateVersionHandler(templateVersionRepo, templateRepo)
	instanceHandler := handlers.NewInstanceHandlerWithDeployer(
		instanceRepo, overrideRepo, branchOverrideRepo, definitionRepo, chartConfigRepo,
		templateRepo, templateChartRepo, valuesGen, userRepo,
		deployManager, k8sWatcher, clusterRegistry, deployLogRepo, clusterRepo,
		cfg.App.DefaultInstanceTTLMinutes,
	)
	instanceHandler.SetTxRunner(repos.TxRunner)
	gitHandler := handlers.NewGitHandler(gitRegistry)
	auditLogHandler := handlers.NewAuditLogHandler(auditRepo)
	userHandler := handlers.NewUserHandler(userRepo)
	apiKeyHandler := handlers.NewAPIKeyHandler(apiKeyRepo, userRepo)

	adminHandler := handlers.NewAdminHandler(clusterRegistry, instanceRepo)
	clusterHandler := handlers.NewClusterHandlerWithQuotas(clusterRepo, clusterRegistry, instanceRepo, quotaRepo)
	branchOverrideHandler := handlers.NewBranchOverrideHandler(branchOverrideRepo, instanceRepo)
	instanceQuotaOverrideHandler := handlers.NewInstanceQuotaOverrideHandler(quotaOverrideRepo, instanceRepo)
	sharedValuesHandler := handlers.NewSharedValuesHandler(sharedValuesRepo, clusterRepo)

	notificationHandler := handlers.NewNotificationHandler(notificationRepo)

	favoriteHandler := handlers.NewFavoriteHandler(favoriteRepo)

	quickDeployHandler := handlers.NewQuickDeployHandler(
		templateRepo, templateChartRepo, definitionRepo, chartConfigRepo,
		instanceRepo, branchOverrideRepo, overrideRepo, valuesGen,
		deployManager, userRepo, deployLogRepo, auditRepo,
		hub, clusterRegistry, k8sWatcher,
		cfg.App.DefaultInstanceTTLMinutes,
	)
	quickDeployHandler.SetTxRunner(repos.TxRunner)

	analyticsHandler := handlers.NewAnalyticsHandler(templateRepo, definitionRepo, instanceRepo, deployLogRepo, userRepo)

	// ------------------------------------------------------------------
	// Phase 6.2: Cleanup policies
	// ------------------------------------------------------------------
	cleanupExecutor := deployer.NewCleanupExecutor(deployManager, definitionRepo, chartConfigRepo, instanceRepo)
	cleanupScheduler := scheduler.NewScheduler(cleanupPolicyRepo, instanceRepo, auditRepo, cleanupExecutor)
	cleanupPolicyHandler := handlers.NewCleanupPolicyHandler(cleanupPolicyRepo, cleanupScheduler)

	// Auto-create admin user on startup if ADMIN_PASSWORD is set.
	authHandler.EnsureAdminUser()

	// Setup router — use gin.New() since SetupRoutes registers its own Logger and Recovery middleware.
	router := gin.New()
	rateLimiter := routes.SetupRoutes(router, routes.Deps{
		Repository:                   repo,
		HealthChecker:                healthChecker,
		Config:                       cfg,
		Hub:                          hub,
		AuthHandler:                  authHandler,
		TemplateHandler:              templateHandler,
		DefinitionHandler:            definitionHandler,
		InstanceHandler:              instanceHandler,
		GitHandler:                   gitHandler,
		AuditLogHandler:              auditLogHandler,
		AuditLogger:                  auditRepo,
		UserHandler:                  userHandler,
		APIKeyHandler:                apiKeyHandler,
		AdminHandler:                 adminHandler,
		BranchOverrideHandler:        branchOverrideHandler,
		InstanceQuotaOverrideHandler: instanceQuotaOverrideHandler,
		TemplateVersionHandler:       templateVersionHandler,
		NotificationHandler:          notificationHandler,
		FavoriteHandler:              favoriteHandler,
		QuickDeployHandler:           quickDeployHandler,
		AnalyticsHandler:             analyticsHandler,
		CleanupPolicyHandler:         cleanupPolicyHandler,
		CleanupScheduler:             cleanupScheduler,
		ClusterHandler:               clusterHandler,
		SharedValuesHandler:          sharedValuesHandler,
		UserRepo:                     userRepo,
		APIKeyRepo:                   apiKeyRepo,
		OIDCHandler:                  oidcHandler,
	})
	defer rateLimiter.Stop()

	// Start TTL reaper for auto-expiring stack instances.
	expiryStopper := deployer.NewExpiryStopper(deployManager, definitionRepo, chartConfigRepo)
	reaper := ttl.NewReaper(instanceRepo, auditRepo, hub, expiryStopper, 60*time.Second)
	go reaper.Start()

	// Start cleanup scheduler.
	if err := cleanupScheduler.Start(); err != nil {
		slog.Error("Failed to start cleanup scheduler", "error", err)
		os.Exit(1)
	}

	// Start pprof server on a separate port when PPROF_ENABLED=true.
	// Access at http://localhost:6060/debug/pprof/
	if cfg.Server.PprofEnabled {
		pprofMux := http.NewServeMux()
		pprofMux.HandleFunc("/debug/pprof/", pprof.Index)
		pprofMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		pprofMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		pprofMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		pprofMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		pprofSrv := &http.Server{
			Addr:         cfg.Server.PprofAddr,
			Handler:      pprofMux,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
		go func() {
			slog.Info("pprof server starting", "addr", cfg.Server.PprofAddr)
			if err := pprofSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("pprof server failed", "error", err)
			}
		}()
	}

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

	shutdownTimeout := cfg.Server.ShutdownTimeout
	if shutdownTimeout == 0 {
		shutdownTimeout = defaultShutdownTimeout
	}

	gracefulShutdown(srv, shutdownTimeout, shutdownDeps{
		telemetry:        tel,
		reaper:           reaper,
		cleanupScheduler: cleanupScheduler,
		deployManager:    deployManager,
		healthPoller:     healthPoller,
		k8sWatcher:       k8sWatcher,
		hub:              hub,
		clusterRegistry:  clusterRegistry,
		watcherCancel:    watcherCancel,
		oidcStateStore:   oidcStateStore,
		repo:             repo,
	})
}

// shutdownDeps holds all dependencies that need to be stopped during graceful shutdown.
type shutdownDeps struct {
	telemetry        *telemetry.Telemetry
	reaper           *ttl.Reaper
	cleanupScheduler *scheduler.Scheduler
	deployManager    *deployer.Manager
	healthPoller     *cluster.HealthPoller
	k8sWatcher       *k8s.Watcher
	hub              *websocket.Hub
	clusterRegistry  *cluster.Registry
	watcherCancel    context.CancelFunc
	oidcStateStore   *auth.StateStore
	repo             models.Repository
}

// gracefulShutdown stops all services in the correct order.
func gracefulShutdown(srv *http.Server, timeout time.Duration, deps shutdownDeps) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 1. Stop HTTP server — no new requests.
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("Server forced to shutdown", "error", err)
	}

	// 2. Stop producers of deploy work.
	deps.reaper.Stop()
	deps.cleanupScheduler.Stop()

	// 3. Now safe to wait for in-flight deploys.
	deps.deployManager.Shutdown()

	// 4. Stop remaining services.
	deps.healthPoller.Stop()
	if deps.k8sWatcher != nil {
		deps.k8sWatcher.Stop()
	}
	deps.hub.Shutdown()
	deps.clusterRegistry.Close()
	deps.watcherCancel()
	if deps.oidcStateStore != nil {
		deps.oidcStateStore.Stop()
	}

	// 5. Flush OTel spans/metrics/logs AFTER all services stop,
	//    so telemetry from graceful shutdown is captured.
	if deps.telemetry != nil {
		if err := deps.telemetry.Shutdown(ctx); err != nil {
			slog.Error("Failed to shutdown OpenTelemetry", "error", err)
		}
	}

	// 6. Close data layer.
	if closeErr := deps.repo.Close(); closeErr != nil {
		slog.Error("Failed to close repository", "error", closeErr)
	}

	slog.Info("Server exited gracefully")
}

// ensureDefaultCluster auto-creates a default cluster from KUBECONFIG_PATH
// when no clusters exist yet. This provides a migration path for existing
// single-cluster setups.
func ensureDefaultCluster(clusterRepo models.ClusterRepository, instanceRepo models.StackInstanceRepository, cfg *config.Config) {
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

	// Backfill: persist the new default cluster ID onto any existing
	// StackInstance records that have an empty ClusterID, so they won't
	// implicitly follow whatever happens to be the default later.
	backfillInstanceClusterIDs(instanceRepo, defaultCluster.ID)
}

// backfillInstanceClusterIDs sets the given clusterID on all stack instances
// that currently have an empty ClusterID. It intentionally lists all instances
// and filters in-memory so that storage backends where missing properties are
// not equal to empty strings (e.g., Azure Table Storage) are handled correctly.
func backfillInstanceClusterIDs(instanceRepo models.StackInstanceRepository, clusterID string) {
	instances, err := instanceRepo.List()
	if err != nil {
		slog.Warn("Failed to list instances for cluster ID backfill", "error", err)
		return
	}

	backfilledCount := 0
	for i := range instances {
		inst := &instances[i]
		if inst.ClusterID != "" {
			continue
		}

		inst.ClusterID = clusterID
		if updateErr := instanceRepo.Update(inst); updateErr != nil {
			slog.Error("Failed to backfill cluster ID on instance",
				"instance_id", inst.ID,
				"error", updateErr,
			)
			continue
		}
		backfilledCount++
	}

	if backfilledCount > 0 {
		slog.Info("Backfilled default cluster ID on existing instances",
			"cluster_id", clusterID,
			"count", backfilledCount,
		)
	}
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
